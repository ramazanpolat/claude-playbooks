package cmd

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/term"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

// Sandboxed launch: `run --sandbox <name>` (or `start --sandbox <path>`)
// runs Claude Code inside a sandbox: a microVM with its own kernel,
// filesystem and network stack, driven through a backend CLI (today only
// `sbx`, Docker Sandboxes). Only what is mounted crosses the boundary: the
// config directory's root and the working directory, plus whatever the
// manifest's [sandbox] block or --mount adds. The environment is the same
// stack every launch gets (registry default, profiles, the block, one-off
// flags), reduced to what those layers SET plus the authentication
// variables, so the host's environment does not leak in. Network egress is
// the sandbox policy's, widened per sandbox by [sandbox].allow_net and the
// host of ANTHROPIC_BASE_URL when the env points elsewhere.
//
// One sandbox per playbook, named cpb-<playbook> (cpbstart-<dir> for
// start), reused across launches so installed tools and the agent's state
// persist; --sandbox-fresh recreates it. The sandbox runs the image's own
// Claude Code unless [sandbox].claude_version pins one, installed once at
// creation. A manifest with [sandbox].always = true sandboxes every launch;
// --no-sandbox overrides that for one launch, loudly.

// defaultSandboxBackend is the backend used when neither the flag nor the
// manifest names one.
const defaultSandboxBackend = "sbx"

// sandboxOpts are the --sandbox family of run/start flags.
type sandboxOpts struct {
	enabled  bool   // --sandbox, --sbx, --sandbox=BACKEND
	disabled bool   // --no-sandbox
	backend  string // --sandbox=BACKEND
	fresh    bool
	clone    bool
	workdir  string
	workdirs []string // every --workdir as typed, in order (the last wins)
	mounts   []string
	host     string // --sandbox-host user@host: run the launch there
}

// launchToken is one launch flag as typed, not yet evaluated: a remote
// launch forwards it verbatim, a local one reads it (env files included)
// only once it is known to be local.
type launchToken struct {
	flag  string
	value string
}

// scanLaunchFlagsWith is takeLaunchFlagsWith without the evaluation: it
// consumes the same leading run and records the flags as tokens.
func scanLaunchFlagsWith(args []string, bools map[string]*bool) (rest []string, tokens []launchToken, err error) {
	i := 0
	for i < len(args) {
		if args[i] == "--" {
			break
		}
		if dst, ok := bools[args[i]]; ok {
			*dst = true
			i++
			continue
		}
		flag, value, inline := strings.Cut(args[i], "=")
		if !launchFlagNames[flag] {
			break
		}
		if !inline {
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("flag needs an argument: %s", flag)
			}
			value = args[i+1]
			i++
		}
		i++
		tokens = append(tokens, launchToken{flag: flag, value: value})
	}
	return args[i:], tokens, nil
}

// launchLayers evaluates tokens into env layers, in order, exactly as
// takeLaunchFlagsWith would have.
func launchLayers(tokens []launchToken) ([]*manifest.Env, error) {
	var layers []*manifest.Env
	for _, t := range tokens {
		layer, err := launchLayer(t.flag, t.value)
		if err != nil {
			return nil, err
		}
		layers = append(layers, layer)
	}
	return layers, nil
}

// sandboxTarget is what a sandboxed launch runs: a registered playbook or
// a start directory.
type sandboxTarget struct {
	label      string            // for messages: `playbook "x"` or `directory /p`
	name       string            // sandbox name
	configPath string            // absolute config directory, registry spelling
	rootPath   string            // absolute root to mount (contains configPath)
	manifest   *manifest.Sandbox // [sandbox] defaults, may be nil
	backend    string            // resolved backend name
}

var sandboxNameClean = regexp.MustCompile(`[^A-Za-z0-9.+-]+`)

func cleanSandboxName(s string) string {
	return strings.Trim(sandboxNameClean.ReplaceAllString(s, "-"), "-")
}

// sandboxName is the sandbox name for a playbook: the characters sbx
// accepts (letters, digits, hyphens, periods, plus and minus), anything
// else folded to a hyphen.
func sandboxName(playbookName string) string {
	return "cpb-" + cleanSandboxName(playbookName)
}

// startSandboxName names the sandbox of a start directory. The prefix
// differs from a registered playbook's before the first hyphen, so no
// playbook name (letters, digits, dashes, underscores; underscores fold
// to dashes) can produce it: "start-x" and "start_x" both give
// cpb-start-x, never cpbstart-x.
func startSandboxName(dir string) string {
	return "cpbstart-" + cleanSandboxName(filepath.Base(dir))
}

// takeSandboxValueFlags consumes the leading --workdir PATH, --mount
// PATH[:ro] and --sandbox=BACKEND flags (the first two also as separate
// tokens). It returns the rest and whether anything was consumed, so the
// caller can alternate with the launch-flag scanner until neither makes
// progress.
func takeSandboxValueFlags(args []string, opts *sandboxOpts) (rest []string, consumed bool, err error) {
	i := 0
	for i < len(args) && args[i] != "--" {
		flag, value, inline := strings.Cut(args[i], "=")
		if flag == "--sandbox" && inline {
			if value == "" {
				return nil, false, fmt.Errorf("flag needs a non-empty argument: --sandbox=BACKEND")
			}
			opts.enabled = true
			opts.backend = value
			i++
			consumed = true
			continue
		}
		if flag != "--workdir" && flag != "--mount" && flag != "--sandbox-host" {
			break
		}
		if !inline {
			if i+1 >= len(args) {
				return nil, false, fmt.Errorf("flag needs an argument: %s", flag)
			}
			value = args[i+1]
			i++
		}
		i++
		if value == "" {
			return nil, false, fmt.Errorf("flag needs a non-empty argument: %s", flag)
		}
		switch flag {
		case "--workdir":
			opts.workdir = value
			opts.workdirs = append(opts.workdirs, value)
		case "--mount":
			opts.mounts = append(opts.mounts, value)
		case "--sandbox-host":
			opts.host = value
		}
		consumed = true
	}
	return args[i:], consumed, nil
}

// takeRunFlags scans one leading run of launch flags and sandbox flags in
// any order, recording the launch flags as tokens (unevaluated). extra
// adds command-specific boolean flags (start's --delete).
func takeRunFlags(args []string, opts *sandboxOpts, extra map[string]*bool) (rest []string, tokens []launchToken, err error) {
	bools := map[string]*bool{
		"--sandbox": &opts.enabled, "--sbx": &opts.enabled, "--no-sandbox": &opts.disabled,
		"--sandbox-fresh": &opts.fresh, "--clone": &opts.clone,
	}
	for k, v := range extra {
		bools[k] = v
	}
	rest = args
	for {
		var more []launchToken
		rest, more, err = scanLaunchFlagsWith(rest, bools)
		if err != nil {
			return nil, nil, err
		}
		tokens = append(tokens, more...)
		var consumed bool
		rest, consumed, err = takeSandboxValueFlags(rest, opts)
		if err != nil {
			return nil, nil, err
		}
		if !consumed {
			return rest, tokens, nil
		}
	}
}

// resolveSandbox decides whether this launch is sandboxed and with which
// backend: the flags first, then the manifest's `always`, which
// --no-sandbox overrides for one launch with a line on stderr so the
// override never passes silently. The sandbox-only flags without a
// sandbox are an error, as is an unknown backend.
func resolveSandbox(sb *manifest.Sandbox, opts *sandboxOpts, label string) (on bool, backend string, err error) {
	if opts.enabled && opts.disabled {
		return false, "", fmt.Errorf("--sandbox and --no-sandbox together: pick one")
	}
	if opts.host != "" && opts.disabled {
		return false, "", fmt.Errorf("--sandbox-host and --no-sandbox together: pick one")
	}
	// A host names where the sandbox runs; it implies one.
	if opts.host != "" {
		opts.enabled = true
	}
	always := sb != nil && sb.Always
	on = opts.enabled || (always && !opts.disabled)
	if always && opts.disabled {
		fmt.Fprintf(os.Stderr, "Sandbox off for this launch: %s is always sandboxed by its manifest\n", label)
	}
	if !on {
		if opts.fresh || opts.clone || opts.workdir != "" || len(opts.mounts) > 0 {
			return false, "", fmt.Errorf("--sandbox-fresh, --clone, --workdir and --mount apply to a sandboxed launch: add --sandbox")
		}
		return false, "", nil
	}
	backend = opts.backend
	if backend == "" && sb != nil {
		backend = sb.Backend
	}
	if backend == "" {
		backend = defaultSandboxBackend
	}
	if !manifest.KnownSandboxBackend(backend) {
		return false, "", fmt.Errorf("unknown sandbox backend %q (available: %s)", backend, strings.Join(manifest.SandboxBackends, ", "))
	}
	return true, backend, nil
}

// remotePathPrefix puts the installer's default (~/.local/bin) and the
// package managers' directories ahead of the PATH an ssh session gets.
const remotePathPrefix = `PATH="$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:$PATH" `

// remoteTransport wraps a POSIX sh command so that any remote login shell
// delivers it to sh unchanged: base64 in an environment variable, decoded
// and evaluated by sh. base64 --decode is GNU and BSD alike.
func remoteTransport(command string) string {
	return "env CPB_CMD=" + base64.StdEncoding.EncodeToString([]byte(command)) + ` sh -c 'eval "$(printf %s "$CPB_CMD" | base64 --decode)"'`
}

// sandboxHost is the remote host for a sandboxed launch: the flag, else
// the manifest, else "" (here).
func sandboxHost(sb *manifest.Sandbox, opts *sandboxOpts) string {
	if opts.host != "" {
		return opts.host
	}
	if sb != nil {
		return sb.Host
	}
	return ""
}

// forwardToSandboxHost runs this launch on host instead: the same
// subcommand, rebuilt from what the launch parser consumed (never from
// the raw text, so claude's own arguments are carried verbatim and
// nothing in them is mistaken for a flag), over ssh, where claude-playbook
// and the target are installed. Every value flag is forwarded in its
// inline form (--flag=value), so a value that looks like a flag stays a
// value on the remote side too. Launch flags travel as typed, unevaluated:
// no env file is read here, and --env-file refuses (a local file the
// remote cannot read). The arguments travel as one quoted command string
// (ssh hands the remote shell a string). ssh's own options end with "--"
// before the destination, so a host is never read as an option, and the
// host is validated like the manifest key. --sandbox is always present,
// so the remote launch is sandboxed there whatever its manifest says; a
// --playbooks-dir travels as given (a path on that host). A pty is
// requested only when this process has a terminal on both ends, as for
// the local attach.
func forwardToSandboxHost(host, subcommand string, original []string, opts *sandboxOpts, tokens []launchToken, wrapper []string, target string, claudeArgs []string) error {
	if !manifest.ValidSandboxHost(host) {
		return fmt.Errorf("--sandbox-host %q must be an ssh destination such as user@host", host)
	}
	for _, t := range tokens {
		if t.flag == "--env-file" {
			return fmt.Errorf("--env-file names a local file: a launch on %s cannot read it. Use an env profile on that host, or --env KEY=VALUE", host)
		}
	}
	var f []string
	if _, dir, err := scanPlaybooksDirArg(original); err == nil && dir != "" {
		f = append(f, "--playbooks-dir="+dir)
	}
	sandbox := "--sandbox"
	if opts.backend != "" {
		sandbox += "=" + opts.backend
	}
	f = append(f, sandbox)
	if opts.fresh {
		f = append(f, "--sandbox-fresh")
	}
	if opts.clone {
		f = append(f, "--clone")
	}
	// A value that is exactly "--" is the one value the inline form would
	// change: the remote registry scan stops at a standalone "--" as the
	// local one did, so such a value travels as two words.
	pair := func(flag, value string) []string {
		if value == "--" {
			return []string{flag, value}
		}
		return []string{flag + "=" + value}
	}
	for _, w := range opts.workdirs {
		// Every occurrence, in order: the last wins on the remote side as
		// here, and an overridden bare "--" keeps the boundary it set.
		f = append(f, pair("--workdir", w)...)
	}
	for _, m := range opts.mounts {
		f = append(f, pair("--mount", m)...)
	}
	for _, t := range tokens {
		f = append(f, pair(t.flag, t.value)...)
	}
	f = append(f, wrapper...)
	f = append(f, target)
	f = append(f, claudeArgs...)
	quoted := []string{"claude-playbook", subcommand}
	for _, a := range f {
		quoted = append(quoted, shell.QuoteArg(a))
	}
	command := strings.Join(quoted, " ")
	// ssh hands the command text to the remote user's login shell, whose
	// family is unknown (tcsh breaks POSIX single quotes on a newline and
	// expands "!"), and whose non-interactive PATH lacks ~/.local/bin. So
	// the command is not parsed by that shell at all: it travels base64
	// in an environment variable, and an explicit POSIX sh decodes and
	// evaluates it. The outer text is plain words every shell family
	// passes through untouched; the inner text is parsed by sh only,
	// with sh's stdin (the ssh channel) and exit status intact. The PATH
	// is widened inside with the places the installer and the package
	// managers put the binary; $HOME and $PATH expand in sh.
	remote := remoteTransport(remotePathPrefix + "exec " + command)
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("'ssh' not found; a sandbox on %s is reached over ssh", host)
	}
	var sshArgs []string
	if isTerminal(os.Stdin) && isTerminal(os.Stdout) {
		sshArgs = append(sshArgs, "-t")
	}
	sshArgs = append(sshArgs, "--", host, remote)
	fmt.Fprintf(os.Stderr, "Sandbox on %s: %s\n", host, command)
	c := exec.Command(sshBin, sshArgs...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return preserveExitCode(c.Run())
}

// resolvedPath is the absolute, symlink-free form of a "~"-prefixed or
// plain path. The sandbox mounts host paths at their own absolute paths and
// a symlink's target is what actually exists there, so every path that
// crosses into the sandbox is resolved first; the path must exist.
func resolvedPath(p string) (string, error) {
	abs, err := filepath.Abs(expandHome(p))
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// withinDir reports whether p is dir or below it (both resolved).
func withinDir(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// envHas reports whether env sets key to a non-empty value.
func envHas(env []string, key string) bool {
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); k == key && v != "" {
			return true
		}
	}
	return false
}

// setEnv replaces key in env, appending it when absent.
func setEnv(env []string, key, value string) []string {
	for i, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k == key {
			env[i] = key + "=" + value
			return env
		}
	}
	return append(env, key+"="+value)
}

// expandHome resolves a leading "~" against the home directory.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// sandboxEnv reduces a prepared launch environment to what the sandbox
// receives: the variables the effective block sets, and the authentication
// variables the launch decided (token, plan descriptors, CLAUDE_CONFIG_DIR).
// Everything else in the host environment stays on the host.
func sandboxEnv(prepared []string, block *manifest.Env) []string {
	want := map[string]bool{auth.OAuthTokenEnv: true, auth.SubscriptionTypeEnv: true, auth.RateLimitTierEnv: true, "CLAUDE_CONFIG_DIR": true}
	if block != nil {
		for k := range block.Set {
			want[k] = true
		}
	}
	var out []string
	for _, kv := range prepared {
		k, _, _ := strings.Cut(kv, "=")
		if want[k] {
			out = append(out, kv)
		}
	}
	return out
}

// baseURLHost is the host of ANTHROPIC_BASE_URL in env, "" when unset or
// unparsable; a sandboxed playbook routed elsewhere needs that host allowed.
func baseURLHost(env []string) string {
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if k != "ANTHROPIC_BASE_URL" {
			continue
		}
		u, err := url.Parse(v)
		if err != nil || u.Hostname() == "" {
			return ""
		}
		return u.Hostname()
	}
	return ""
}

// sandboxBackend is the seam every sandbox implementation fills: the six
// operations a launch needs. Only sbx exists today; the seam keeps the
// launch logic independent of its CLI.
type sandboxBackend interface {
	// names lists the existing sandboxes.
	names() ([]string, error)
	// mounts lists the host paths an existing sandbox was created with
	// (":ro" suffix kept), which it keeps for its lifetime.
	mounts(name string) ([]string, error)
	// create makes the sandbox with the given host paths mounted at their
	// own absolute paths (a ":ro" suffix marks a read-only mount);
	// shareSkills mounts the backend's shared skills store as well.
	create(name string, clone, shareSkills bool, mounts []string) error
	// secret registers value as a proxy-injected secret for requests from
	// the sandbox to host: inside, env holds placeholder; the proxy swaps
	// the real value into request headers on the way out. Registering the
	// same placeholder again updates the value.
	secret(name, host, env, value, placeholder string) error
	// allowNetwork widens the sandbox's egress policy by one host.
	allowNetwork(name, host string) error
	// shell runs a login-shell command inside, non-interactively.
	shell(name, command string) error
	// shellOutput runs a login-shell command inside and returns its stdout.
	shellOutput(name, command string) (string, error)
	// secrets lists the custom secrets registered for the sandbox, env
	// variable to placeholder.
	secrets(name string) (map[string]string, error)
	// attach runs a login-shell command inside with env set, wired to this
	// process's stdio; tty asks for a pty.
	attach(name string, env []string, tty bool, command string) error
	// remove deletes the sandbox and everything in it.
	remove(name string) error
	// homeDir is the sandbox user's home inside, a path that exists only
	// there; sandbox-local state (a login the host does not share) lives
	// below it.
	homeDir() string
	// hostAlias is the name by which the sandbox reaches the host machine
	// (inside, "localhost" is the sandbox itself).
	hostAlias() string
	// policyHost is the spelling the backend's policy and secret store use
	// for host: a service on the host machine is one resource there
	// whatever name the sandbox used for it.
	policyHost(host string) string
}

// newSandboxBackend returns the backend for kind, or an error naming what
// to install when its CLI is missing.
func newSandboxBackend(kind string) (sandboxBackend, error) {
	switch kind {
	case "sbx":
		bin, err := exec.LookPath("sbx")
		if err != nil {
			return nil, fmt.Errorf("'sbx' (Docker Sandboxes) not found; install it (macOS: brew trust docker/tap && brew install docker/tap/sbx) and run 'sbx login' once, or launch without --sandbox")
		}
		return sbxBackend{bin: bin}, nil
	}
	return nil, fmt.Errorf("unknown sandbox backend %q (available: %s)", kind, strings.Join(manifest.SandboxBackends, ", "))
}

// sbxBackend drives Docker Sandboxes through the sbx CLI (v0.38.0).
type sbxBackend struct {
	bin string
}

func (b sbxBackend) run(args ...string) error {
	c := exec.Command(b.bin, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func (b sbxBackend) names() ([]string, error) {
	c := exec.Command(b.bin, "ls", "-q")
	c.Stderr = os.Stderr
	out, err := c.Output()
	if err != nil {
		return nil, fmt.Errorf("sbx is not ready (run 'sbx login' if it reports not authenticated): %w", err)
	}
	var names []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if n := strings.TrimSpace(sc.Text()); n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}

func (b sbxBackend) mounts(name string) ([]string, error) {
	c := exec.Command(b.bin, "ls", "--json")
	c.Stderr = os.Stderr
	out, err := c.Output()
	if err != nil {
		return nil, fmt.Errorf("sbx ls --json: %w", err)
	}
	// sbx 0.38.0 wraps the list: {"sandboxes": [{name, workspaces, ...}]}
	// (a bare array is accepted too).
	type sandbox struct {
		Name       string   `json:"name"`
		Workspaces []string `json:"workspaces"`
	}
	var wrapped struct {
		Sandboxes []sandbox `json:"sandboxes"`
	}
	list := wrapped.Sandboxes
	if err := json.Unmarshal(out, &wrapped); err != nil {
		if err2 := json.Unmarshal(out, &list); err2 != nil {
			return nil, fmt.Errorf("sbx ls --json: %w", err)
		}
	} else {
		list = wrapped.Sandboxes
	}
	for _, s := range list {
		if s.Name == name {
			return s.Workspaces, nil
		}
	}
	return nil, fmt.Errorf("sandbox %s not listed", name)
}

func (b sbxBackend) create(name string, clone, shareSkills bool, mounts []string) error {
	args := []string{"create", "--name", name}
	if clone {
		args = append(args, "--clone")
	}
	if !shareSkills {
		// Accepted by sbx 0.38.0 though absent from its --help: without it
		// the shared skills store is mounted read-write into every sandbox.
		args = append(args, "--no-share-skills")
	}
	args = append(args, "claude")
	args = append(args, mounts...)
	return b.run(args...)
}

func (b sbxBackend) secret(name, host, env, value, placeholder string) error {
	// The value travels on sbx's argument list (sbx 0.38.0 has no stdin
	// form), visible to a local process listing for the moment of the
	// call; it already sits in a 0600 profile file. Output is discarded:
	// sbx echoes the masked value and the placeholder.
	c := exec.Command(b.bin, "secret", "set-custom", "--host", host, "--env", env, "--value", value, "--placeholder", placeholder, "--sandbox", name)
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (b sbxBackend) allowNetwork(name, host string) error {
	return b.run("policy", "allow", "network", "--sandbox", name, host)
}

func (b sbxBackend) shell(name, command string) error {
	return b.run("exec", name, "bash", "-lc", command)
}

func (b sbxBackend) shellOutput(name, command string) (string, error) {
	c := exec.Command(b.bin, "exec", name, "bash", "-lc", command)
	c.Stderr = os.Stderr
	out, err := c.Output()
	return string(out), err
}

// secrets parses `sbx secret ls --sandbox NAME` (no JSON form in 0.38.0):
// under the CUSTOM SECRETS table, rows are SCOPE TARGETS ENV PLACEHOLDER
// SECRET; only rows scoped to the sandbox count.
func (b sbxBackend) secrets(name string) (map[string]string, error) {
	c := exec.Command(b.bin, "secret", "ls", "--sandbox", name)
	c.Stderr = os.Stderr
	out, err := c.Output()
	if err != nil {
		return nil, fmt.Errorf("sbx secret ls: %w", err)
	}
	found := map[string]string{}
	custom := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "CUSTOM SECRETS"):
			custom = true
			continue
		case strings.HasSuffix(line, "SECRETS"):
			custom = false
			continue
		}
		if !custom {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 4 || f[0] != name || f[0] == "SCOPE" {
			continue
		}
		found[f[2]] = f[3]
	}
	return found, nil
}

func (b sbxBackend) attach(name string, env []string, tty bool, command string) error {
	// A pty is requested only when this process has a terminal on both
	// ends: sbx exec -t without one produces no output and exits 0, so a
	// piped or scripted launch (-p) would silently do nothing.
	args := []string{"exec", "-i"}
	if tty {
		args = append(args, "-t")
	}
	for _, kv := range env {
		args = append(args, "-e", kv)
	}
	args = append(args, name, "bash", "-lc", command)
	return b.run(args...)
}

func (b sbxBackend) remove(name string) error {
	return b.run("rm", "-f", name)
}

func (b sbxBackend) homeDir() string { return "/home/agent" }

func (b sbxBackend) hostAlias() string { return "host.docker.internal" }

// policyHost: the sbx proxy resolves host.docker.internal to the host and
// names it "localhost" in policy and secret matching (verified on 0.38.0:
// an allow rule or a custom secret for host.docker.internal never matches,
// one for localhost does).
func (b sbxBackend) policyHost(host string) string {
	switch host {
	case b.hostAlias(), "localhost", "127.0.0.1", "::1":
		return "localhost"
	}
	return host
}

// removeSandbox deletes a target's sandbox (start --delete): best effort,
// reported as a warning.
func removeSandbox(kind, name string) {
	b, err := newSandboxBackend(kind)
	if err == nil {
		err = b.remove(name)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not remove sandbox %s: %v\n", name, err)
	}
}

// isTerminal reports whether f is a terminal (/dev/null is a character
// device too, so a mode check is not enough).
func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// sandboxLoginPath is where a sandboxed launch keeps a login the host does
// not share: a file below the sandbox user's home, which exists only inside
// the sandbox and persists with it.
func sandboxLoginPath(backend sandboxBackend, name string) string {
	return backend.homeDir() + "/.claude-playbook-logins/" + name + "/" + auth.CredentialsFileName
}

// isSandboxLoginLink reports whether the store at configPath is a link to
// a sandbox-local login (any sandbox's), which a host launch never
// resolves.
func isSandboxLoginLink(configPath string, backend sandboxBackend) bool {
	target, err := os.Readlink(filepath.Join(configPath, auth.CredentialsFileName))
	return err == nil && strings.HasPrefix(target, backend.homeDir()+"/.claude-playbook-logins/")
}

// prepareSandboxEnv runs the host-side authentication decision for a
// sandboxed launch and returns the environment the sandbox receives, plus
// the sandbox-local path the store now points at ("" when the store is the
// target's own).
//
// A store that is still a symlink afterwards, with no token in play, is the
// shared machine login: its target (~/.claude) is not mounted and the
// backend mounts directories only, so inside the sandbox the link would
// dangle and Claude Code would be logged out. The link is re-pointed at a
// sandbox-local file instead: /login inside writes through it into the
// sandbox, where the grant persists with the sandbox and never touches the
// host. On the host the link dangles until the next unsandboxed launch,
// whose credential sync replaces any link that is not the shared one with
// the shared one and copies nothing (only a regular grant-bearing store is
// ever promoted to the machine store, which is why the login must not land
// as a regular file on the mount). The account state the host sync copied
// in is purged, as for an isolated playbook: inside, nothing authenticates
// as that account until /login.
func prepareSandboxEnv(t sandboxTarget, backend sandboxBackend, layers []*manifest.Env) (env []string, loginPath string, err error) {
	launchEnv, syncErr := auth.PrepareLaunchEnvWith(t.configPath, layers)
	if errors.Is(syncErr, envprofile.ErrProfile) {
		return nil, "", syncErr
	}
	if syncErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to prepare authentication state: %v\n", syncErr)
	}
	store := filepath.Join(t.configPath, auth.CredentialsFileName)
	if envHas(launchEnv, auth.OAuthTokenEnv) && isSandboxLoginLink(t.configPath, backend) {
		// Token launch after a sandbox login: on the host the link dangles,
		// so the quarantine above found no grant to detach, but inside the
		// sandbox it resolves to the earlier login, which Claude Code's
		// 401 recovery would adopt over the token. Detach it, as the
		// quarantine does for a shared link with a grant.
		if err := os.Remove(store); err != nil {
			return nil, "", fmt.Errorf("could not detach the sandbox login for a token launch: %w", err)
		}
	}
	// A missing store on a non-isolated target is the shared-login shape
	// too, with no machine login to link to yet: without the sandbox link,
	// /login inside would land as a regular grant on the mount, and a
	// machine login established later would let the host sync promote it.
	info, lerr := os.Lstat(store)
	linked := lerr == nil && info.Mode()&os.ModeSymlink != 0
	missing := os.IsNotExist(lerr) && !auth.IsAuthIsolated(t.configPath)
	if (linked || missing) && !envHas(launchEnv, auth.OAuthTokenEnv) {
		loginPath = sandboxLoginPath(backend, t.name)
		if target, err := os.Readlink(store); err != nil || target != loginPath {
			if linked {
				if err := os.Remove(store); err != nil {
					return nil, "", fmt.Errorf("could not detach the shared login for the sandbox: %w", err)
				}
			}
			if err := os.Symlink(loginPath, store); err != nil {
				return nil, "", fmt.Errorf("could not point the store at the sandbox login: %w", err)
			}
		}
		if _, qErr := auth.QuarantineAccountState(t.configPath); qErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v\n", qErr)
		}
		fmt.Fprintf(os.Stderr, "Shared login stays on the host: %s authenticates on its own inside the sandbox (run /login once there; the login lives in the sandbox)\n", t.label)
	}
	block, _ := auth.EffectiveBlock(t.configPath, layers)
	return sandboxEnv(launchEnv, block), loginPath, nil
}

// mountCovers reports whether p is the mount or below it, by filesystem
// identity: p's ancestors are compared with the mount through os.SameFile,
// so "/" (whose string prefix would never match), a differently cased
// spelling on a case-insensitive filesystem, and any other alias of the
// same directory all count.
func mountCovers(mount, p string) bool {
	mi, err := os.Stat(mount)
	if err != nil {
		return false
	}
	for cur := p; ; cur = filepath.Dir(cur) {
		if ci, err := os.Stat(cur); err == nil && os.SameFile(mi, ci) {
			return true
		}
		if filepath.Dir(cur) == cur {
			return false
		}
	}
}

// checkMountedManifests walks up from configPath and refuses when a
// manifest whose directory lies under one of the mounts sets a backend
// API key, or cannot be read (it may hold one).
func checkMountedManifests(configPath string, mounts []string) error {
	underMount := func(dir string) bool {
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return false
		}
		for _, m := range mounts {
			p, _ := strings.CutSuffix(m, ":ro")
			if mountCovers(p, resolved) {
				return true
			}
		}
		return false
	}
	for dir := configPath; ; dir = filepath.Dir(dir) {
		if underMount(dir) {
			m, err := manifest.Read(dir)
			if err != nil {
				// An unreadable manifest may hold a key: not a guard to skip.
				return fmt.Errorf("cannot check %s for keys the sandbox would mount: %w (fix the manifest, or set [sandbox] secrets = \"env\" to accept the exposure)", filepath.Join(dir, manifest.FileName), err)
			}
			if m != nil && m.Env != nil {
				for _, key := range secretEnvVars {
					if m.Env.Set[key] != "" {
						return fmt.Errorf("%s is set in %s, which the sandbox mounts: the key would be readable inside. Move it to an env profile, which lives outside the mount (cpb env-profile <profile> set %s=...; cpb env <playbook> use <profile>; cpb env <playbook> clear %s), or set [sandbox] secrets = \"env\" to accept the exposure", key, filepath.Join(dir, manifest.FileName), key, key)
					}
				}
			}
		}
		if filepath.Dir(dir) == dir {
			return nil
		}
	}
}

// checkReusedMounts refuses a reused sandbox whose mounts do not cover
// what this launch needs, or carry a machine or registry secret; the
// remedy is --sandbox-fresh.
func checkReusedMounts(name string, have, need []string) error {
	var havePaths []string
	for _, m := range have {
		p, _ := strings.CutSuffix(m, ":ro")
		havePaths = append(havePaths, p)
		if what := machineLoginInside(p); what != "" {
			return fmt.Errorf("sandbox %s mounts %s, which contains %s: the machine login would be inside. Recreate it with --sandbox-fresh", name, p, what)
		}
	}
	var missing []string
	for _, m := range need {
		p, _ := strings.CutSuffix(m, ":ro")
		covered := false
		for _, h := range havePaths {
			if h == p || mountCovers(h, p) {
				covered = true
				break
			}
		}
		if !covered {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("sandbox %s was created with mounts %s; this launch also needs %s, which a reused sandbox cannot add. Recreate it with --sandbox-fresh", name, strings.Join(have, " "), strings.Join(missing, " "))
	}
	return nil
}

// machineLoginInside reports which machine credential a mount would carry
// into the sandbox: the machine config directory (~/.claude), the machine
// credentials store at its resolved location (the store may be a symlink
// into another directory, a supported layout), or the long-lived token
// file, when any of them exists below the mount. "" when the mount is
// clean.
func machineLoginInside(mount string) string {
	if dir, err := auth.GlobalConfigDir(); err == nil {
		if _, err := os.Stat(dir); err == nil && mountCovers(mount, dir) {
			return "the machine's Claude config directory " + dir
		}
		if store, err := filepath.EvalSymlinks(filepath.Join(dir, auth.CredentialsFileName)); err == nil && mountCovers(mount, store) {
			return "the machine's credentials store " + store
		}
	}
	if tf := auth.OAuthTokenFile(); tf != "" {
		if resolved, err := filepath.EvalSymlinks(tf); err == nil && mountCovers(mount, resolved) {
			return "the machine's long-lived token file " + resolved
		}
	}
	// The registry's env profiles are its secret store (proxy injection
	// keeps their keys out of the sandbox only while the files stay out).
	if profiles, err := filepath.EvalSymlinks(envprofile.Dir(config.ResolvePlaybooksDir())); err == nil && mountCovers(mount, profiles) {
		return "the registry's env profiles " + profiles
	}
	return ""
}

// runSandboxed launches t inside its sandbox. claudeArgs are forwarded to
// claude verbatim. started reports whether a session was attached (as
// opposed to a refusal before anything ran), so a caller's post-session
// cleanup never follows a refusal.
func runSandboxed(t sandboxTarget, layers []*manifest.Env, claudeArgs []string, opts sandboxOpts) (started bool, err error) {
	backend, err := newSandboxBackend(t.backend)
	if err != nil {
		return false, err
	}
	// The machine config directory is never a sandbox root; checked before
	// any preparation touches it.
	if auth.IsGlobalConfigDir(t.configPath) {
		return false, fmt.Errorf("%s is the machine's Claude config directory: a sandbox would mount the machine login. Sandbox a playbook or another directory", t.configPath)
	}
	env, loginPath, err := prepareSandboxEnv(t, backend, layers)
	if err != nil {
		return false, err
	}
	env = rewriteHostEndpoint(env, backend)

	var sb manifest.Sandbox
	if t.manifest != nil {
		sb = *t.manifest
	}
	workdir := opts.workdir
	if workdir == "" {
		workdir = sb.Workdir
	}
	if workdir == "" {
		if workdir, err = os.Getwd(); err != nil {
			return false, err
		}
	}
	if info, err := os.Stat(expandHome(workdir)); err != nil || !info.IsDir() {
		return false, fmt.Errorf("sandbox working directory %s is not a directory", expandHome(workdir))
	}
	if workdir, err = resolvedPath(workdir); err != nil {
		return false, err
	}
	// Mount the root (the config directory, or the install root it sits
	// in) and address the config directory inside the sandbox by its
	// resolved path: a linked registry entry is a symlink the sandbox does
	// not have, its target is what gets mounted.
	rootDir, err := resolvedPath(t.rootPath)
	if err != nil {
		return false, err
	}
	configDir, err := resolvedPath(t.configPath)
	if err != nil {
		return false, err
	}
	env = setEnv(env, "CLAUDE_CONFIG_DIR", configDir)
	mounts := []string{workdir}
	if !withinDir(rootDir, workdir) {
		mounts = append(mounts, rootDir)
	}
	if !withinDir(configDir, rootDir) && !withinDir(configDir, workdir) {
		mounts = append(mounts, configDir)
	}
	var extras []string
	for _, m := range append(append([]string{}, sb.Mounts...), opts.mounts...) {
		p, ro := strings.CutSuffix(m, ":ro")
		if p, err = resolvedPath(p); err != nil {
			return false, fmt.Errorf("sandbox mount %s: %w", m, err)
		}
		if ro {
			p += ":ro"
		}
		extras = append(extras, p)
	}
	mounts = append(mounts, extras...)
	// No mount may carry the machine login in: the home directory, or any
	// directory above ~/.claude or the token file, is refused, read-only
	// or not.
	for _, m := range mounts {
		p, _ := strings.CutSuffix(m, ":ro")
		if what := machineLoginInside(p); what != "" {
			return false, fmt.Errorf("sandbox mount %s contains %s: the machine login would enter the sandbox. Mount a narrower directory", p, what)
		}
	}

	// A key set in a manifest the sandbox mounts: the placeholder in the
	// environment would hide nothing. The launch reads the nearest
	// manifest walking up from the config directory, so every manifest
	// on the way up is checked while its directory lies under one of this
	// launch's mounts (a subdir install keeps its [env] in the root's; a
	// start directory under the working directory inherits from above
	// it). Env profiles live in the registry root, outside every mount,
	// so that is where a secret belongs; refused before the sandbox
	// exists.
	if sb.Secrets != "env" {
		if err := checkMountedManifests(t.configPath, mounts); err != nil {
			return false, err
		}
	}

	name := t.name
	names, err := backend.names()
	if err != nil {
		return false, err
	}
	exists := false
	for _, n := range names {
		if n == name {
			exists = true
		}
	}
	if exists && opts.fresh {
		if err := backend.remove(name); err != nil {
			return false, fmt.Errorf("could not remove sandbox %s: %w", name, err)
		}
		exists = false
	}
	if exists {
		// Mounts are creation-time: a reused sandbox keeps the ones it was
		// created with. They must cover this launch (a different working
		// directory would not exist inside) and pass the same guard as new
		// mounts (a registry root mounted before its profiles existed).
		have, err := backend.mounts(name)
		if err != nil {
			return false, err
		}
		if err := checkReusedMounts(name, have, mounts); err != nil {
			return false, err
		}
		// The existing mounts may be wider than this launch's: a manifest
		// under them is on disk inside just the same.
		if sb.Secrets != "env" {
			if err := checkMountedManifests(t.configPath, have); err != nil {
				return false, err
			}
		}
	}
	if exists {
		// Creation-time choices this launch cannot see from outside: the
		// marker cpb wrote inside at creation says what they were. A
		// sandbox without one predates the marker (v3.12.0 and earlier,
		// created with sbx's shared skills store mounted) and must be
		// recreated; one whose choices differ from the manifest too.
		marker, err := backend.shellOutput(name, "cat ~/"+sandboxMarkerFile+" 2>/dev/null")
		want := sandboxMarker(sb.ShareSkills)
		if err != nil || strings.TrimSpace(marker) != want {
			return false, fmt.Errorf("sandbox %s was created with other creation-time settings (found %q, this launch needs %q): an earlier claude-playbook, or a changed share_skills. Recreate it with --sandbox-fresh", name, strings.TrimSpace(marker), want)
		}
	}
	if !exists {
		if err := backend.create(name, opts.clone, sb.ShareSkills, mounts); err != nil {
			return false, fmt.Errorf("could not create sandbox %s: %w", name, err)
		}
		if err := backend.shell(name, "printf %s "+shell.QuoteArg(sandboxMarker(sb.ShareSkills))+" > ~/"+sandboxMarkerFile); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not record the sandbox's creation settings inside %s: %v (the next reuse will ask for --sandbox-fresh)\n", name, err)
		}
		hosts := append([]string{}, sb.AllowNet...)
		if h := baseURLHost(env); h != "" {
			hosts = append(hosts, h)
		}
		for i, h := range hosts {
			hosts[i] = backend.policyHost(h)
		}
		for _, h := range hosts {
			if err := backend.allowNetwork(name, h); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not allow network %q for sandbox %s: %v\n", h, name, err)
			}
		}
		if sb.ClaudeVersion != "" {
			install := "set -o pipefail; curl -fsSL https://claude.ai/install.sh | bash -s " + shell.QuoteArg(sb.ClaudeVersion)
			if err := backend.shell(name, install); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not pin Claude Code %s inside sandbox %s: %v (the image's own version runs)\n", sb.ClaudeVersion, name, err)
			}
		}
		fmt.Fprintf(os.Stderr, "Sandbox %s created (%s): workdir %s, config %s%s\n", name, t.backend, workdir, rootDir, describeExtras(extras, hosts, sb.ClaudeVersion))
	} else {
		fmt.Fprintf(os.Stderr, "Sandbox %s reused (--sandbox-fresh recreates it): workdir %s\n", name, workdir)
	}

	env = injectSecrets(backend, name, env, sb.Secrets)

	// Attach: the environment and claude's arguments travel as exec
	// arguments, never through a file inside the sandbox.
	quoted := make([]string, 0, len(claudeArgs))
	for _, a := range claudeArgs {
		quoted = append(quoted, shell.QuoteArg(a))
	}
	command := "cd " + shell.QuoteArg(workdir) + " && exec claude"
	if loginPath != "" {
		// The sandbox-local login directory must exist for /login to write
		// through the store link; created as the sandbox user, inside.
		command = "mkdir -p " + shell.QuoteArg(filepath.Dir(loginPath)) + " && " + command
	}
	if len(quoted) > 0 {
		command += " " + strings.Join(quoted, " ")
	}
	tty := isTerminal(os.Stdin) && isTerminal(os.Stdout)
	runErr := backend.attach(name, env, tty, command)
	if loginPath != "" {
		// The session is over: give the host its shared link back, so the
		// store dangles only while a sandboxed session is live. The sync
		// replaces a link that is not the shared one and copies nothing;
		// the sandbox login stays inside the sandbox for the next launch.
		// A launch that never returns here (crash, kill) leaves the link
		// dangling, which every host command tolerates and the next host
		// launch repairs.
		if err := auth.SyncCredentials(t.configPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not restore the shared login link after the sandbox session: %v\n", err)
		}
	}
	return true, preserveExitCode(runErr)
}

// rewriteHostEndpoint points an ANTHROPIC_BASE_URL at localhost (the
// pilot's spelling for a service on this machine, a router say) at the
// backend's host alias: inside the sandbox, localhost is the sandbox. The
// scheme, port and path are kept. Said on stderr, since the sandbox then
// talks to a different name than the profile wrote.
func rewriteHostEndpoint(env []string, backend sandboxBackend) []string {
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if k != "ANTHROPIC_BASE_URL" {
			continue
		}
		u, err := url.Parse(v)
		if err != nil || u.Hostname() == "" {
			return env
		}
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
			host := backend.hostAlias()
			if p := u.Port(); p != "" {
				host += ":" + p
			}
			u.Host = host
			fmt.Fprintf(os.Stderr, "ANTHROPIC_BASE_URL names this machine: inside the sandbox it is %s\n", u.String())
			return setEnv(env, k, u.String())
		}
		return env
	}
	return env
}

// sandboxMarkerFile, below the sandbox user's home, records the
// creation-time settings a launch cannot read back from the backend.
const sandboxMarkerFile = ".claude-playbook-sandbox"

func sandboxMarker(shareSkills bool) string {
	if shareSkills {
		return "skills=shared"
	}
	return "skills=private"
}

// secretEnvVars are the backend API keys Claude Code sends as request
// headers to its API endpoint: injectable at the proxy. The subscription
// token is not among them: Claude Code checks its shape locally, and a
// placeholder would not pass.
var secretEnvVars = []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"}

// secretPlaceholder is the value the sandbox sees for env: stable per
// sandbox and variable, so re-registering it updates the secret in place
// (rotation) and nothing has to be parsed from the backend.
func secretPlaceholder(sandbox, env string) string {
	return sandbox + "-" + env
}

// injectSecrets registers each API key the environment carries as a
// proxy-injected secret for the endpoint host and replaces its value with
// the placeholder, so the key never enters the sandbox. mode "env" passes
// the values as they are. A registration that fails falls back to the
// plain value with a warning naming the variable: a silent fallback would
// leave the pilot believing the key stayed outside.
func injectSecrets(backend sandboxBackend, sandbox string, env []string, mode string) []string {
	if mode == "env" {
		return env
	}
	host := baseURLHost(env)
	if host == "" {
		host = "api.anthropic.com"
	}
	host = backend.policyHost(host)
	// Mappings registered by an earlier launch for a key the environment
	// no longer carries would still inject that key for anyone inside who
	// sends the (predictable) placeholder: sbx cannot delete them, so they
	// are neutralized by re-registering the placeholder as its own value.
	registered, err := backend.secrets(sandbox)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not list the sandbox's secrets (%v); a key registered by an earlier launch may still be mapped\n", err)
	}
	for _, key := range secretEnvVars {
		value := ""
		for _, kv := range env {
			if k, v, _ := strings.Cut(kv, "="); k == key {
				value = v
			}
		}
		if value == "" {
			placeholder := secretPlaceholder(sandbox, key)
			if registered[key] == placeholder {
				if err := backend.secret(sandbox, host, key, placeholder, placeholder); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: %s is no longer in the environment but its proxy mapping could not be revoked (%v)\n", key, err)
				} else {
					fmt.Fprintf(os.Stderr, "Secret %s revoked at the proxy: it is no longer in the environment\n", key)
				}
			}
			continue
		}
		placeholder := secretPlaceholder(sandbox, key)
		if err := backend.secret(sandbox, host, key, value, placeholder); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: %s could not be injected at the proxy (%v); it enters the sandbox as a plain value. [sandbox] secrets = \"env\" silences this\n", key, err)
			continue
		}
		env = setEnv(env, key, placeholder)
		fmt.Fprintf(os.Stderr, "Secret %s stays on the host: injected at the proxy for %s, the sandbox sees a placeholder\n", key, host)
	}
	return env
}

func describeExtras(extraMounts, hosts []string, version string) string {
	var parts []string
	if len(extraMounts) > 0 {
		parts = append(parts, "mounts "+strings.Join(extraMounts, " "))
	}
	if len(hosts) > 0 {
		parts = append(parts, "network +"+strings.Join(hosts, ","))
	}
	if version != "" {
		parts = append(parts, "claude "+version)
	}
	if len(parts) == 0 {
		return ""
	}
	return ", " + strings.Join(parts, ", ")
}

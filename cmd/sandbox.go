package cmd

import (
	"bufio"
	"bytes"
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
	mounts   []string
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
		if flag != "--workdir" && flag != "--mount" {
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
		case "--mount":
			opts.mounts = append(opts.mounts, value)
		}
		consumed = true
	}
	return args[i:], consumed, nil
}

// takeRunFlags scans one leading run of launch flags and sandbox flags in
// any order. extra adds command-specific boolean flags (start's --delete).
func takeRunFlags(args []string, opts *sandboxOpts, extra map[string]*bool) (rest []string, layers []*manifest.Env, err error) {
	bools := map[string]*bool{
		"--sandbox": &opts.enabled, "--sbx": &opts.enabled, "--no-sandbox": &opts.disabled,
		"--sandbox-fresh": &opts.fresh, "--clone": &opts.clone,
	}
	for k, v := range extra {
		bools[k] = v
	}
	rest = args
	for {
		var more []*manifest.Env
		rest, more, err = takeLaunchFlagsWith(rest, bools)
		if err != nil {
			return nil, nil, err
		}
		layers = append(layers, more...)
		var consumed bool
		rest, consumed, err = takeSandboxValueFlags(rest, opts)
		if err != nil {
			return nil, nil, err
		}
		if !consumed {
			return rest, layers, nil
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
	// create makes the sandbox with the given host paths mounted at their
	// own absolute paths (a ":ro" suffix marks a read-only mount).
	create(name string, clone bool, mounts []string) error
	// allowNetwork widens the sandbox's egress policy by one host.
	allowNetwork(name, host string) error
	// shell runs a login-shell command inside, non-interactively.
	shell(name, command string) error
	// attach runs a login-shell command inside with env set, wired to this
	// process's stdio; tty asks for a pty.
	attach(name string, env []string, tty bool, command string) error
	// remove deletes the sandbox and everything in it.
	remove(name string) error
	// homeDir is the sandbox user's home inside, a path that exists only
	// there; sandbox-local state (a login the host does not share) lives
	// below it.
	homeDir() string
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

func (b sbxBackend) create(name string, clone bool, mounts []string) error {
	args := []string{"create", "--name", name}
	if clone {
		args = append(args, "--clone")
	}
	args = append(args, "claude")
	args = append(args, mounts...)
	return b.run(args...)
}

func (b sbxBackend) allowNetwork(name, host string) error {
	return b.run("policy", "allow", "network", "--sandbox", name, host)
}

func (b sbxBackend) shell(name, command string) error {
	return b.run("exec", name, "bash", "-lc", command)
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
	if !exists {
		if err := backend.create(name, opts.clone, mounts); err != nil {
			return false, fmt.Errorf("could not create sandbox %s: %w", name, err)
		}
		hosts := append([]string{}, sb.AllowNet...)
		if h := baseURLHost(env); h != "" {
			hosts = append(hosts, h)
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

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

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

// Sandboxed launch: `run --sandbox <name>` runs the playbook's Claude Code
// inside a Docker Sandbox (the `sbx` CLI: a microVM with its own kernel,
// filesystem and network stack). Only what is mounted crosses the boundary:
// the playbook's own directory and the working directory, plus whatever the
// manifest's [sandbox] block or --mount adds. The environment is the same
// stack every launch gets (registry default, profiles, the block, one-off
// flags), reduced to what those layers SET plus the authentication
// variables, so the host's environment does not leak in. Network egress is
// the sandbox policy's, widened per sandbox by [sandbox].allow_net and the
// host of ANTHROPIC_BASE_URL when the env points elsewhere.
//
// One sandbox per playbook, named cpb-<playbook>, reused across launches so
// installed tools and the agent's state persist; --sandbox-fresh recreates
// it. The sandbox runs the image's own Claude Code unless
// [sandbox].claude_version pins one, installed once at creation.

// sandboxOpts are the --sandbox family of run flags.
type sandboxOpts struct {
	enabled bool
	fresh   bool
	clone   bool
	workdir string
	mounts  []string
}

var sandboxNameClean = regexp.MustCompile(`[^A-Za-z0-9.+-]+`)

// sandboxName is the sbx sandbox name for a playbook: the characters sbx
// accepts (letters, digits, hyphens, periods, plus and minus), anything
// else folded to a hyphen.
func sandboxName(playbookName string) string {
	return "cpb-" + strings.Trim(sandboxNameClean.ReplaceAllString(playbookName, "-"), "-")
}

// takeSandboxValueFlags consumes the leading --workdir PATH and --mount
// PATH[:ro] flags (also in =form). It returns the rest and whether anything
// was consumed, so the caller can alternate with the launch-flag scanner
// until neither makes progress.
func takeSandboxValueFlags(args []string, opts *sandboxOpts) (rest []string, consumed bool, err error) {
	i := 0
	for i < len(args) && args[i] != "--" {
		flag, value, inline := strings.Cut(args[i], "=")
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
// any order.
func takeRunFlags(args []string, opts *sandboxOpts) (rest []string, layers []*manifest.Env, err error) {
	bools := map[string]*bool{"--sandbox": &opts.enabled, "--sandbox-fresh": &opts.fresh, "--clone": &opts.clone}
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

// sbxRunner runs sbx commands; tests substitute the binary through PATH.
type sbxRunner struct {
	bin string
}

func (r sbxRunner) output(args ...string) ([]byte, error) {
	c := exec.Command(r.bin, args...)
	c.Stderr = os.Stderr
	return c.Output()
}

func (r sbxRunner) run(args ...string) error {
	c := exec.Command(r.bin, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// runSandboxed launches pb inside its sandbox. claudeArgs are forwarded to
// claude verbatim.
func runSandboxed(pb *playbook.Playbook, layers []*manifest.Env, claudeArgs []string, opts sandboxOpts) error {
	bin, err := exec.LookPath("sbx")
	if err != nil {
		return fmt.Errorf("'sbx' (Docker Sandboxes) not found; install it (macOS: brew trust docker/tap && brew install docker/tap/sbx) and run 'sbx login' once, or launch without --sandbox")
	}
	sbx := sbxRunner{bin: bin}

	launchEnv, syncErr := auth.PrepareLaunchEnvWith(pb.Path, layers)
	if errors.Is(syncErr, envprofile.ErrProfile) {
		return syncErr
	}
	if syncErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to prepare authentication state: %v\n", syncErr)
	}
	block, _ := auth.EffectiveBlock(pb.Path, layers)
	env := sandboxEnv(launchEnv, block)

	var sb manifest.Sandbox
	if pb.Manifest != nil && pb.Manifest.Sandbox != nil {
		sb = *pb.Manifest.Sandbox
	}
	workdir := opts.workdir
	if workdir == "" {
		workdir = sb.Workdir
	}
	if workdir == "" {
		if workdir, err = os.Getwd(); err != nil {
			return err
		}
	}
	workdir = expandHome(workdir)
	if workdir, err = filepath.Abs(workdir); err != nil {
		return err
	}
	if info, err := os.Stat(workdir); err != nil || !info.IsDir() {
		return fmt.Errorf("sandbox working directory %s is not a directory", workdir)
	}
	configDir := pb.RootPath
	if configDir == "" {
		configDir = pb.Path
	}
	if configDir, err = filepath.Abs(configDir); err != nil {
		return err
	}
	mounts := []string{workdir}
	if !samePath(configDir, workdir) {
		mounts = append(mounts, configDir)
	}
	var extras []string
	for _, m := range append(append([]string{}, sb.Mounts...), opts.mounts...) {
		p, ro := strings.CutSuffix(m, ":ro")
		p = expandHome(p)
		if p, err = filepath.Abs(p); err != nil {
			return err
		}
		if ro {
			p += ":ro"
		}
		extras = append(extras, p)
	}
	mounts = append(mounts, extras...)

	name := sandboxName(pb.Name)
	names, err := sbx.output("ls", "-q")
	if err != nil {
		return fmt.Errorf("sbx is not ready (run 'sbx login' if it reports not authenticated): %w", err)
	}
	exists := false
	sc := bufio.NewScanner(bytes.NewReader(names))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == name {
			exists = true
		}
	}
	if exists && opts.fresh {
		if err := sbx.run("rm", "-f", name); err != nil {
			return fmt.Errorf("could not remove sandbox %s: %w", name, err)
		}
		exists = false
	}
	if !exists {
		createArgs := []string{"create", "--name", name}
		if opts.clone {
			createArgs = append(createArgs, "--clone")
		}
		createArgs = append(createArgs, "claude")
		createArgs = append(createArgs, mounts...)
		if err := sbx.run(createArgs...); err != nil {
			return fmt.Errorf("could not create sandbox %s: %w", name, err)
		}
		hosts := append([]string{}, sb.AllowNet...)
		if h := baseURLHost(env); h != "" {
			hosts = append(hosts, h)
		}
		for _, h := range hosts {
			if err := sbx.run("policy", "allow", "network", "--sandbox", name, h); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not allow network %q for sandbox %s: %v\n", h, name, err)
			}
		}
		if sb.ClaudeVersion != "" {
			install := "curl -fsSL https://claude.ai/install.sh | bash -s " + shell.QuoteArg(sb.ClaudeVersion)
			if err := sbx.run("exec", name, "bash", "-lc", install); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not pin Claude Code %s inside sandbox %s: %v (the image's own version runs)\n", sb.ClaudeVersion, name, err)
			}
		}
		fmt.Fprintf(os.Stderr, "Sandbox %s created: workdir %s, playbook %s%s\n", name, workdir, configDir, describeExtras(extras, hosts, sb.ClaudeVersion))
	} else {
		fmt.Fprintf(os.Stderr, "Sandbox %s reused (--sandbox-fresh recreates it): workdir %s\n", name, workdir)
	}

	// Attach: the playbook's environment and claude's arguments travel as
	// exec arguments, never through a file inside the sandbox.
	execArgs := []string{"exec", "-it"}
	for _, kv := range env {
		execArgs = append(execArgs, "-e", kv)
	}
	quoted := make([]string, 0, len(claudeArgs))
	for _, a := range claudeArgs {
		quoted = append(quoted, shell.QuoteArg(a))
	}
	command := "cd " + shell.QuoteArg(workdir) + " && exec claude"
	if len(quoted) > 0 {
		command += " " + strings.Join(quoted, " ")
	}
	execArgs = append(execArgs, name, "bash", "-lc", command)
	return preserveExitCode(sbx.run(execArgs...))
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

package cmd

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// The OpenShell backend (experimental): NVIDIA OpenShell 0.1.x driven
// through its CLI, on Linux with Docker Engine as the gateway's compute
// driver. Differences from sbx that shape it: the sandbox reaches only the
// paths its filesystem policy lists (fixed at creation) and no host by
// default; OpenShell injects its own credential placeholder and can delete
// a mapping; an idle sandbox costs about a third of a CPU core, so a launch
// stops the sandbox when its last session ends.

// openshellClaudeVersion is the Claude Code the image carries when
// [sandbox] claude_version is unset. Bump it only after the testbed e2e
// passes on the new version (AGENTS.md).
const openshellClaudeVersion = "2.1.285"

// openshellClaudePath is the real path of the claude binary in the image
// (npm's global install of the native build), which policy rules name.
const openshellClaudePath = "/usr/local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe"

//go:embed openshell/Dockerfile
var openshellDockerfile string

// Seams for tests: the platform, the uid, and how long a cold gateway may
// take to come up.
var (
	sandboxGOOS          = runtime.GOOS
	sandboxUID           = os.Getuid
	sandboxGID           = os.Getgid
	openshellGatewayWait = 45 * time.Second
	openshellRetryDelay  = 2 * time.Second
)

// openshellBaselineRO and openshellBaselineRW are the system paths every
// sandbox policy lists; mounts come on top. OpenShell adds them itself only
// when a network rule exists, which this backend does not rely on.
var (
	openshellBaselineRO = []string{"/bin", "/usr", "/lib", "/etc", "/proc", "/dev/urandom", "/var/log"}
	openshellBaselineRW = []string{"/tmp", "/dev/null"}
)

// openshellClaudeHosts are the hosts Claude Code reaches on its own, for
// the claude binary only: the API, the startup check (platform.claude.com;
// without it Claude Code 2.1.285 exits at start), telemetry, and the hosts
// /login needs. Listed at creation for every sandbox: network rules are
// only added when a sandbox is created, and a later launch may need the
// login.
var openshellClaudeHosts = []string{"api.anthropic.com", "platform.claude.com", "statsig.anthropic.com", "claude.ai", "console.anthropic.com"}

// sandboxLifecycle is implemented by a backend that needs the launch's
// settings before it creates or reuses a sandbox, starts a stopped one,
// carries Claude Code in its image (so the in-sandbox pin is skipped), and
// acts when the session ends. sbx does not implement it.
type sandboxLifecycle interface {
	prepare(sb manifest.Sandbox, clone bool, env []string) error
	reuse(name string) error
	imageCarriesClaude() bool
	finish(name string)
}

// openshellBackend drives NVIDIA OpenShell through its CLI (v0.1.2).
type openshellBackend struct {
	bin           string
	claudeVersion string
	pinned        bool
	baseHost      string
	basePort      int
}

// openshellPreflight checks what every openshell launch needs, before any
// state changes, and returns the CLI's path.
func openshellPreflight() (string, error) {
	if sandboxGOOS != "linux" {
		return "", errors.New("the openshell sandbox backend runs on Linux with Docker Engine; here, use --sandbox=sbx (or --sandbox-host to a Linux host)")
	}
	bin, err := exec.LookPath("openshell")
	if err != nil {
		return "", errors.New("'openshell' (NVIDIA OpenShell 0.1.x) not found; install it on this Linux host (docs/guides/sandbox.md, \"OpenShell backend\"), or use --sandbox=sbx")
	}
	out, _ := exec.Command(bin, "--version").Output()
	if v := strings.TrimSpace(string(out)); !openshellVersionOK(v) {
		if v == "" {
			v = "(no version reported)"
		}
		return "", fmt.Errorf("OpenShell %s is not supported by this claude-playbook (it needs 0.1.2 up to 0.1.x)", strings.TrimPrefix(v, "openshell "))
	}
	dv, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Output()
	major, _ := strconv.Atoi(strings.SplitN(strings.TrimSpace(string(dv)), ".", 2)[0])
	if err != nil || major < 28 {
		found := strings.TrimSpace(string(dv))
		if err != nil || found == "" {
			found = "no Docker Engine answered"
		}
		return "", fmt.Errorf("the openshell backend needs Docker Engine 28 or newer as the gateway's compute driver (found: %s)", found)
	}
	if sandboxUID() == 0 {
		return "", errors.New("the openshell backend does not run as root (OpenShell refuses a root workload)")
	}
	if err := openshellGatewayReady(bin); err != nil {
		return "", err
	}
	return bin, nil
}

// openshellVersionOK accepts 0.1.2 up to any later 0.1.x.
func openshellVersionOK(v string) bool {
	f := strings.Fields(v)
	if len(f) == 0 {
		return false
	}
	parts := strings.SplitN(strings.TrimPrefix(f[len(f)-1], "v"), ".", 3)
	if len(parts) != 3 || parts[0] != "0" || parts[1] != "1" {
		return false
	}
	patch := parts[2]
	if i := strings.IndexFunc(patch, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		patch = patch[:i]
	}
	n, err := strconv.Atoi(patch)
	return err == nil && n >= 2
}

// openshellGatewayReady waits for a gateway whose service is up but not
// listening yet (a cold start after login takes 20 to 30 s), and refuses
// one that is not running.
func openshellGatewayReady(bin string) error {
	connected := func() bool {
		out, _ := exec.Command(bin, "status").CombinedOutput()
		return strings.Contains(string(out), "Connected")
	}
	if connected() {
		return nil
	}
	state, _ := exec.Command("systemctl", "--user", "is-active", "openshell-gateway").Output()
	switch strings.TrimSpace(string(state)) {
	case "active", "activating":
		fmt.Fprintln(os.Stderr, "Waiting for the OpenShell gateway to come up ...")
		deadline := time.Now().Add(openshellGatewayWait)
		for time.Now().Before(deadline) {
			time.Sleep(openshellRetryDelay)
			if connected() {
				return nil
			}
		}
	}
	return errors.New("the OpenShell gateway is not running: systemctl --user start openshell-gateway")
}

// run runs the CLI with stdin closed (openshell reads a piped stdin to EOF
// even when it does not need it), and retries the conflict OpenShell
// reports while a sandbox is still being modified.
func (b *openshellBackend) run(env []string, args ...string) (string, error) {
	var out []byte
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		c := exec.Command(b.bin, args...)
		if env != nil {
			c.Env = env
		}
		out, err = c.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "modified by another operation") {
			break
		}
		time.Sleep(openshellRetryDelay)
	}
	if err != nil {
		return string(out), fmt.Errorf("openshell %s: %w: %s", args[0]+" "+args[1], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (b *openshellBackend) prepare(sb manifest.Sandbox, clone bool, env []string) error {
	if clone {
		return errors.New("--clone is sbx-only for now: the openshell backend mounts the working directory itself")
	}
	if sb.ShareSkills {
		return errors.New("share_skills applies to sbx; OpenShell has no shared skills store")
	}
	b.claudeVersion, b.pinned = sb.ClaudeVersion, sb.ClaudeVersion != ""
	if !b.pinned {
		b.claudeVersion = openshellClaudeVersion
	}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if k != "ANTHROPIC_BASE_URL" {
			continue
		}
		if u, err := url.Parse(v); err == nil && u.Hostname() != "" {
			b.baseHost = b.policyHost(u.Hostname())
			b.basePort, _ = strconv.Atoi(u.Port())
			if b.basePort == 0 {
				b.basePort = 443
				if u.Scheme == "http" {
					b.basePort = 80
				}
			}
		}
	}
	who := os.Getenv("USER")
	if u, err := user.Current(); err == nil {
		who = u.Username
	}
	if who == "" {
		who = strconv.Itoa(sandboxUID())
	}
	if out, err := exec.Command("loginctl", "show-user", who, "-p", "Linger", "--value").Output(); err == nil && strings.TrimSpace(string(out)) == "no" {
		fmt.Fprintf(os.Stderr, "Warning: linger is off for %s: the OpenShell gateway stops when your last session ends (sudo loginctl enable-linger %s)\n", who, who)
	}
	return nil
}

func (b *openshellBackend) imageCarriesClaude() bool { return true }

// imageLabel names the image a sandbox runs: the Claude Code version and
// the recipe's hash (a changed recipe is a different image).
func (b *openshellBackend) imageLabel() string {
	sum := sha256.Sum256([]byte(openshellDockerfile))
	return b.claudeVersion + "-" + hex.EncodeToString(sum[:])[:8]
}

// ensureImage builds the image on first use; later launches find it.
func (b *openshellBackend) ensureImage() (string, error) {
	tag := "cpb-openshell/claude:" + b.imageLabel()
	if exec.Command("docker", "image", "inspect", tag).Run() == nil {
		return tag, nil
	}
	fmt.Fprintf(os.Stderr, "Building the OpenShell image for Claude Code %s (once per version) ...\n", b.claudeVersion)
	c := exec.Command("docker", "build", "-t", tag, "--build-arg", "CLAUDE_CODE_VERSION="+b.claudeVersion, "-")
	c.Stdin = strings.NewReader(openshellDockerfile)
	c.Stdout = os.Stderr
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("could not build the OpenShell image %s: %w", tag, err)
	}
	return tag, nil
}

func (b *openshellBackend) names() ([]string, error) {
	out, err := b.run(nil, "sandbox", "list", "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Sandboxes []struct {
			Name string `json:"name"`
		} `json:"sandboxes"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("openshell sandbox list: %w", err)
	}
	var names []string
	for _, s := range list.Sandboxes {
		names = append(names, s.Name)
	}
	return names, nil
}

// openshellSandbox is the part of `openshell sandbox get -o json` the
// backend reads.
type openshellSandbox struct {
	Phase  string            `json:"phase"`
	Labels map[string]string `json:"labels"`
	Policy struct {
		Filesystem struct {
			ReadOnly  []string `json:"read_only"`
			ReadWrite []string `json:"read_write"`
		} `json:"filesystem_policy"`
	} `json:"policy"`
}

func (b *openshellBackend) get(name string) (openshellSandbox, error) {
	var s openshellSandbox
	out, err := b.run(nil, "sandbox", "get", name, "-o", "json")
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		return s, fmt.Errorf("openshell sandbox get %s: %w", name, err)
	}
	return s, nil
}

// mounts reads the mounted paths back from the sandbox's filesystem
// policy: OpenShell does not report the mounts themselves, and the policy
// lists exactly the paths cpb mounted, on top of the baseline.
func (b *openshellBackend) mounts(name string) ([]string, error) {
	s, err := b.get(name)
	if err != nil {
		return nil, err
	}
	base := map[string]bool{}
	for _, p := range append(append([]string{}, openshellBaselineRO...), openshellBaselineRW...) {
		base[p] = true
	}
	var mounts []string
	for _, p := range s.Policy.Filesystem.ReadWrite {
		if !base[p] {
			mounts = append(mounts, p)
		}
	}
	for _, p := range s.Policy.Filesystem.ReadOnly {
		if !base[p] {
			mounts = append(mounts, p+":ro")
		}
	}
	return mounts, nil
}

// openshellPolicy is the sandbox policy a create writes: the baseline
// paths, the mounts (":ro" read-only), the host user as the workload's
// identity (files on the mounts stay theirs), Landlock required, and the
// hosts Claude Code reaches, for the claude binary only.
func openshellPolicy(mounts []string, uid, gid int) string {
	ro := append([]string{}, openshellBaselineRO...)
	rw := append([]string{}, openshellBaselineRW...)
	for _, m := range mounts {
		if p, isRO := strings.CutSuffix(m, ":ro"); isRO {
			ro = append(ro, p)
		} else {
			rw = append(rw, m)
		}
	}
	var b strings.Builder
	b.WriteString("version: 1\nfilesystem_policy:\n  include_workdir: true\n  read_only:\n")
	for _, p := range ro {
		b.WriteString("    - " + yamlString(p) + "\n")
	}
	b.WriteString("  read_write:\n")
	for _, p := range rw {
		b.WriteString("    - " + yamlString(p) + "\n")
	}
	b.WriteString("landlock:\n  compatibility: hard_requirement\n")
	fmt.Fprintf(&b, "process:\n  run_as_user: \"%d\"\n  run_as_group: \"%d\"\n", uid, gid)
	b.WriteString("network_policies:\n  cpb_claude:\n    endpoints:\n")
	for _, h := range openshellClaudeHosts {
		b.WriteString("      - {host: " + h + ", port: 443, protocol: rest, access: read-write, enforcement: enforce}\n")
	}
	b.WriteString("    binaries:\n      - path: " + openshellClaudePath + "\n")
	return b.String()
}

// openshellMounts is the driver config that bind-mounts each path at its
// own absolute path.
func openshellMounts(mounts []string) string {
	type mount struct {
		Type     string `json:"type"`
		Source   string `json:"source"`
		Target   string `json:"target"`
		ReadOnly bool   `json:"read_only"`
	}
	list := []mount{}
	for _, m := range mounts {
		p, ro := strings.CutSuffix(m, ":ro")
		list = append(list, mount{"bind", p, p, ro})
	}
	data, _ := json.Marshal(map[string]map[string][]mount{"docker": {"mounts": list}})
	return string(data)
}

// yamlString quotes s for YAML: a JSON string is a valid YAML scalar.
func yamlString(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

// writeTemp writes content to a private temporary file and returns its
// path; the caller removes it.
func writeTemp(pattern, content string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	if _, err := f.WriteString(content); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func (b *openshellBackend) create(name string, clone, shareSkills bool, mounts []string) error {
	tag, err := b.ensureImage()
	if err != nil {
		return err
	}
	policy, err := writeTemp("cpb-openshell-policy-*.yaml", openshellPolicy(mounts, sandboxUID(), sandboxGID()))
	if err != nil {
		return err
	}
	defer os.Remove(policy)
	out, err := b.run(nil, "sandbox", "create", "--name", name, "--from", tag, "--detach", "--policy", policy,
		"--driver-config-json", openshellMounts(mounts), "--label", "cpb-image="+b.imageLabel())
	if err != nil && strings.Contains(strings.ToLower(out), "bind") {
		return fmt.Errorf("%w\nthe OpenShell gateway must allow host mounts: in ~/.config/openshell/gateway.toml set [openshell.drivers.docker] allow_driver_config = true and enable_bind_mounts = true, and [openshell.drivers.docker.resource_admission] enabled = false, then restart the gateway (docs/guides/sandbox.md, \"OpenShell backend\")", err)
	}
	return err
}

// reuse starts a sandbox the last session stopped, and compares the image
// it runs with the one this launch wants: an explicit claude_version that
// differs refuses; a changed default only says so.
func (b *openshellBackend) reuse(name string) error {
	s, err := b.get(name)
	if err != nil {
		return err
	}
	if have, want := s.Labels["cpb-image"], b.imageLabel(); have != want {
		if b.pinned {
			return fmt.Errorf("sandbox %s runs image %q; this launch pins Claude Code %s (%s). Recreate it with --sandbox-fresh", name, have, b.claudeVersion, want)
		}
		fmt.Fprintf(os.Stderr, "Sandbox %s runs image %q; this claude-playbook's default is %s: --sandbox-fresh moves it\n", name, have, want)
	}
	if s.Phase == "Stopped" {
		fmt.Fprintf(os.Stderr, "Starting sandbox %s (stopped after its last session) ...\n", name)
		if _, err := b.run(nil, "sandbox", "start", name); err != nil {
			return fmt.Errorf("could not start sandbox %s (%v); --sandbox-fresh recreates it", name, err)
		}
	}
	return nil
}

// finish stops the sandbox when no Claude Code session is left in it: an
// idle sandbox costs about a third of a CPU core.
func (b *openshellBackend) finish(name string) {
	out, err := b.shellOutput(name, "pgrep -x claude >/dev/null && echo busy || echo idle")
	if err != nil || strings.TrimSpace(out) != "idle" {
		return
	}
	if _, err := b.run(nil, "sandbox", "stop", name); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not stop sandbox %s: %v (an idle OpenShell sandbox costs about a third of a CPU core)\n", name, err)
		return
	}
	fmt.Fprintf(os.Stderr, "Sandbox %s stopped (an idle OpenShell sandbox costs about a third of a CPU core); the next launch starts it\n", name)
}

// openshellSecretID names the profile and provider cpb keeps for one key
// of one sandbox; the sandbox prefix is how cpb finds its own.
func openshellSecretID(sandbox, env string) string {
	return sandbox + "-" + strings.ToLower(strings.ReplaceAll(env, "_", "-"))
}

// openshellProfile binds env to one endpoint, for the claude binary.
func openshellProfile(id, sandbox, env, host string, port int) string {
	style, header := "header", "x-api-key"
	if env == "ANTHROPIC_AUTH_TOKEN" {
		style, header = "bearer", "authorization"
	}
	return "id: " + id + "\n" +
		"display_name: " + yamlString("cpb "+sandbox+" "+env) + "\n" +
		"description: " + yamlString("claude-playbook: "+env+" for sandbox "+sandbox) + "\n" +
		"category: inference\n" +
		"credentials:\n" +
		"  - name: key\n" +
		"    description: " + yamlString(env) + "\n" +
		"    env_vars: [" + env + "]\n" +
		"    required: true\n" +
		"    auth_style: " + style + "\n" +
		"    header_name: " + header + "\n" +
		"endpoints:\n" +
		"  - {host: " + yamlString(host) + ", port: " + strconv.Itoa(port) + ", protocol: rest, access: read-write, enforcement: enforce}\n" +
		"binaries: [" + openshellClaudePath + "]\n"
}

// profileEndpoint is the first endpoint of an exported profile ("" when
// there is none).
func profileEndpoint(out string) (string, int) {
	var v any
	if json.Unmarshal([]byte(out), &v) != nil {
		return "", 0
	}
	var walk func(any) (string, int, bool)
	walk = func(x any) (string, int, bool) {
		switch t := x.(type) {
		case map[string]any:
			if eps, ok := t["endpoints"].([]any); ok && len(eps) > 0 {
				if ep, ok := eps[0].(map[string]any); ok {
					h, _ := ep["host"].(string)
					p, _ := ep["port"].(float64)
					return h, int(p), true
				}
			}
			for _, c := range t {
				if h, p, ok := walk(c); ok {
					return h, p, true
				}
			}
		case []any:
			for _, c := range t {
				if h, p, ok := walk(c); ok {
					return h, p, true
				}
			}
		}
		return "", 0, false
	}
	h, p, _ := walk(v)
	return h, p
}

// endpointPort is the port for host: the base URL's for its host, else 443.
func (b *openshellBackend) endpointPort(host string) int {
	if host == b.baseHost && b.basePort != 0 {
		return b.basePort
	}
	return 443
}

// secret keeps one profile and provider per key: the profile binds the key
// to the endpoint, the provider holds the value (handed to openshell in its
// environment, never on its argument list), and the sandbox has it
// attached. OpenShell puts its own placeholder into the sandbox's process
// environment, so the launch leaves the key out ("").
func (b *openshellBackend) secret(name, host, env, value string) (string, error) {
	id := openshellSecretID(name, env)
	port := b.endpointPort(host)
	if out, err := b.run(nil, "profile", "export", id, "-o", "json"); err == nil {
		// A changed endpoint (the base URL moved) cannot be updated in
		// place without the profile's revision; revoke, then register anew.
		if h, p := profileEndpoint(out); h != host || p != port {
			if err := b.revokeID(name, id); err != nil {
				return "", err
			}
		}
	}
	if _, err := b.run(nil, "profile", "export", id, "-o", "json"); err != nil {
		path, err := writeTemp("cpb-openshell-profile-*.yaml", openshellProfile(id, name, env, host, port))
		if err != nil {
			return "", err
		}
		_, err = b.run(nil, "profile", "import", "-f", path, "--global")
		os.Remove(path)
		if err != nil {
			return "", err
		}
	}
	withValue := append(os.Environ(), env+"="+value)
	if _, err := b.run(nil, "provider", "get", id); err == nil {
		if _, err := b.run(withValue, "provider", "update", id, "--credential", env, "--wait"); err != nil {
			return "", err
		}
	} else if _, err := b.run(withValue, "provider", "create", "--name", id, "--type", id, "--credential", env); err != nil {
		return "", err
	}
	attached, err := b.attached(name)
	if err != nil {
		return "", err
	}
	if !attached[id] {
		if _, err := b.run(nil, "sandbox", "provider", "attach", name, id, "--wait", "--timeout", "60"); err != nil {
			return "", err
		}
	}
	return "", nil
}

// attached lists the providers attached to the sandbox.
func (b *openshellBackend) attached(name string) (map[string]bool, error) {
	out, err := b.run(nil, "sandbox", "provider", "list", name, "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Providers []struct {
			Name string `json:"name"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("openshell sandbox provider list: %w", err)
	}
	m := map[string]bool{}
	for _, p := range list.Providers {
		m[p.Name] = true
	}
	return m, nil
}

// secrets maps each key cpb keeps a provider for, attached to the sandbox,
// to that provider.
func (b *openshellBackend) secrets(name string) (map[string]string, error) {
	attached, err := b.attached(name)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, key := range secretEnvVars {
		if id := openshellSecretID(name, key); attached[id] {
			m[key] = id
		}
	}
	return m, nil
}

func (b *openshellBackend) revoke(name, host, env string, registered map[string]string) (bool, error) {
	id := registered[env]
	if id == "" {
		return false, nil
	}
	return true, b.revokeID(name, id)
}

// revokeID detaches the provider and deletes it with its profile.
func (b *openshellBackend) revokeID(name, id string) error {
	if attached, err := b.attached(name); err == nil && attached[id] {
		if _, err := b.run(nil, "sandbox", "provider", "detach", name, id, "--wait", "--timeout", "60"); err != nil {
			return err
		}
	}
	if _, err := b.run(nil, "provider", "get", id); err == nil {
		if _, err := b.run(nil, "provider", "delete", id); err != nil {
			return err
		}
	}
	if _, err := b.run(nil, "profile", "export", id, "-o", "json"); err == nil {
		if _, err := b.run(nil, "profile", "delete", "--global", id); err != nil {
			return err
		}
	}
	return nil
}

func (b *openshellBackend) allowNetwork(name, host string) error {
	endpoint := host
	if !strings.Contains(host, ":") {
		endpoint = host + ":" + strconv.Itoa(b.endpointPort(host))
	}
	_, err := b.run(nil, "policy", "update", name, "--binary", "/**", "--add-endpoint", endpoint, "--wait")
	return err
}

func (b *openshellBackend) shell(name, command string) error {
	_, err := b.run(nil, "sandbox", "exec", "-n", name, "--no-tty", "--", "bash", "-lc", command)
	return err
}

func (b *openshellBackend) shellOutput(name, command string) (string, error) {
	c := exec.Command(b.bin, "sandbox", "exec", "-n", name, "--no-tty", "--", "bash", "-lc", command)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return string(out), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

func (b *openshellBackend) attach(name string, env []string, tty bool, command string) error {
	args := []string{"sandbox", "exec", "-n", name}
	if tty {
		args = append(args, "--tty")
	} else {
		args = append(args, "--no-tty")
	}
	// The image is the Claude Code pin: a self-update would install a
	// binary the policy does not name.
	for _, kv := range append(append([]string{}, env...), "DISABLE_AUTOUPDATER=1") {
		args = append(args, "--env", kv)
	}
	args = append(args, "--", "bash", "-lc", command)
	c := exec.Command(b.bin, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// remove deletes the sandbox and the profiles and providers cpb keeps for
// it.
func (b *openshellBackend) remove(name string) error {
	if _, err := b.run(nil, "sandbox", "delete", name); err != nil {
		return err
	}
	for _, key := range secretEnvVars {
		id := openshellSecretID(name, key)
		if _, err := b.run(nil, "provider", "get", id); err == nil {
			if _, err := b.run(nil, "provider", "delete", id); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not delete provider %s: %v\n", id, err)
			}
		}
		if _, err := b.run(nil, "profile", "export", id, "-o", "json"); err == nil {
			if _, err := b.run(nil, "profile", "delete", "--global", id); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not delete provider profile %s: %v\n", id, err)
			}
		}
	}
	return nil
}

func (b *openshellBackend) homeDir() string { return "/sandbox" }

func (b *openshellBackend) hostAlias() string { return "host.openshell.internal" }

// policyHost: a service on the host machine is host.openshell.internal to
// the sandbox and to its policy.
func (b *openshellBackend) policyHost(host string) string {
	switch host {
	case b.hostAlias(), "localhost", "127.0.0.1", "::1":
		return b.hostAlias()
	}
	return host
}

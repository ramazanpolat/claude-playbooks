package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// stubOpenshellScript fakes the openshell CLI. Every call appends its
// arguments to $OS_STUB_LOG; a secret key present in its environment is
// written, with its value, to $OS_STUB_LOG.env (the value must reach
// openshell only that way). Answers come from OS_STUB_* variables.
const stubOpenshellScript = `#!/bin/sh
d="$(dirname "$0")"
printf '%s\n' "$*" >> "$OS_STUB_LOG"
for k in ANTHROPIC_AUTH_TOKEN ANTHROPIC_API_KEY; do eval "v=\${$k:-}"; [ -n "$v" ] && printf '%s %s %s=%s\n' "$1" "$2" "$k" "$v" >> "$OS_STUB_LOG.env"; done
if [ -n "$OS_STUB_FAIL" ]; then case "$*" in *"$OS_STUB_FAIL"*) echo "stub failure" >&2; exit 1;; esac; fi
has() { case " $1 " in *" $2 "*) return 0;; esac; return 1; }
case "$1 $2" in
"--version ") echo "openshell ${OS_STUB_VERSION:-0.1.2}";;
"status ") if [ "$OS_STUB_STATUS" = down ]; then echo "Connection refused"; exit 1; fi; echo "Status: Connected";;
"sandbox list") printf '{"sandboxes":['; s=""; for n in $OS_STUB_LS; do printf '%s{"name":"%s"}' "$s" "$n"; s=,; done; printf ']}\n';;
"sandbox get") j="$OS_STUB_GET"; [ -n "$j" ] || j='{"phase":"Ready","labels":{}}'; printf '%s\n' "$j";;
"sandbox create") p=""; for a in "$@"; do [ "$p" = --policy ] && cp "$a" "$d/policy.yaml"; p="$a"; done;;
"sandbox exec") case "$*" in *pgrep*) echo "${OS_STUB_PGREP:-idle}";; *"cat ~/.claude-playbook-sandbox"*) printf 'skills=private';; esac;;
"sandbox provider") if [ "$3" = list ]; then printf '{"providers":['; s=""; for n in $OS_STUB_ATTACHED; do printf '%s{"name":"%s"}' "$s" "$n"; s=,; done; printf ']}\n'; fi;;
"profile export") has "$OS_STUB_PROFILES" "$3" || exit 1; j="$OS_STUB_PROFILE_JSON"; [ -n "$j" ] || j='{"endpoints":[{"host":"router.local","port":9}]}'; printf '%s\n' "$j";;
"profile import") p=""; for a in "$@"; do [ "$p" = -f ] && cp "$a" "$d/profile.yaml"; p="$a"; done;;
"provider get") has "$OS_STUB_PROVIDERS" "$3" || exit 1;;
esac
exit 0
`

// stubOpenshell puts fake openshell, docker, systemctl and loginctl first
// on PATH, pretends to be Linux as uid 1000, and returns the call log.
func stubOpenshell(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	scripts := map[string]string{
		"openshell": stubOpenshellScript,
		"docker": "#!/bin/sh\nprintf 'docker %s\\n' \"$*\" >> \"$OS_STUB_LOG\"\n[ -z \"$DOCKER_STUB_DOWN\" ] || exit 1\n" +
			"case \"$1\" in version) echo \"${DOCKER_STUB_VERSION:-29.8.1}\";; image) [ \"$DOCKER_STUB_IMAGE\" = present ] || exit 1;; build) cat > \"$(dirname \"$0\")/Dockerfile.built\";; esac\nexit 0\n",
		"systemctl": "#!/bin/sh\necho \"${SYSTEMCTL_STUB:-inactive}\"\n",
		"loginctl":  "#!/bin/sh\necho \"${LOGINCTL_STUB:-yes}\"\n",
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(dir, "calls.log")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OS_STUB_LOG", log)
	for _, k := range []string{"OS_STUB_FAIL", "OS_STUB_VERSION", "OS_STUB_STATUS", "OS_STUB_LS", "OS_STUB_GET", "OS_STUB_PGREP", "OS_STUB_ATTACHED", "OS_STUB_PROFILES", "OS_STUB_PROFILE_JSON", "OS_STUB_PROVIDERS", "DOCKER_STUB_DOWN", "DOCKER_STUB_VERSION", "DOCKER_STUB_IMAGE", "SYSTEMCTL_STUB", "LOGINCTL_STUB", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		t.Setenv(k, "")
	}
	goos, uid, gid, wait, delay := sandboxGOOS, sandboxUID, sandboxGID, openshellGatewayWait, openshellRetryDelay
	sandboxGOOS, sandboxUID, sandboxGID, openshellGatewayWait, openshellRetryDelay = "linux", func() int { return 1000 }, func() int { return 1000 }, 0, 0
	t.Cleanup(func() {
		sandboxGOOS, sandboxUID, sandboxGID, openshellGatewayWait, openshellRetryDelay = goos, uid, gid, wait, delay
	})
	return log
}

func stubLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// inOrder fails unless each fragment appears in some line, in order (the
// next fragment may sit on the same line).
func inOrder(t *testing.T, lines []string, frags ...string) {
	t.Helper()
	i := 0
	for _, f := range frags {
		for i < len(lines) && !strings.Contains(lines[i], f) {
			i++
		}
		if i == len(lines) {
			t.Fatalf("missing %q (in order) in:\n%s", f, strings.Join(lines, "\n"))
		}
	}
}

func TestOpenshellPolicyAndMounts(t *testing.T) {
	got := openshellPolicy([]string{"/w", "/p b:ro"}, 1500, 1501, "")
	want := `version: 1
filesystem_policy:
  include_workdir: true
  read_only:
    - "/bin"
    - "/usr"
    - "/lib"
    - "/etc"
    - "/proc"
    - "/dev/urandom"
    - "/var/log"
    - "/p b"
  read_write:
    - "/tmp"
    - "/dev/null"
    - "/w"
landlock:
  compatibility: hard_requirement
process:
  run_as_user: "1500"
  run_as_group: "1501"
network_policies:
  cpb_claude:
    endpoints:
      - {host: api.anthropic.com, port: 443, protocol: rest, access: read-write, enforcement: enforce}
      - {host: platform.claude.com, port: 443, protocol: rest, access: read-write, enforcement: enforce}
      - {host: statsig.anthropic.com, port: 443, protocol: rest, access: read-write, enforcement: enforce}
      - {host: claude.ai, port: 443, protocol: rest, access: read-write, enforcement: enforce}
      - {host: console.anthropic.com, port: 443, protocol: rest, access: read-write, enforcement: enforce}
    binaries:
      - path: /usr/local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe
`
	if got != want {
		t.Fatalf("policy:\n%s\nwant:\n%s", got, want)
	}
	// The endpoint a key's provider covers is left out (a second rule for
	// it would keep the provider's from installing).
	if p := openshellPolicy(nil, 1, 1, "api.anthropic.com:443"); strings.Contains(p, "api.anthropic.com") || !strings.Contains(p, "platform.claude.com") {
		t.Fatalf("excluded endpoint:\n%s", p)
	}
	if got, want := openshellMounts([]string{"/w", "/p b:ro"}), `{"docker":{"mounts":[{"type":"bind","source":"/w","target":"/w","read_only":false},{"type":"bind","source":"/p b","target":"/p b","read_only":true}]}}`; got != want {
		t.Fatalf("mounts: %s", got)
	}
	for v, ok := range map[string]bool{"openshell 0.1.2": true, "openshell 0.1.10": true, "openshell 0.1.2-pre.3": true, "openshell 0.1.1": false, "openshell 0.2.0": false, "": false} {
		if openshellVersionOK(v) != ok {
			t.Errorf("version %q: want %v", v, ok)
		}
	}
	if h, p := profileEndpoint(`{"profile":{"id":"x","endpoints":[{"host":"buildbox","port":8080}]}}`); h != "buildbox" || p != 8080 {
		t.Errorf("profileEndpoint: %s %d", h, p)
	}
}

func TestOpenshellPreflightRefusals(t *testing.T) {
	cases := []struct {
		name, want string
		set        func()
	}{
		{"macos", "runs on Linux with Docker Engine; here, use --sandbox=sbx", func() { sandboxGOOS = "darwin" }},
		{"old", "OpenShell 0.1.1 is not supported", func() { t.Setenv("OS_STUB_VERSION", "0.1.1") }},
		{"docker27", "needs Docker Engine 28 or newer as the gateway's compute driver (found: 27.5.1)", func() { t.Setenv("DOCKER_STUB_VERSION", "27.5.1") }},
		{"nodocker", "(found: no Docker Engine answered)", func() { t.Setenv("DOCKER_STUB_DOWN", "1") }},
		{"root", "does not run as root", func() { sandboxUID = func() int { return 0 } }},
		{"gateway", "the OpenShell gateway is not running: systemctl --user start openshell-gateway", func() { t.Setenv("OS_STUB_STATUS", "down") }},
		{"cold-gateway-times-out", "the OpenShell gateway is not running", func() { t.Setenv("OS_STUB_STATUS", "down"); t.Setenv("SYSTEMCTL_STUB", "active") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubOpenshell(t)
			c.set()
			if _, err := newSandboxBackend("openshell"); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want %q, got %v", c.want, err)
			}
		})
	}
	t.Run("missing", func(t *testing.T) {
		stubOpenshell(t)
		t.Setenv("PATH", t.TempDir())
		if _, err := newSandboxBackend("openshell"); err == nil || !strings.Contains(err.Error(), "'openshell' (NVIDIA OpenShell 0.1.x) not found") {
			t.Fatalf("missing CLI: %v", err)
		}
	})
	t.Run("ok", func(t *testing.T) {
		stubOpenshell(t)
		if _, err := newSandboxBackend("openshell"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRunSandboxOpenshellCreatesInjectsAndStops(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	if err := runEnvProfile(nil, []string{"router", "set", "ANTHROPIC_BASE_URL=http://router.local:9/v1", "ANTHROPIC_AUTH_TOKEN=real-token", "MODEL=glm"}); err != nil {
		t.Fatal(err)
	}
	writePlaybook(t, root, "box", &manifest.Manifest{IsolateAuth: true, Env: &manifest.Env{Profiles: []string{"router"}}, Sandbox: &manifest.Sandbox{AllowNet: []string{"api.example.com", "127.0.0.1:8000", "::1"}}})
	work := t.TempDir()
	log := stubOpenshell(t)
	var err error
	stderr := captureStderr(t, func() { err = runRun(nil, []string{"--sandbox=openshell", "--workdir", work, "box"}) })
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	label := (&openshellBackend{claudeVersion: openshellClaudeVersion}).imageLabel()
	pbDir, cwork := canon(t, filepath.Join(root, "box")), canon(t, work)
	calls := stubLines(t, log)
	inOrder(t, calls,
		"sandbox list -o json",
		"docker image inspect cpb-openshell/claude:"+label,
		"docker build -t cpb-openshell/claude:"+label+" --build-arg CLAUDE_CODE_VERSION="+openshellClaudeVersion+" -",
		"sandbox create --name cpb-box --from cpb-openshell/claude:"+label+" --detach --policy ",
		`--driver-config-json {"docker":{"mounts":[{"type":"bind","source":"`+cwork+`","target":"`+cwork+`","read_only":false},{"type":"bind","source":"`+pbDir+`","target":"`+pbDir+`","read_only":false}]}} --label cpb-image=`+label,
		"sandbox exec -n cpb-box --no-tty -- bash -lc printf %s 'skills=private' > ~/.claude-playbook-sandbox",
		"policy update cpb-box --binary /** --add-endpoint api.example.com:443 --wait",
		// A host on this machine, with a port or as an IPv6 address, is
		// spelled as the sandbox reaches it.
		"policy update cpb-box --binary /** --add-endpoint host.openshell.internal:8000 --wait",
		"policy update cpb-box --binary /** --add-endpoint host.openshell.internal:443 --wait",
		"sandbox provider list cpb-box -o json",
		"profile export cpb-box-anthropic-auth-token -o json",
		"profile import -f ",
		"provider create --name cpb-box-anthropic-auth-token --type cpb-box-anthropic-auth-token --credential ANTHROPIC_AUTH_TOKEN",
		"sandbox provider attach cpb-box cpb-box-anthropic-auth-token --wait --timeout 60",
		"sandbox exec -n cpb-box --no-tty --env ",
		"pgrep -x claude",
		"sandbox stop cpb-box",
	)
	joined := strings.Join(calls, "\n")
	// The key's provider brings the endpoint's rule; cpb adds none there.
	if strings.Contains(joined, "--add-endpoint router.local:9") {
		t.Fatalf("a rule of cpb's own for the key's endpoint:\n%s", joined)
	}
	if strings.Contains(joined, "real-token") || strings.Contains(joined, "claude.ai/install.sh") {
		t.Fatalf("the key reached an argument list, or the in-sandbox pin ran:\n%s", joined)
	}
	var attach string
	for _, c := range calls {
		if strings.HasPrefix(c, "sandbox exec -n cpb-box --no-tty --env ") {
			attach = c
		}
	}
	for _, frag := range []string{"--env ANTHROPIC_BASE_URL=http://router.local:9/v1", "--env MODEL=glm", "--env CLAUDE_CONFIG_DIR=" + pbDir, "--env DISABLE_AUTOUPDATER=1", "-- bash -lc cd '" + cwork + "' && exec claude"} {
		if !strings.Contains(attach, frag) {
			t.Errorf("attach lacks %q: %s", frag, attach)
		}
	}
	if strings.Contains(attach, "ANTHROPIC_AUTH_TOKEN") {
		t.Errorf("the attach overrides OpenShell's placeholder: %s", attach)
	}
	if env := strings.Join(stubLines(t, log+".env"), "\n"); env != "provider create ANTHROPIC_AUTH_TOKEN=real-token" {
		t.Errorf("the value must reach openshell only in provider create's environment, got:\n%s", env)
	}
	stub := filepath.Dir(log)
	policy, _ := os.ReadFile(filepath.Join(stub, "policy.yaml"))
	for _, frag := range []string{"    - \"" + cwork + "\"\n", "    - \"" + pbDir + "\"\n", "compatibility: hard_requirement", "run_as_user: \"1000\""} {
		if !strings.Contains(string(policy), frag) {
			t.Errorf("policy lacks %q:\n%s", frag, policy)
		}
	}
	profile, _ := os.ReadFile(filepath.Join(stub, "profile.yaml"))
	for _, frag := range []string{"id: cpb-box-anthropic-auth-token", "env_vars: [ANTHROPIC_AUTH_TOKEN]", "auth_style: bearer", "header_name: authorization", `{host: "router.local", port: 9,`, "binaries: [" + openshellClaudePath + "]"} {
		if !strings.Contains(string(profile), frag) {
			t.Errorf("profile lacks %q:\n%s", frag, profile)
		}
	}
	if built, _ := os.ReadFile(filepath.Join(stub, "Dockerfile.built")); string(built) != openshellDockerfile {
		t.Errorf("the image was not built from the embedded recipe")
	}
	if !strings.Contains(stderr, "Sandbox cpb-box stopped") {
		t.Errorf("no stop note: %s", stderr)
	}
}

func TestRunSandboxOpenshellReuseRotateRevoke(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	if err := runEnvProfile(nil, []string{"router", "set", "ANTHROPIC_BASE_URL=http://router.local:9/v1", "ANTHROPIC_AUTH_TOKEN=new-token"}); err != nil {
		t.Fatal(err)
	}
	writePlaybook(t, root, "box", &manifest.Manifest{IsolateAuth: true, Env: &manifest.Env{Profiles: []string{"router"}}})
	work := t.TempDir()
	log := stubOpenshell(t)
	label := (&openshellBackend{claudeVersion: openshellClaudeVersion}).imageLabel()
	pbDir, cwork := canon(t, filepath.Join(root, "box")), canon(t, work)
	get := `{"phase":"Stopped","labels":{"cpb-image":"` + label + `"},"policy":{"filesystem_policy":{"read_only":["/usr","/etc"],"read_write":["/tmp","` + cwork + `","` + pbDir + `"]}}}`
	t.Setenv("OS_STUB_LS", "cpb-box")
	t.Setenv("OS_STUB_GET", get)
	t.Setenv("OS_STUB_ATTACHED", "cpb-box-anthropic-auth-token cpb-box-anthropic-api-key")
	t.Setenv("OS_STUB_PROFILES", "cpb-box-anthropic-auth-token cpb-box-anthropic-api-key")
	t.Setenv("OS_STUB_PROVIDERS", "cpb-box-anthropic-auth-token cpb-box-anthropic-api-key")
	var err error
	stderr := captureStderr(t, func() { err = runRun(nil, []string{"--sandbox=openshell", "--workdir", work, "box"}) })
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	calls := stubLines(t, log)
	// The stopped sandbox is started, the rotated token updated in place,
	// the key no longer in the environment revoked (detached, deleted).
	inOrder(t, calls,
		"sandbox get cpb-box -o json",
		"sandbox start cpb-box",
		"sandbox provider list cpb-box -o json",
		"provider update cpb-box-anthropic-auth-token --credential ANTHROPIC_AUTH_TOKEN --wait",
		"sandbox provider detach cpb-box cpb-box-anthropic-api-key --wait --timeout 60",
		"provider delete cpb-box-anthropic-api-key",
		"profile delete --global cpb-box-anthropic-api-key",
		"sandbox exec -n cpb-box --no-tty --env ",
	)
	joined := strings.Join(calls, "\n")
	for _, never := range []string{"sandbox create", "profile import", "provider create", "sandbox provider attach", "docker build", "--add-endpoint"} {
		if strings.Contains(joined, never) {
			t.Fatalf("reuse ran %q:\n%s", never, joined)
		}
	}
	if !strings.Contains(stderr, "Secret ANTHROPIC_API_KEY revoked at the proxy") || !strings.Contains(stderr, "Starting sandbox cpb-box") {
		t.Fatalf("stderr: %s", stderr)
	}
	// A second session still inside: the sandbox keeps running.
	os.Remove(log)
	t.Setenv("OS_STUB_GET", strings.Replace(get, "Stopped", "Ready", 1))
	t.Setenv("OS_STUB_ATTACHED", "cpb-box-anthropic-auth-token")
	t.Setenv("OS_STUB_PGREP", "busy")
	if err := runRun(nil, []string{"--sandbox=openshell", "--workdir", work, "box"}); err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(stubLines(t, log), "\n"); strings.Contains(joined, "sandbox stop") || strings.Contains(joined, "sandbox start") {
		t.Fatalf("busy sandbox stopped, or a running one started:\n%s", joined)
	}
	// An explicit pin the sandbox's image does not carry refuses.
	writePlaybook(t, root, "box", &manifest.Manifest{IsolateAuth: true, Env: &manifest.Env{Profiles: []string{"router"}}, Sandbox: &manifest.Sandbox{ClaudeVersion: "2.1.300"}})
	os.Remove(log)
	t.Setenv("OS_STUB_PGREP", "idle")
	err = runRun(nil, []string{"--sandbox=openshell", "--workdir", work, "box"})
	if err == nil || !strings.Contains(err.Error(), "this launch pins Claude Code 2.1.300") || !strings.Contains(err.Error(), "--sandbox-fresh") {
		t.Fatalf("pin mismatch: %v", err)
	}
	// The refused reuse leaves the sandbox (another session's, perhaps)
	// as it was: no probe, no stop.
	if joined := strings.Join(stubLines(t, log), "\n"); strings.Contains(joined, "pgrep") || strings.Contains(joined, "sandbox stop") {
		t.Fatalf("a refused reuse touched the sandbox:\n%s", joined)
	}
	// --sandbox-fresh removes the sandbox and cpb's providers and profiles.
	writePlaybook(t, root, "box", &manifest.Manifest{IsolateAuth: true, Env: &manifest.Env{Profiles: []string{"router"}}})
	os.Remove(log)
	t.Setenv("OS_STUB_PGREP", "")
	if err := runRun(nil, []string{"--sandbox=openshell", "--sandbox-fresh", "--workdir", work, "box"}); err != nil {
		t.Fatal(err)
	}
	inOrder(t, stubLines(t, log), "sandbox delete cpb-box", "provider delete cpb-box-anthropic-auth-token", "profile delete --global cpb-box-anthropic-auth-token", "provider delete cpb-box-anthropic-api-key", "sandbox create --name cpb-box")
}

func TestRunSandboxOpenshellRefusals(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	if err := runEnvProfile(nil, []string{"canary", "set", "ANTHROPIC_API_KEY=cpbcanaryopenshell"}); err != nil {
		t.Fatal(err)
	}
	writePlaybook(t, root, "box", &manifest.Manifest{IsolateAuth: true, Env: &manifest.Env{Profiles: []string{"canary"}}})
	work := t.TempDir()
	log := stubOpenshell(t)
	// A provider that cannot be created refuses the launch before any
	// attach, and the value is in no message.
	t.Setenv("OS_STUB_FAIL", "provider create")
	var err error
	stderr := captureStderr(t, func() { err = runRun(nil, []string{"--sandbox=openshell", "--workdir", work, "box"}) })
	if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY could not be registered at the sandbox proxy for api.anthropic.com") {
		t.Fatalf("failed provider: %v", err)
	}
	if strings.Contains(err.Error(), "cpbcanaryopenshell") || strings.Contains(stderr, "cpbcanaryopenshell") {
		t.Fatalf("the value leaked: %v\n%s", err, stderr)
	}
	for _, c := range stubLines(t, log) {
		if strings.Contains(c, "cpbcanaryopenshell") || strings.HasPrefix(c, "sandbox exec -n cpb-box --no-tty --env") {
			t.Fatalf("attached, or the value on an argument list: %q", c)
		}
	}
	// The sandbox the refused launch created is stopped all the same.
	if c := stubLines(t, log); c[len(c)-1] != "sandbox stop cpb-box" {
		t.Fatalf("a launch refused before the attach left the sandbox running:\n%s", strings.Join(c, "\n"))
	}
	// A registered mapping that cannot be listed stops the launch: the
	// backend would go on injecting a key the pilot removed.
	t.Setenv("OS_STUB_FAIL", "sandbox provider list")
	if err := runRun(nil, []string{"--sandbox=openshell", "--workdir", work, "box"}); err == nil || !strings.Contains(err.Error(), "could not list the secrets registered for sandbox cpb-box") {
		t.Fatalf("unlisted secrets: %v", err)
	}
	t.Setenv("OS_STUB_FAIL", "")
	// A mount at one of the sandbox's own system paths is refused.
	if err := runRun(nil, []string{"--sandbox=openshell", "--mount", "/usr:ro", "--workdir", work, "box"}); err == nil || !strings.Contains(err.Error(), "cannot mount /usr: it is one of the sandbox's own system paths") {
		t.Fatalf("baseline mount: %v", err)
	}
	// --clone and share_skills are sbx's.
	if err := runRun(nil, []string{"--sandbox=openshell", "--clone", "--workdir", work, "box"}); err == nil || !strings.Contains(err.Error(), "--clone is sbx-only for now") {
		t.Fatalf("--clone: %v", err)
	}
	writePlaybook(t, root, "shared", &manifest.Manifest{IsolateAuth: true, Sandbox: &manifest.Sandbox{ShareSkills: true}})
	if err := runRun(nil, []string{"--sandbox=openshell", "--workdir", work, "shared"}); err == nil || !strings.Contains(err.Error(), "share_skills applies to sbx") {
		t.Fatalf("share_skills: %v", err)
	}
	// Linger off is a warning, not a refusal.
	t.Setenv("LOGINCTL_STUB", "no")
	writePlaybook(t, root, "plain", &manifest.Manifest{IsolateAuth: true})
	stderr = captureStderr(t, func() { err = runRun(nil, []string{"--sandbox=openshell", "--workdir", work, "plain"}) })
	if err != nil || !strings.Contains(stderr, "Warning: linger is off for") {
		t.Fatalf("linger: %v\n%s", err, stderr)
	}
}

func TestRunSandboxOpenshellEndpointRules(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	if err := runEnvProfile(nil, []string{"router", "set", "ANTHROPIC_BASE_URL=http://router.local:9/v1", "ANTHROPIC_AUTH_TOKEN=tok"}); err != nil {
		t.Fatal(err)
	}
	writePlaybook(t, root, "box", &manifest.Manifest{IsolateAuth: true, Env: &manifest.Env{Profiles: []string{"router"}}})
	work := t.TempDir()
	log := stubOpenshell(t)
	label := (&openshellBackend{claudeVersion: openshellClaudeVersion}).imageLabel()
	pol := func(net string) string {
		return `{"phase":"Ready","labels":{"cpb-image":"` + label + `"},"policy":{"filesystem_policy":{"read_write":["` + canon(t, work) + `","` + canon(t, filepath.Join(root, "box")) + `"]},"network_policies":{` + net + `}}}`
	}
	// A sandbox created before the key was set has cpb's own rule for the
	// endpoint: it is removed before the provider is attached.
	t.Setenv("OS_STUB_LS", "cpb-box")
	t.Setenv("OS_STUB_GET", pol(`"allow_router_local_9":{"endpoints":[{"host":"router.local","port":9}]},"_provider_x":{"endpoints":[{"host":"other","port":1}]}`))
	if err := runRun(nil, []string{"--sandbox=openshell", "--workdir", work, "box"}); err != nil {
		t.Fatal(err)
	}
	inOrder(t, stubLines(t, log), "provider create --name cpb-box-anthropic-auth-token", "policy update cpb-box --remove-endpoint router.local:9 --wait", "sandbox provider attach cpb-box cpb-box-anthropic-auth-token")
	// The key gone and none left: the endpoint gets a rule of its own again.
	if err := runEnvProfile(nil, []string{"router", "unset", "ANTHROPIC_AUTH_TOKEN"}); err != nil {
		t.Fatal(err)
	}
	os.Remove(log)
	t.Setenv("OS_STUB_GET", pol(""))
	t.Setenv("OS_STUB_ATTACHED", "cpb-box-anthropic-auth-token")
	t.Setenv("OS_STUB_PROVIDERS", "cpb-box-anthropic-auth-token")
	t.Setenv("OS_STUB_PROFILES", "cpb-box-anthropic-auth-token")
	if err := runRun(nil, []string{"--sandbox=openshell", "--workdir", work, "box"}); err != nil {
		t.Fatal(err)
	}
	inOrder(t, stubLines(t, log), "sandbox provider detach cpb-box cpb-box-anthropic-auth-token", "profile delete --global cpb-box-anthropic-auth-token", "policy update cpb-box --binary /** --add-endpoint router.local:9 --wait")
}

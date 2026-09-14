package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// stubSbx puts a fake `sbx` first on PATH. Every invocation appends its
// arguments (space-joined) to the log file; `ls -q` prints the names in
// SBX_STUB_LS (one per line). Returns the log path.
func stubSbx(t *testing.T, existing ...string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "sbx.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SBX_STUB_LOG\"\n" +
		"if [ \"$1\" = ls ]; then printf '%s\\n' $SBX_STUB_LS; fi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "sbx"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SBX_STUB_LOG", log)
	t.Setenv("SBX_STUB_LS", strings.Join(existing, " "))
	return log
}

// canon is the symlink-resolved absolute path, the form the sandbox mounts
// (t.TempDir lives under a symlink on macOS).
func canon(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func sbxCalls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("stub sbx was never called: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestTakeRunFlagsMixesLaunchAndSandboxFlags(t *testing.T) {
	var o sandboxOpts
	rest, layers, err := takeRunFlags([]string{"--sandbox", "--env", "K=V", "--workdir", "/w", "--mount=/m:ro", "--clone", "pb", "--sandbox-fresh", "-p", "hi"}, &o, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !o.enabled || !o.clone || o.fresh || o.workdir != "/w" || len(o.mounts) != 1 || o.mounts[0] != "/m:ro" {
		t.Fatalf("opts before the name: %+v", o)
	}
	if len(layers) != 1 || layers[0].Set["K"] != "V" {
		t.Fatalf("layers: %#v", layers)
	}
	if len(rest) != 4 || rest[0] != "pb" {
		t.Fatalf("rest: %q", rest)
	}
	rest, _, err = takeRunFlags(rest[1:], &o, nil)
	if err != nil || !o.fresh || len(rest) != 2 || rest[0] != "-p" {
		t.Fatalf("after the name: fresh=%v rest=%q err=%v", o.fresh, rest, err)
	}
	for _, bad := range [][]string{{"--workdir"}, {"--mount="}, {"--workdir", ""}} {
		if _, _, err := takeRunFlags(bad, &sandboxOpts{}, nil); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSandboxHelpers(t *testing.T) {
	if got := sandboxName("my pb/x_1.2"); got != "cpb-my-pb-x-1.2" {
		t.Fatalf("sandboxName: %q", got)
	}
	env := []string{"HOME=/h", "SECRET=1", "CLAUDE_CONFIG_DIR=/c", "MODEL=glm", "CLAUDE_CODE_OAUTH_TOKEN=t", "ANTHROPIC_BASE_URL=http://tr0:20128/v1"}
	got := sandboxEnv(env, &manifest.Env{Set: map[string]string{"MODEL": "glm", "ANTHROPIC_BASE_URL": "x"}})
	want := "CLAUDE_CONFIG_DIR=/c MODEL=glm CLAUDE_CODE_OAUTH_TOKEN=t ANTHROPIC_BASE_URL=http://tr0:20128/v1"
	if strings.Join(got, " ") != want {
		t.Fatalf("sandboxEnv: %q", got)
	}
	if h := baseURLHost(env); h != "tr0" {
		t.Fatalf("baseURLHost: %q", h)
	}
	if h := baseURLHost([]string{"ANTHROPIC_BASE_URL=::bad"}); h != "" {
		t.Fatalf("baseURLHost on junk: %q", h)
	}
}

func TestRunSandboxCreatesConfiguresAndAttaches(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	extra := t.TempDir()
	work := t.TempDir()
	writePlaybook(t, root, "box", &manifest.Manifest{
		Env:     &manifest.Env{Set: map[string]string{"ANTHROPIC_BASE_URL": "http://router.local:9/v1", "MODEL": "glm"}},
		Sandbox: &manifest.Sandbox{Mounts: []string{extra + ":ro"}, AllowNet: []string{"api.example.com"}, ClaudeVersion: "2.1.263"},
	})
	t.Setenv("HOSTONLY", "leak")
	log := stubSbx(t)
	if err := runRun(nil, []string{"--sandbox", "--workdir", work, "box", "--env", "EXTRA=1", "-p", "it's"}); err != nil {
		t.Fatal(err)
	}
	calls := sbxCalls(t, log)
	pbDir := canon(t, filepath.Join(root, "box"))
	work, extra = canon(t, work), canon(t, extra)
	want := []string{
		"ls -q",
		"create --name cpb-box claude " + work + " " + pbDir + " " + extra + ":ro",
		"policy allow network --sandbox cpb-box api.example.com",
		"policy allow network --sandbox cpb-box router.local",
		"exec cpb-box bash -lc set -o pipefail; curl -fsSL https://claude.ai/install.sh | bash -s '2.1.263'",
	}
	for i, w := range want {
		if i >= len(calls) || calls[i] != w {
			t.Fatalf("call %d: want %q\ngot %q", i, w, calls)
		}
	}
	attach := calls[len(calls)-1]
	// Tests run without a terminal, so no pty is requested.
	for _, frag := range []string{"exec -i -e ", "-e CLAUDE_CONFIG_DIR=" + pbDir, "-e MODEL=glm", "-e EXTRA=1", " cpb-box bash -lc cd '" + work + "' && exec claude '-p' 'it'\\''s'"} {
		if !strings.Contains(attach, frag) {
			t.Errorf("attach lacks %q: %q", frag, attach)
		}
	}
	if strings.Contains(attach, "HOSTONLY") || strings.Contains(attach, "-e HOME=") {
		t.Errorf("host environment leaked into the sandbox: %q", attach)
	}
}

func TestRunSandboxReusesOrRecreates(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	writePlaybook(t, root, "box", nil)
	work := t.TempDir()
	log := stubSbx(t, "other", "cpb-box")
	if err := runRun(nil, []string{"--sandbox", "--workdir", work, "box"}); err != nil {
		t.Fatal(err)
	}
	calls := sbxCalls(t, log)
	if len(calls) != 2 || calls[0] != "ls -q" || !strings.HasPrefix(calls[1], "exec -i ") {
		t.Fatalf("reuse should only list and attach: %q", calls)
	}
	os.Remove(log)
	if err := runRun(nil, []string{"--sandbox", "--sandbox-fresh", "--clone", "--workdir", work, "box"}); err != nil {
		t.Fatal(err)
	}
	calls = sbxCalls(t, log)
	if len(calls) != 4 || calls[1] != "rm -f cpb-box" || calls[2] != "create --name cpb-box --clone claude "+canon(t, work)+" "+canon(t, filepath.Join(root, "box")) {
		t.Fatalf("fresh should remove and recreate: %q", calls)
	}
}

func TestRunSandboxRefusals(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	writePlaybook(t, root, "box", nil)
	err := runRun(nil, []string{"--workdir", t.TempDir(), "box"})
	if err == nil || !strings.Contains(err.Error(), "add --sandbox") {
		t.Fatalf("sandbox-only flags without --sandbox: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	err = runRun(nil, []string{"--sandbox", "box"})
	if err == nil || !strings.Contains(err.Error(), "'sbx' (Docker Sandboxes) not found") {
		t.Fatalf("missing sbx: %v", err)
	}
	stubSbx(t)
	err = runRun(nil, []string{"--sandbox", "--workdir", filepath.Join(t.TempDir(), "missing"), "box"})
	if err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("missing workdir: %v", err)
	}
}

func TestRunSandboxDetachesSharedLogin(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	writePlaybook(t, root, "box", nil)
	// A global login and no token: the launch links the playbook's store to
	// ~/.claude, which the sandbox cannot see.
	home := os.Getenv("HOME")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r","expiresAt":9999999999999}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	log := stubSbx(t)
	// An unsandboxed preparation links the store; the sandboxed launch
	// detaches it for this launch and runs as own-login inside.
	if err := runRun(nil, []string{"--sandbox", "--workdir", t.TempDir(), "box"}); err != nil {
		t.Fatalf("shared login: %v", err)
	}
	store := filepath.Join(root, "box", ".credentials.json")
	if info, err := os.Lstat(store); err == nil && info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("the shared-login link survived into a sandboxed launch")
	}
	if calls := sbxCalls(t, log); !strings.HasPrefix(calls[len(calls)-1], "exec -i ") || strings.Contains(calls[len(calls)-1], "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Fatalf("attach after detach: %q", calls)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", ".credentials.json")); err != nil {
		t.Fatal("the machine login was touched")
	}
	os.Remove(log)
	// An isolated playbook holds its own store: launched.
	writePlaybook(t, root, "iso", &manifest.Manifest{IsolateAuth: true})
	if err := runRun(nil, []string{"--sandbox", "--workdir", t.TempDir(), "iso"}); err != nil {
		t.Fatal(err)
	}
	// So is one the layers give a token.
	if err := runRun(nil, []string{"--sandbox", "--workdir", t.TempDir(), "--env", "CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-x", "box"}); err != nil {
		t.Fatal(err)
	}
	// A token launch whose store is a leftover link to a grantless store:
	// the token path leaves such a link alone, and the launch proceeds.
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"mcpOAuth":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	writePlaybook(t, root, "leftover", nil)
	if err := os.Symlink(filepath.Join(home, ".claude", ".credentials.json"), filepath.Join(root, "leftover", ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	if err := runRun(nil, []string{"--sandbox", "--workdir", t.TempDir(), "--env", "CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-x", "leftover"}); err != nil {
		t.Fatalf("token launch with a grantless leftover link: %v", err)
	}
	if info, err := os.Lstat(filepath.Join(root, "leftover", ".credentials.json")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the leftover link was expected to survive (the fixture would not exercise the refusal otherwise)")
	}
}

func TestRunSandboxResolvesLinkedAndRelativePaths(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	// A linked playbook: the registry entry is a symlink to a directory
	// elsewhere. The target is mounted and named inside the sandbox.
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writePlaybook(t, filepath.Dir(target), "elsewhere", nil)
	if err := os.Symlink(target, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	log := stubSbx(t)
	if err := runRun(nil, []string{"--sandbox", "--workdir", work, "linked"}); err != nil {
		t.Fatal(err)
	}
	calls := sbxCalls(t, log)
	if calls[1] != "create --name cpb-linked claude "+canon(t, work)+" "+canon(t, target) {
		t.Fatalf("linked playbook mounts: %q", calls)
	}
	if !strings.Contains(calls[len(calls)-1], "-e CLAUDE_CONFIG_DIR="+canon(t, target)+" ") {
		t.Fatalf("linked playbook config dir inside: %q", calls[len(calls)-1])
	}
	// A relative --playbooks-dir still yields an absolute config directory.
	os.Remove(log)
	writePlaybook(t, root, "rel", nil)
	wd, _ := os.Getwd()
	if err := os.Chdir(filepath.Dir(root)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })
	if err := runRun(nil, []string{"--playbooks-dir", filepath.Base(root), "--sandbox", "--workdir", work, "rel"}); err != nil {
		t.Fatal(err)
	}
	calls = sbxCalls(t, log)
	if !strings.Contains(calls[len(calls)-1], "-e CLAUDE_CONFIG_DIR="+canon(t, filepath.Join(root, "rel"))+" ") {
		t.Fatalf("relative registry: %q", calls[len(calls)-1])
	}
}

// stubClaude puts a fake `claude` first on PATH that records its argv and
// environment in the given file, so a host launch is provable.
func stubClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "claude.log")
	script := "#!/bin/sh\nprintf 'ARGS %s\\n' \"$*\" > \"$CLAUDE_STUB_LOG\"\nenv >> \"$CLAUDE_STUB_LOG\"\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_STUB_LOG", log)
	return log
}

func TestRunSandboxAlwaysAndOverride(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	writePlaybook(t, root, "locked", &manifest.Manifest{IsolateAuth: true, Sandbox: &manifest.Sandbox{Always: true}})
	writePlaybook(t, root, "plain", &manifest.Manifest{IsolateAuth: true})
	work := t.TempDir()
	sbxLog := stubSbx(t)
	claudeLog := stubClaude(t)
	// always: no flag needed.
	if err := runRun(nil, []string{"--workdir", work, "locked", "--version"}); err != nil {
		t.Fatal(err)
	}
	calls := sbxCalls(t, sbxLog)
	if !strings.HasPrefix(calls[1], "create --name cpb-locked ") || !strings.HasSuffix(calls[len(calls)-1], "exec claude '--version'") {
		t.Fatalf("always-sandboxed launch: %q", calls)
	}
	if _, err := os.Stat(claudeLog); err == nil {
		t.Fatal("host claude ran for an always-sandboxed playbook")
	}
	// --no-sandbox overrides for one launch, on the host, with a note.
	os.Remove(sbxLog)
	err := runRun(nil, []string{"--no-sandbox", "locked", "--version"})
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(claudeLog); err != nil || !strings.HasPrefix(string(data), "ARGS --version\n") {
		t.Fatalf("host launch under --no-sandbox: %v %q", err, data)
	}
	if _, err := os.Stat(sbxLog); err == nil {
		t.Fatal("sbx was called under --no-sandbox")
	}
	// A plain playbook is unaffected by --no-sandbox; a launcher-style
	// position after the name works too.
	os.Remove(claudeLog)
	if err := runRun(nil, []string{"plain", "--no-sandbox", "--version"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(claudeLog); err != nil {
		t.Fatal("plain playbook did not launch on the host")
	}
	// Contradictions and unknown backends refuse before anything runs.
	for _, args := range [][]string{
		{"--sandbox", "--no-sandbox", "plain"},
		{"--sandbox=tart", "plain"},
		{"--sandbox=", "plain"},
		{"--no-sandbox", "--workdir", work, "plain"},
	} {
		os.Remove(sbxLog)
		os.Remove(claudeLog)
		if err := runRun(nil, args); err == nil {
			t.Errorf("%q accepted", args)
		}
		if _, err := os.Stat(sbxLog); err == nil {
			t.Errorf("%q reached sbx", args)
		}
	}
	// --sbx and --sandbox=sbx are the same switch.
	for _, flag := range []string{"--sbx", "--sandbox=sbx"} {
		os.Remove(sbxLog)
		if err := runRun(nil, []string{flag, "--workdir", work, "plain"}); err != nil {
			t.Fatalf("%s: %v", flag, err)
		}
		if calls := sbxCalls(t, sbxLog); !strings.HasPrefix(calls[len(calls)-1], "exec -i ") {
			t.Fatalf("%s: %q", flag, calls)
		}
	}
}

func TestStartSandbox(t *testing.T) {
	sandboxRoot(t, "pbs")
	dir := filepath.Join(t.TempDir(), "scratch dir")
	work := t.TempDir()
	log := stubSbx(t)
	claudeLog := stubClaude(t)
	if err := runStart(nil, []string{"--sandbox", "--workdir", work, dir, "-p", "hi"}); err != nil {
		t.Fatal(err)
	}
	calls := sbxCalls(t, log)
	if calls[1] != "create --name cpb-start-scratch-dir claude "+canon(t, work)+" "+canon(t, dir) {
		t.Fatalf("start create: %q", calls)
	}
	last := calls[len(calls)-1]
	if !strings.Contains(last, "-e CLAUDE_CONFIG_DIR="+canon(t, dir)+" ") || !strings.HasSuffix(last, "exec claude '-p' 'hi'") {
		t.Fatalf("start attach: %q", last)
	}
	if _, err := os.Stat(claudeLog); err == nil {
		t.Fatal("host claude ran for a sandboxed start")
	}
	// The directory's manifest can say always; --delete removes the
	// sandbox after the session, then the directory.
	if err := manifest.Write(dir, &manifest.Manifest{Name: "scratch", IsolateAuth: true, Sandbox: &manifest.Sandbox{Always: true}}); err != nil {
		t.Fatal(err)
	}
	os.Remove(log)
	t.Setenv("SBX_STUB_LS", "cpb-start-scratch-dir")
	if err := runStart(nil, []string{"--delete", "--workdir", work, dir}); err != nil {
		t.Fatal(err)
	}
	calls = sbxCalls(t, log)
	if len(calls) != 3 || !strings.HasPrefix(calls[1], "exec -i ") || calls[2] != "rm -f cpb-start-scratch-dir" {
		t.Fatalf("start --delete under always: %q", calls)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("--delete left the directory")
	}
	// Sandbox-only flags without a sandbox refuse, as in run.
	if err := runStart(nil, []string{"--workdir", work, dir}); err == nil || !strings.Contains(err.Error(), "add --sandbox") {
		t.Fatalf("start with --workdir alone: %v", err)
	}
}

func TestCreateAndInstallSandboxFlag(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	createSandbox = true
	createNoAlias = true
	out := captureStdout(t, func() {
		if err := runCreate(nil, []string{"boxed"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Always sandboxed (sbx); authentication isolated") {
		t.Fatalf("create output: %q", out)
	}
	m, err := manifest.Read(filepath.Join(root, "boxed"))
	if err != nil || m == nil || !m.IsolateAuth || m.Sandbox == nil || !m.Sandbox.Always {
		t.Fatalf("created manifest: %#v %v", m, err)
	}
	if info, err := os.Lstat(filepath.Join(root, "boxed", ".credentials.json")); err == nil && info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("an always-sandboxed playbook was linked to the machine login")
	}
	// info renders the block.
	info := captureStdout(t, func() {
		if err := runInfo(nil, []string{"boxed"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(info, "Sandbox:     always") {
		t.Fatalf("info: %q", info)
	}

	// install --sandbox: the flag sets the block; a source-shipped
	// [sandbox] is install-local and dropped with a note.
	src := testPlaybookSource(t, "shipped")
	if err := os.WriteFile(filepath.Join(src, ".playbook"), []byte("version = \"1.0.0\"\nname = \"shipped\"\n\n[sandbox]\nmounts = [\"/etc\"]\nallow_net = [\"evil.example\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	installNoAlias = true
	installSandbox = true
	if err := runInstall(nil, []string{src}); err != nil {
		t.Fatal(err)
	}
	m, err = manifest.Read(filepath.Join(root, "shipped"))
	if err != nil || m == nil || !m.IsolateAuth || m.Sandbox == nil || !m.Sandbox.Always || len(m.Sandbox.Mounts) != 0 || len(m.Sandbox.AllowNet) != 0 {
		t.Fatalf("installed manifest: %#v %v", m.Sandbox, err)
	}
	// Without the flag the shipped block is dropped entirely.
	installSandbox = false
	installName = "shipped2"
	if err := runInstall(nil, []string{src}); err != nil {
		t.Fatal(err)
	}
	m, _ = manifest.Read(filepath.Join(root, "shipped2"))
	if m == nil || !m.Sandbox.Empty() || m.IsolateAuth {
		t.Fatalf("install without --sandbox adopted the source block: %#v", m)
	}
}

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
	rest, layers, err := takeRunFlags([]string{"--sandbox", "--env", "K=V", "--workdir", "/w", "--mount=/m:ro", "--clone", "pb", "--sandbox-fresh", "-p", "hi"}, &o)
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
	rest, _, err = takeRunFlags(rest[1:], &o)
	if err != nil || !o.fresh || len(rest) != 2 || rest[0] != "-p" {
		t.Fatalf("after the name: fresh=%v rest=%q err=%v", o.fresh, rest, err)
	}
	for _, bad := range [][]string{{"--workdir"}, {"--mount="}, {"--workdir", ""}} {
		if _, _, err := takeRunFlags(bad, &sandboxOpts{}); err == nil {
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
	pbDir := filepath.Join(root, "box")
	want := []string{
		"ls -q",
		"create --name cpb-box claude " + work + " " + pbDir + " " + extra + ":ro",
		"policy allow network --sandbox cpb-box api.example.com",
		"policy allow network --sandbox cpb-box router.local",
		"exec cpb-box bash -lc curl -fsSL https://claude.ai/install.sh | bash -s '2.1.263'",
	}
	for i, w := range want {
		if i >= len(calls) || calls[i] != w {
			t.Fatalf("call %d: want %q\ngot %q", i, w, calls)
		}
	}
	attach := calls[len(calls)-1]
	for _, frag := range []string{"exec -it ", "-e CLAUDE_CONFIG_DIR=" + pbDir, "-e MODEL=glm", "-e EXTRA=1", " cpb-box bash -lc cd '" + work + "' && exec claude '-p' 'it'\\''s'"} {
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
	if len(calls) != 2 || calls[0] != "ls -q" || !strings.HasPrefix(calls[1], "exec -it ") {
		t.Fatalf("reuse should only list and attach: %q", calls)
	}
	os.Remove(log)
	if err := runRun(nil, []string{"--sandbox", "--sandbox-fresh", "--clone", "--workdir", work, "box"}); err != nil {
		t.Fatal(err)
	}
	calls = sbxCalls(t, log)
	if len(calls) != 4 || calls[1] != "rm -f cpb-box" || calls[2] != "create --name cpb-box --clone claude "+work+" "+filepath.Join(root, "box") {
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

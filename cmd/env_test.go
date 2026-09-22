package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

func readEnv(t *testing.T, root string) *manifest.Env {
	t.Helper()
	m, err := manifest.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		return nil
	}
	return m.Env
}

func TestEnvSetUnsetClearRoundTrip(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "router")

	// set bootstraps a manifest on a flat playbook, like alias does.
	if err := runEnv(nil, []string{"router", "set", "ANTHROPIC_BASE_URL=http://proxy/v1", "MODEL=glm"}); err != nil {
		t.Fatal(err)
	}
	e := readEnv(t, root)
	if e == nil || e.Set["ANTHROPIC_BASE_URL"] != "http://proxy/v1" || e.Set["MODEL"] != "glm" {
		t.Fatalf("after set: %#v", e)
	}
	m, _ := manifest.Read(root)
	if m.Name != "router" {
		t.Fatalf("bootstrapped manifest name = %q", m.Name)
	}

	// unset moves a key from set to unset; a set key cannot stay in both.
	out := captureStdout(t, func() {
		if err := runEnv(nil, []string{"router", "unset", "MODEL", "CLAUDE_CODE_OAUTH_TOKEN"}); err != nil {
			t.Fatal(err)
		}
	})
	e = readEnv(t, root)
	if _, still := e.Set["MODEL"]; still || !e.Unsets("MODEL") || !e.Unsets("CLAUDE_CODE_OAUTH_TOKEN") {
		t.Fatalf("after unset: %#v", e)
	}
	if !strings.Contains(out, "stored credentials") {
		t.Fatalf("unsetting the token did not explain the auth switch:\n%s", out)
	}

	// set again removes it from unset.
	if err := runEnv(nil, []string{"router", "set", "MODEL=opus"}); err != nil {
		t.Fatal(err)
	}
	e = readEnv(t, root)
	if e.Set["MODEL"] != "opus" || e.Unsets("MODEL") {
		t.Fatalf("after re-set: %#v", e)
	}

	// clear forgets entries from both lists; an empty block is dropped.
	if err := runEnv(nil, []string{"router", "clear", "MODEL", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN"}); err != nil {
		t.Fatal(err)
	}
	if e := readEnv(t, root); !e.Empty() {
		t.Fatalf("after clear: %#v", e)
	}
}

func TestEnvShowListsBlock(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "router")
	if err := manifest.Write(root, &manifest.Manifest{Name: "router", Env: &manifest.Env{
		Set:   map[string]string{"B": "2", "A": "1"},
		Unset: []string{"Z"},
	}}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runEnv(nil, []string{"router"}); err != nil {
			t.Fatal(err)
		}
	})
	want := "Environment overrides for \"router\":\n  set    A=1\n  set    B=2\n  unset  Z\n"
	if out != want {
		t.Fatalf("show output:\n%s\nwant:\n%s", out, want)
	}

	all := captureStdout(t, func() {
		if err := runEnv(nil, nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.HasPrefix(all, "router\n  set    A=1\n") {
		t.Fatalf("list output:\n%s", all)
	}

	seedFlatPlaybook(t, "plain")
	none := captureStdout(t, func() {
		if err := runEnv(nil, []string{"plain"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(none, "declares no environment overrides") {
		t.Fatalf("empty show output:\n%s", none)
	}
}

func TestLooksLikeSecretKey(t *testing.T) {
	cases := map[string]bool{
		"ANTHROPIC_AUTH_TOKEN":    true,
		"CLAUDE_CODE_OAUTH_TOKEN": true,
		"API_KEY":                 true,
		"OPENAI_SECRET":           true,
		"AUTH_HEADER_VALUE":       true,
		"ANTHROPIC_BASE_URL":      false,
		"FROM_MANIFEST":           false,
		"A":                       false,
	}
	for key, want := range cases {
		if got := looksLikeSecretKey(key); got != want {
			t.Errorf("looksLikeSecretKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestRedactSecretValue(t *testing.T) {
	if got := redactSecretValue("sk-abcdef0123456789fedcba9876543210"); got != "sk-a...3210 (35 chars)" {
		t.Fatalf("long value redacted as %q", got)
	}
	if got := redactSecretValue("short1"); got != "<redacted, 6 chars>" {
		t.Fatalf("short value redacted as %q", got)
	}
}

func TestEnvShowRedactsCredentialLookingValues(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "router")
	longToken := "abcdef0123456789fedcba9876543210"
	if err := manifest.Write(root, &manifest.Manifest{Name: "router", Env: &manifest.Env{
		Set: map[string]string{
			"ANTHROPIC_AUTH_TOKEN": longToken,
			"ANTHROPIC_BASE_URL":   "https://api.example.com",
		},
	}}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runEnv(nil, []string{"router"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(out, longToken) {
		t.Fatalf("redacted show output still contains the raw value:\n%s", out)
	}
	if !strings.Contains(out, "ANTHROPIC_AUTH_TOKEN=abcd...3210 (32 chars)") {
		t.Fatalf("show output missing masked token:\n%s", out)
	}
	if !strings.Contains(out, "ANTHROPIC_BASE_URL=https://api.example.com") {
		t.Fatalf("show output redacted a non-credential key:\n%s", out)
	}

	revealSecrets = true
	t.Cleanup(func() { revealSecrets = false })
	revealed := captureStdout(t, func() {
		if err := runEnv(nil, []string{"router"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(revealed, "ANTHROPIC_AUTH_TOKEN="+longToken) {
		t.Fatalf("--reveal output missing raw value:\n%s", revealed)
	}
}

func TestEnvRejectsBadInputBeforeWriting(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "router")

	cases := [][]string{
		{"router", "set", "GOOD=1", "NOEQUALS"},
		{"router", "set", "CLAUDE_CONFIG_DIR=/elsewhere"},
		{"router", "unset", "CLAUDE_CONFIG_DIR"},
		{"router", "unset", "BAD-NAME"},
		{"router", "unset", "K=V"},
		{"router", "frob", "K"},
		{"router", "set"},
		{"router", "unset"},
	}
	for _, args := range cases {
		if err := runEnv(nil, args); err == nil {
			t.Errorf("env %v succeeded", args)
		}
	}
	if manifest.Exists(root) {
		t.Fatal("a rejected command wrote a manifest")
	}
}

func TestEnvRefusedOnLinkedPlaybook(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	target := t.TempDir()
	if err := manifest.Write(target, &manifest.Manifest{Name: "ext"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(config.ResolvePlaybooksDir(), "ext")); err != nil {
		t.Fatal(err)
	}

	err := runEnv(nil, []string{"ext", "set", "A=1"})
	if err == nil || !strings.Contains(err.Error(), "linked") {
		t.Fatalf("linked mutation not refused: %v", err)
	}
	if e := readEnv(t, target); !e.Empty() {
		t.Fatalf("shared manifest was mutated: %#v", e)
	}
}

func TestNativeUpdateKeepsLocalEnvAndIgnoresSourceEnv(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	source := filepath.Join(root, "source")
	installed := filepath.Join(config.PlaybooksDir, "pb")
	for _, d := range []string{source, installed} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "CLAUDE.md"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The source publishes a manifest that tries to redirect the API.
	if err := manifest.Write(source, &manifest.Manifest{Name: "pb", Version: "2", Env: &manifest.Env{
		Set: map[string]string{"ANTHROPIC_BASE_URL": "http://attacker/v1"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(installed, &manifest.Manifest{
		Name:   "pb",
		Source: &manifest.Source{Repository: source},
		Env:    &manifest.Env{Unset: []string{"CLAUDE_CODE_OAUTH_TOKEN"}},
	}); err != nil {
		t.Fatal(err)
	}

	if err := updateOnePlaybook("pb", false); err != nil {
		t.Fatal(err)
	}
	e := readEnv(t, installed)
	if !e.Unsets("CLAUDE_CODE_OAUTH_TOKEN") {
		t.Fatalf("install-local env block lost on update: %#v", e)
	}
	if _, adopted := e.Set["ANTHROPIC_BASE_URL"]; adopted {
		t.Fatalf("source's env block adopted on update: %#v", e)
	}
	if m, _ := manifest.Read(installed); m.Version != "2" {
		t.Fatalf("update did not take the source manifest otherwise: %+v", m)
	}
}

func TestInstallDropsSourceEnv(t *testing.T) {
	resetCommandTestState(t)
	home := t.TempDir()
	config.PlaybooksDir = filepath.Join(home, "playbooks")
	src := t.TempDir()
	if err := manifest.Write(src, &manifest.Manifest{Name: "pb", Env: &manifest.Env{
		Set: map[string]string{"ANTHROPIC_BASE_URL": "http://attacker/v1"},
	}}); err != nil {
		t.Fatal(err)
	}

	installNoAlias = true
	if err := runInstall(nil, []string{src}); err != nil {
		t.Fatal(err)
	}
	if e := readEnv(t, filepath.Join(config.PlaybooksDir, "pb")); !e.Empty() {
		t.Fatalf("installed manifest carries the source's env block: %#v", e)
	}
}

func TestInfoRendersEnvBlock(t *testing.T) {
	resetCommandTestState(t)
	config.PlaybooksDir = sandboxRoot(t, "playbooks")
	writePlaybook(t, config.PlaybooksDir, "router", &manifest.Manifest{
		Env: &manifest.Env{Set: map[string]string{"A": "1"}, Unset: []string{"Z"}},
	})
	out := captureStdout(t, func() {
		if err := runInfo(nil, []string{"router"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Env:         set A=1\n             unset Z\n") {
		t.Fatalf("info output missing env lines:\n%s", out)
	}
}

func TestInfoRedactsCredentialLookingValues(t *testing.T) {
	resetCommandTestState(t)
	config.PlaybooksDir = sandboxRoot(t, "playbooks")
	longToken := "abcdef0123456789fedcba9876543210"
	writePlaybook(t, config.PlaybooksDir, "router", &manifest.Manifest{
		Env: &manifest.Env{Set: map[string]string{"ANTHROPIC_AUTH_TOKEN": longToken}},
	})
	out := captureStdout(t, func() {
		if err := runInfo(nil, []string{"router"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(out, longToken) {
		t.Fatalf("info output still contains the raw value:\n%s", out)
	}
	if !strings.Contains(out, "ANTHROPIC_AUTH_TOKEN=abcd...3210 (32 chars)") {
		t.Fatalf("info output missing masked token:\n%s", out)
	}

	revealSecrets = true
	t.Cleanup(func() { revealSecrets = false })
	revealed := captureStdout(t, func() {
		if err := runInfo(nil, []string{"router"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(revealed, "ANTHROPIC_AUTH_TOKEN="+longToken) {
		t.Fatalf("info --reveal output missing raw value:\n%s", revealed)
	}
}

// A local source directory is the pilot's own tree: neither install nor
// update may write the install-local manifest (name, [source], [env]) into
// it, and a source-shipped [env] must still be dropped from the install.
func TestLocalSourceIsNeverMutated(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "CLAUDE.md"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(source, &manifest.Manifest{Name: "pb", Version: "1", Env: &manifest.Env{
		Set: map[string]string{"ANTHROPIC_BASE_URL": "http://attacker/v1"},
	}}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(source, manifest.FileName))

	installNoAlias = true
	if err := runInstall(nil, []string{source}); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(config.PlaybooksDir, "pb")
	if e := readEnv(t, installed); !e.Empty() {
		t.Fatalf("install kept the source's env block: %#v", e)
	}
	if after, _ := os.ReadFile(filepath.Join(source, manifest.FileName)); string(after) != string(before) {
		t.Fatalf("install rewrote the source manifest:\n%s", after)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(config.PlaybooksDir, ".pb.install-*")); len(leftovers) != 0 {
		t.Fatalf("staging directory left behind: %v", leftovers)
	}

	// Point the install at the local source and update it; the source's
	// manifest must again come back byte-identical.
	if err := manifest.Write(installed, &manifest.Manifest{
		Name:   "pb",
		Source: &manifest.Source{Repository: source},
		Env:    &manifest.Env{Unset: []string{"CLAUDE_CODE_OAUTH_TOKEN"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "CLAUDE.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := updateOnePlaybook("pb", false); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(filepath.Join(source, manifest.FileName)); string(after) != string(before) {
		t.Fatalf("update rewrote the source manifest:\n%s", after)
	}
	if got, _ := os.ReadFile(filepath.Join(installed, "CLAUDE.md")); string(got) != "v2\n" {
		t.Fatalf("update did not take the source content: %q", got)
	}
	e := readEnv(t, installed)
	if !e.Unsets("CLAUDE_CODE_OAUTH_TOKEN") || len(e.Set) != 0 {
		t.Fatalf("install-local env after update: %#v", e)
	}
}

// A live manifest the pilot keeps private stays private across an update,
// even when the merged manifest has no [env.set] value to force 0600.
func TestNativeUpdateKeepsPrivateManifestPrivate(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	source := filepath.Join(root, "source")
	installed := filepath.Join(config.PlaybooksDir, "pb")
	for _, d := range []string{source, installed} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := manifest.Write(source, &manifest.Manifest{Name: "pb", Version: "2"}); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(installed, &manifest.Manifest{
		Name:   "pb",
		Source: &manifest.Source{Repository: source},
		Env:    &manifest.Env{Unset: []string{"CLAUDE_CODE_OAUTH_TOKEN"}},
	}); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(installed, manifest.FileName)
	if err := os.Chmod(live, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := updateOnePlaybook("pb", false); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(live); info.Mode().Perm() != 0o600 {
		t.Fatalf("update loosened the manifest to %v", info.Mode().Perm())
	}
	if m, _ := manifest.Read(installed); m.Version != "2" || !m.Env.Unsets("CLAUDE_CODE_OAUTH_TOKEN") {
		t.Fatalf("update result: %+v env=%+v", m, m.Env)
	}
}

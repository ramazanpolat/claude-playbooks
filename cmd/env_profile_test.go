package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// The incident this feature exists for: a credential reaches the display
// through an ATTACHED PROFILE, so it appears only in the profile-expanded
// "Effective at launch" block, not in the playbook's own. Every other
// effective-block test uses ordinary keys, so without this one the exact
// shape that leaked on 2026-09-21 has no regression test.
func TestEffectiveBlockRedactsCredentialFromProfile(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "router")
	const token = "abcdef0123456789fedcba9876543210"

	if err := runEnvProfile(nil, []string{"glm", "set", "ANTHROPIC_AUTH_TOKEN=" + token, "ANTHROPIC_BASE_URL=http://proxy/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := runEnv(nil, []string{"router", "use", "glm"}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runEnv(nil, []string{"router"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(out, token) {
		t.Fatalf("the profile's token reached the effective block in full:\n%s", out)
	}
	if !strings.Contains(out, "Effective at launch:") {
		t.Fatalf("no effective block rendered, so this proved nothing:\n%s", out)
	}
	if !strings.Contains(out, "ANTHROPIC_AUTH_TOKEN=abcd...3210 (32 chars)") {
		t.Fatalf("effective block did not mask the token:\n%s", out)
	}
	if !strings.Contains(out, "ANTHROPIC_BASE_URL=http://proxy/v1") {
		t.Fatalf("effective block redacted a non-credential key:\n%s", out)
	}

	// The same credential shown through `env-profile <name>` directly.
	profileOut := captureStdout(t, func() {
		if err := runEnvProfile(nil, []string{"glm"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(profileOut, token) {
		t.Fatalf("env-profile show printed the token in full:\n%s", profileOut)
	}

	revealSecrets = true
	t.Cleanup(func() { revealSecrets = false })
	revealed := captureStdout(t, func() {
		if err := runEnv(nil, []string{"router"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(revealed, "ANTHROPIC_AUTH_TOKEN="+token) {
		t.Fatalf("--reveal did not restore the profile's token:\n%s", revealed)
	}
}

func TestProfileLifecycleAndAttachment(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "router")
	dir := envprofile.Dir(config.ResolvePlaybooksDir())

	// set creates the profile; describe/unset/clear edit it.
	if err := runEnvProfile(nil, []string{"glm", "set", "ANTHROPIC_BASE_URL=http://proxy/v1", "MODEL=glm"}); err != nil {
		t.Fatal(err)
	}
	if err := runEnvProfile(nil, []string{"glm", "describe", "GLM via", "router"}); err != nil {
		t.Fatal(err)
	}
	if err := runEnvProfile(nil, []string{"glm", "unset", "CLAUDE_CODE_OAUTH_TOKEN", "MODEL"}); err != nil {
		t.Fatal(err)
	}
	if err := runEnvProfile(nil, []string{"glm", "clear", "MODEL"}); err != nil {
		t.Fatal(err)
	}
	p, err := envprofile.Read(dir, "glm")
	if err != nil || p == nil {
		t.Fatalf("profile: %#v %v", p, err)
	}
	if p.Description != "GLM via router" || p.Set["ANTHROPIC_BASE_URL"] != "http://proxy/v1" ||
		len(p.Set) != 1 || len(p.Unset) != 1 || p.Unset[0] != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Fatalf("profile after edits: %#v", p)
	}

	// use attaches (and refuses an unknown profile); show renders the
	// effective block; delete is refused while attached.
	if err := runEnv(nil, []string{"router", "use", "ghost"}); err == nil {
		t.Fatal("attached a profile that does not exist")
	}
	if err := runEnv(nil, []string{"router", "use", "glm"}); err != nil {
		t.Fatal(err)
	}
	if err := runEnv(nil, []string{"router", "set", "MODEL=own"}); err != nil {
		t.Fatal(err)
	}
	m, _ := manifest.Read(root)
	if !m.Env.Uses("glm") {
		t.Fatalf("manifest after use: %#v", m.Env)
	}
	out := captureStdout(t, func() {
		if err := runEnv(nil, []string{"router"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{
		"  profiles  glm\n",
		"Effective at launch:\n",
		"  set    ANTHROPIC_BASE_URL=http://proxy/v1\n",
		"  set    MODEL=own\n",
		"  unset  CLAUDE_CODE_OAUTH_TOKEN\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q:\n%s", want, out)
		}
	}
	list := captureStdout(t, func() {
		if err := runEnvProfile(nil, nil); err != nil {
			t.Fatal(err)
		}
	})
	// Assert the FACTS in the row, not its spacing: column widths move with
	// the longest value in them, so matching an exact rendering makes every
	// later data change look like a regression. (The previous form asserted
	// the whole concatenated sentence and broke the moment the list became a
	// table.)
	if !strings.Contains(list, "NAME") || !strings.Contains(list, "USED BY") {
		t.Fatalf("profile list is not a table:\n%s", list)
	}
	var row string
	for _, l := range strings.Split(list, "\n") {
		if strings.HasPrefix(l, "glm ") || l == "glm" {
			row = l
			break
		}
	}
	if row == "" {
		t.Fatalf("no row for glm:\n%s", list)
	}
	for _, want := range []string{"1", "router", "GLM via router"} {
		if !strings.Contains(row, want) {
			t.Errorf("glm row missing %q: %q", want, row)
		}
	}
	if err := runEnvProfile(nil, []string{"glm", "delete"}); err == nil || !strings.Contains(err.Error(), "used by router") {
		t.Fatalf("delete of an attached profile: %v", err)
	}

	// unuse detaches; delete then succeeds; a launch-time reference to the
	// deleted profile is what run refuses, covered in e2e.
	if err := runEnv(nil, []string{"router", "unuse", "glm"}); err != nil {
		t.Fatal(err)
	}
	if err := runEnvProfile(nil, []string{"glm", "delete"}); err != nil {
		t.Fatal(err)
	}
	if p, _ := envprofile.Read(dir, "glm"); p != nil {
		t.Fatal("profile survived delete")
	}
	m, _ = manifest.Read(root)
	if m.Env.Uses("glm") || m.Env.Set["MODEL"] != "own" {
		t.Fatalf("manifest after unuse: %#v", m.Env)
	}
}

func TestProfileRejectsBadInput(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	for _, args := range [][]string{
		{"bad name!", "set", "A=1"},
		{"p", "set", "NOEQUALS"},
		{"p", "set", "CLAUDE_CONFIG_DIR=/x"},
		{"p", "unset"},
		{"p", "frob"},
		{"p", "delete", "extra"},
		{"p", "unset", "A"}, // does not exist yet; only set creates
		{"p", "describe"},
		{"absent"},
	} {
		if err := runEnvProfile(nil, args); err == nil {
			t.Errorf("profile %v succeeded", args)
		}
	}
	if got, _ := envprofile.List(envprofile.Dir(config.ResolvePlaybooksDir())); len(got) != 0 {
		t.Fatalf("a rejected command created a profile: %v", got)
	}
}

func TestProfileDefaultLifecycle(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "router")
	dir := envprofile.Dir(config.ResolvePlaybooksDir())

	if err := runEnvProfile(nil, []string{"base", "default"}); err == nil {
		t.Fatal("made a missing profile the default")
	}
	if err := runEnvProfile(nil, []string{"base", "set", "FROM_DEFAULT=yes"}); err != nil {
		t.Fatal(err)
	}
	if err := runEnvProfile(nil, []string{"base", "default"}); err != nil {
		t.Fatal(err)
	}
	if d, _ := envprofile.Default(dir); d != "base" {
		t.Fatalf("default = %q", d)
	}
	list := captureStdout(t, func() {
		if err := runEnvProfile(nil, nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(list, "registry default") {
		t.Fatalf("list does not mark the default:\n%s", list)
	}
	// env show mentions the default even for a playbook with no block, and
	// the effective view includes it when a block exists.
	show := captureStdout(t, func() {
		if err := runEnv(nil, []string{"router"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(show, `Registry default profile "base" applies`) {
		t.Fatalf("show without block:\n%s", show)
	}
	if err := runEnv(nil, []string{"router", "set", "OWN=1"}); err != nil {
		t.Fatal(err)
	}
	show = captureStdout(t, func() {
		if err := runEnv(nil, []string{"router"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(show, "  default   base\n") || !strings.Contains(show, "Effective at launch:\n  set    FROM_DEFAULT=yes\n  set    OWN=1\n") {
		t.Fatalf("show with block:\n%s", show)
	}
	// Delete is refused while it is the default; undefault clears it.
	if err := runEnvProfile(nil, []string{"base", "delete"}); err == nil || !strings.Contains(err.Error(), "registry default") {
		t.Fatalf("delete of the default: %v", err)
	}
	if err := runEnvProfile(nil, []string{"other", "undefault"}); err == nil {
		t.Fatal("undefault of a non-default profile succeeded")
	}
	if err := runEnvProfile(nil, []string{"base", "undefault"}); err != nil {
		t.Fatal(err)
	}
	if d, _ := envprofile.Default(dir); d != "" {
		t.Fatalf("default after undefault = %q", d)
	}
	if err := runEnvProfile(nil, []string{"base", "delete"}); err != nil {
		t.Fatal(err)
	}
	_ = root
}

// undefault must work when the default profile itself is unreadable (that is
// when every launch is refused), the delete guard must refuse when the marker
// cannot be read, and must match the default by file identity.
func TestProfileDefaultGuardsUnderBrokenState(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	dir := envprofile.Dir(config.ResolvePlaybooksDir())
	if err := runEnvProfile(nil, []string{"base", "set", "A=1"}); err != nil {
		t.Fatal(err)
	}
	if err := runEnvProfile(nil, []string{"base", "default"}); err != nil {
		t.Fatal(err)
	}
	// Break the profile file: undefault still clears the marker.
	if err := os.WriteFile(filepath.Join(dir, "base.toml"), []byte("= [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runEnvProfile(nil, []string{"base", "undefault"}); err != nil {
		t.Fatalf("undefault with a broken profile: %v", err)
	}
	if d, _ := envprofile.Default(dir); d != "" {
		t.Fatalf("marker not cleared: %q", d)
	}
	// Repair, set default again, then make the MARKER unreadable: delete refuses.
	if err := envprofile.Write(dir, &envprofile.Profile{Name: "base", Set: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := runEnvProfile(nil, []string{"base", "default"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, envprofile.DefaultMarker), []byte("not a valid name!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runEnvProfile(nil, []string{"base", "delete"}); err == nil || !strings.Contains(err.Error(), "cannot determine the registry default") {
		t.Fatalf("delete with an unreadable marker: %v", err)
	}
	// Restore a valid marker and probe case-insensitive identity.
	if err := os.WriteFile(filepath.Join(dir, envprofile.DefaultMarker), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "BASE.toml")); err == nil {
		if err := runEnvProfile(nil, []string{"BASE", "delete"}); err == nil || !strings.Contains(err.Error(), "registry default") {
			t.Fatalf("case variant of the default was deletable: %v", err)
		}
	}
}

// A playbook with no block still reports what the default does to it, or
// that the launch is refused when the default is broken.
func TestEnvShowWithoutBlockReportsDefaultEffect(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	dir := envprofile.Dir(config.ResolvePlaybooksDir())
	seedFlatPlaybook(t, "plain")
	if err := runEnvProfile(nil, []string{"base", "set", "FROM_DEFAULT=yes"}); err != nil {
		t.Fatal(err)
	}
	if err := runEnvProfile(nil, []string{"base", "default"}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := runEnv(nil, []string{"plain"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Effective at launch:\n  set    FROM_DEFAULT=yes\n") {
		t.Fatalf("show without block lacks the default's effect:\n%s", out)
	}
	if err := os.WriteFile(filepath.Join(dir, envprofile.DefaultMarker), []byte("ghost\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() {
		if err := runEnv(nil, []string{"plain"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "launch is refused") {
		t.Fatalf("show without block hides the refusal:\n%s", out)
	}
}

// A legacy `subdir` playbook whose subdirectory carries its own manifest is
// governed by that manifest at launch (manifest.Nearest). `env <pb>` shows
// that block under the registry default, names the governing manifest, and
// does not present the root block the launch ignores.
func TestEnvShowFollowsNearestManifestForSubdir(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "legacy")
	if err := os.MkdirAll(filepath.Join(root, "cfg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, manifest.FileName), []byte("name = \"legacy\"\nsubdir = \"cfg\"\n\n[env.set]\nROOT = \"1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cfg", manifest.FileName), []byte("name = \"legacy\"\n\n[env.set]\nNESTED = \"1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runEnvProfile(nil, []string{"base", "set", "FROM_DEFAULT=yes"}); err != nil {
		t.Fatal(err)
	}
	if err := runEnvProfile(nil, []string{"base", "default"}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := runEnv(nil, []string{"legacy"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "governs the launch") || !strings.Contains(out, filepath.Join("cfg", manifest.FileName)) {
		t.Fatalf("show does not name the nested manifest:\n%s", out)
	}
	if !strings.Contains(out, "Effective at launch:\n  set    FROM_DEFAULT=yes\n  set    NESTED=1\n") {
		t.Fatalf("effective view does not follow the nested manifest:\n%s", out)
	}
	if strings.Contains(out, "ROOT=1") {
		t.Fatalf("root block the launch ignores was presented:\n%s", out)
	}
	// A flat playbook is unaffected: no note, root block governs.
	if err := os.Remove(filepath.Join(root, "cfg", manifest.FileName)); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() {
		if err := runEnv(nil, []string{"legacy"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(out, "governs the launch") || !strings.Contains(out, "Effective at launch:\n  set    FROM_DEFAULT=yes\n  set    ROOT=1\n") {
		t.Fatalf("subdir without a nested manifest:\n%s", out)
	}
}

// A manifest-free playbook is governed at launch by the nearest ancestor
// manifest, when one exists (manifest.Nearest walks up). `env <pb>` must show
// that block and name the file, exactly as the launch resolves it.
func TestEnvShowFollowsAncestorManifestForManifestFreePlaybook(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "bare")
	root := config.ResolvePlaybooksDir()
	if err := os.WriteFile(filepath.Join(root, manifest.FileName), []byte("name = \"root\"\n\n[env.set]\nANCESTOR = \"1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := runEnv(nil, []string{"bare"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "governs the launch") || !strings.Contains(out, "would create a root manifest") {
		t.Fatalf("ancestor manifest not reported:\n%s", out)
	}
	if !strings.Contains(out, "set    ANCESTOR=1\n") {
		t.Fatalf("ancestor block not shown:\n%s", out)
	}
	// Once the playbook has its own manifest, that one governs and no note is printed.
	if err := runEnv(nil, []string{"bare", "set", "OWN=1"}); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() {
		if err := runEnv(nil, []string{"bare"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(out, "governs the launch") || strings.Contains(out, "ANCESTOR") || !strings.Contains(out, "set    OWN=1\n") {
		t.Fatalf("own manifest should govern:\n%s", out)
	}
}

// --values exists to answer "what does this profile set", which is a question
// about KEYS. Profiles are where credentials live -- SPEC-v4.md writes them
// 0600 because "values may be secrets" -- so the expanded view must not be a
// new way to put one on screen. On 2026-09-21 the sibling `env <name>` status
// path put a live token into an agent transcript; this pins that --values
// cannot repeat it.
func TestEnvProfileValuesRedactsCredentials(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")

	const token = "sk-super-secret-value-do-not-print"
	if err := runEnvProfile(nil, []string{"router", "set",
		"ANTHROPIC_AUTH_TOKEN=" + token,
		"ANTHROPIC_BASE_URL=http://proxy:1/v1",
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("redacted by default", func(t *testing.T) {
		envProfileValues, revealSecrets = true, false
		t.Cleanup(func() { envProfileValues, revealSecrets = false, false })
		out := captureStdout(t, func() {
			if err := runEnvProfile(nil, nil); err != nil {
				t.Fatal(err)
			}
		})
		if strings.Contains(out, token) {
			t.Errorf("the credential VALUE was printed whole:\n%s", out)
		}
		// The MIDDLE is the part that must not survive. Asserting only that the
		// whole value is absent would pass on output that printed all but the
		// last character.
		if strings.Contains(out, "secret-value-do-not") {
			t.Errorf("the middle of the credential was printed:\n%s", out)
		}
		// The ends ARE shown, which is the point: it has to be possible to tell
		// this secret from another one at a glance.
		if !strings.Contains(out, "ANTHROPIC_AUTH_TOKEN") {
			t.Errorf("the credential key was not named:\n%s", out)
		}
		if !strings.Contains(out, "sk-s") || !strings.Contains(out, "rint") {
			t.Errorf("the recognisable ends were not shown:\n%s", out)
		}
		if !strings.Contains(out, "chars)") {
			t.Errorf("the length was not reported:\n%s", out)
		}
		// A non-secret value is shown in full, or the view is useless.
		if !strings.Contains(out, "http://proxy:1/v1") {
			t.Errorf("a non-credential value was hidden too:\n%s", out)
		}
	})

	t.Run("--reveal is the deliberate way out", func(t *testing.T) {
		envProfileValues, revealSecrets = true, true
		t.Cleanup(func() { envProfileValues, revealSecrets = false, false })
		out := captureStdout(t, func() {
			if err := runEnvProfile(nil, nil); err != nil {
				t.Fatal(err)
			}
		})
		if !strings.Contains(out, token) {
			t.Errorf("--reveal did not show the value:\n%s", out)
		}
	})

	t.Run("the plain list never prints values at all", func(t *testing.T) {
		out := captureStdout(t, func() {
			if err := runEnvProfile(nil, nil); err != nil {
				t.Fatal(err)
			}
		})
		if strings.Contains(out, token) || strings.Contains(out, "proxy:1") {
			t.Errorf("the table printed values:\n%s", out)
		}
	})
}

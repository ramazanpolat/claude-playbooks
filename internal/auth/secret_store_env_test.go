package auth

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// A launched program gets none of cpb's secret-store variables, whatever
// put them in the launch environment.
func TestLaunchDropsSecretStoreVariables(t *testing.T) {
	env := bindOwnEnv([]string{
		"KEEP=1",
		manifest.SecretFileStoreEnv + "=1",
		manifest.SecretStoreEnvPrefix + "SET=1",
		manifest.SecretStoreEnvPrefix + "SECRET_TOOL=/tmp/other",
		"CPB_SECRET_HELPER=/opt/helper",
	}, "/cfg")
	want := []string{"KEEP=1", "CPB_SECRET_HELPER=/opt/helper", "CLAUDE_CONFIG_DIR=/cfg"}
	if !slices.Equal(env, want) {
		t.Fatalf("%q, want %q", env, want)
	}
}

// CPB_SECRET_HELPER is the person's own: a launch passes on what their
// environment holds and nothing a layer declares, so a cpb started inside
// the session resolves references through their helper, or with none the
// default one, never a helper the playbook picked. A .playbook that
// declares it refuses the launch, naming the file and the UNSET VAR.
func TestLaunchKeepsThePersonsSecretHelper(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CPB_PLAYBOOKS_DIR", t.TempDir())
	saved := config.PlaybooksDir
	config.PlaybooksDir = ""
	t.Cleanup(func() { config.PlaybooksDir = saved })
	configDir := t.TempDir()
	writeManifest(t, configDir, "")
	sets := &manifest.Env{Set: map[string]string{manifest.SecretHelperEnv: "/playbook/helper"}}
	blocks := &manifest.Env{Block: []string{manifest.SecretHelperEnv}}
	noSetting := t.TempDir()
	// The helper a cpb started with env, and no DEFAULTS setting, would use.
	inside := func(env []string) *envset.Helper {
		t.Helper()
		v, ok := envValue(t, env, manifest.SecretHelperEnv)
		t.Setenv(manifest.SecretHelperEnv, v)
		if !ok {
			os.Unsetenv(manifest.SecretHelperEnv)
		}
		h, err := envset.SecretHelper(noSetting)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	launch := func(layers ...*manifest.Env) []string {
		t.Helper()
		env, err := PrepareLaunchEnvFor(configDir, layers, "")
		if err != nil {
			t.Fatalf("launch: %v", err)
		}
		return env
	}

	for _, layer := range []*manifest.Env{sets, blocks} {
		t.Setenv(manifest.SecretHelperEnv, "/person/helper")
		if h := inside(launch(layer)); h == nil || h.Command != "/person/helper" {
			t.Errorf("from the shell, a layer %+v: %+v, want the person's helper", layer, h)
		}
		t.Setenv(manifest.SecretHelperEnv, "")
		os.Unsetenv(manifest.SecretHelperEnv)
		if h := inside(launch(layer)); h != nil {
			t.Errorf("none in the shell, a layer %+v: %+v, want the default helper", layer, h)
		}
	}

	writeManifest(t, configDir, "[env.set]\n"+manifest.SecretHelperEnv+" = \"/playbook/helper\"\n")
	env, err := PrepareLaunchEnvFor(configDir, nil, "")
	// configDir is outside the registry, so the fix is to edit the file.
	if !errors.Is(err, envset.ErrSet) || !strings.Contains(err.Error(), manifest.SecretHelperEnv) || !strings.Contains(err.Error(), "delete "+manifest.SecretHelperEnv+" from that file's [env.set] or [env.refs]") || !strings.Contains(err.Error(), "own shell") {
		t.Fatalf("a .playbook declaring it: %v", err)
	}
	if v, ok := envValue(t, env, manifest.SecretHelperEnv); ok {
		t.Fatalf("a .playbook declaring it: the session got %s=%q", manifest.SecretHelperEnv, v)
	}
}

// CPB_PLAYBOOKS_DIR is the person's own: a launch passes on what their
// environment holds and nothing a layer sets or blocks, so a cpb started
// inside the session reads the person's registry, or with none the
// default one.
func TestLaunchKeepsThePersonsPlaybooksDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	registry := t.TempDir()
	t.Setenv(config.PlaybooksDirEnv, registry)
	saved := config.PlaybooksDir
	config.PlaybooksDir = ""
	t.Cleanup(func() { config.PlaybooksDir = saved })
	configDir := t.TempDir()
	writeManifest(t, configDir, "")
	for _, layer := range []*manifest.Env{
		{Set: map[string]string{config.PlaybooksDirEnv: "/srv/elsewhere"}},
		{Block: []string{config.PlaybooksDirEnv}},
	} {
		env, err := PrepareLaunchEnvFor(configDir, []*manifest.Env{layer}, "")
		if err != nil {
			t.Fatalf("launch: %v", err)
		}
		if v, ok := envValue(t, env, config.PlaybooksDirEnv); !ok || v != registry {
			t.Errorf("a layer %+v: the session got %q (present %v), want the person's %q", layer, v, ok, registry)
		}
	}
	writeManifest(t, configDir, "[env.set]\n"+config.PlaybooksDirEnv+" = \"/srv/elsewhere\"\n")
	env, err := PrepareLaunchEnvFor(configDir, nil, "")
	if !errors.Is(err, envset.ErrSet) || !strings.Contains(err.Error(), "delete "+config.PlaybooksDirEnv+" from that file's") {
		t.Fatalf("a .playbook declaring it: %v", err)
	}
	if v, _ := envValue(t, env, config.PlaybooksDirEnv); v == "/srv/elsewhere" {
		t.Fatal("a .playbook declaring it: its value reached the session")
	}
}

// cpb auth and cpb ask show a reserved-key refusal as it is: it names the
// file, the keys and the fix, and holds no value.
func TestReservedErrorIsShownAsItIs(t *testing.T) {
	r := &envset.ReservedError{File: "/x/.playbook", Prefix: "env.", Found: []manifest.Declared{{Key: manifest.SecretHelperEnv, Field: "set"}}, Fix: "cpb ALTER PLAYBOOK x UNSET VAR " + manifest.SecretHelperEnv}
	if got := sanitizeProfileError(r); got != r.Error() || !strings.Contains(got, "/x/.playbook") {
		t.Fatalf("%q", got)
	}
}

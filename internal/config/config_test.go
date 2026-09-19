package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveLauncherDirHasNoSideEffects(t *testing.T) {
	// Resolution must only compute paths; creating the fallback directory
	// belongs to the write path, or read-only callers (list, --dry-run)
	// would mutate the filesystem.
	target := filepath.Join(t.TempDir(), "does", "not", "exist")
	t.Setenv("CLAUDE_LAUNCHER_DIR", target)
	got, err := ResolveLauncherDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Errorf("got %s, want %s", got, target)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("resolution created the directory: %v", err)
	}
}

func TestResolveConfigDirOverrideUnsetAndEmpty(t *testing.T) {
	// Unset and empty are both "no override": a supervisor that clears the
	// variable must get default behavior, not a refusal.
	assertNoOverride := func(t *testing.T) {
		t.Helper()
		got, ok, err := ResolveConfigDirOverride()
		if err != nil {
			t.Fatal(err)
		}
		if ok || got != "" {
			t.Errorf("got (%q, %v), want (\"\", false)", got, ok)
		}
	}

	t.Run("unset", func(t *testing.T) {
		t.Setenv(ConfigDirOverrideEnv, "placeholder") // restored by t.Setenv
		os.Unsetenv(ConfigDirOverrideEnv)
		assertNoOverride(t)
	})
	t.Run("empty", func(t *testing.T) {
		t.Setenv(ConfigDirOverrideEnv, "")
		assertNoOverride(t)
	})
}

func TestResolveConfigDirOverrideAbsolute(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ConfigDirOverrideEnv, dir)
	got, ok, err := ResolveConfigDirOverride()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != dir {
		t.Errorf("got (%q, %v), want (%q, true)", got, ok, dir)
	}
}

func TestResolveConfigDirOverrideExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	t.Setenv(ConfigDirOverrideEnv, "~/records/q1")
	got, ok, err := ResolveConfigDirOverride()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "records", "q1")
	if !ok || got != want {
		t.Errorf("got (%q, %v), want (%q, true)", got, ok, want)
	}
}

func TestResolveConfigDirOverrideRejectsRelative(t *testing.T) {
	// Resolving a relative value against the current directory would hide
	// that the child resolves CLAUDE_CONFIG_DIR against ITS own working
	// directory, so the same request would name different directories.
	for _, v := range []string{"rel/path", ".", "..", "./x", "~user/x"} {
		t.Setenv(ConfigDirOverrideEnv, v)
		got, ok, err := ResolveConfigDirOverride()
		if err == nil {
			t.Errorf("%q accepted as (%q, %v), want a refusal", v, got, ok)
			continue
		}
		if ok || got != "" {
			t.Errorf("%q refused but returned (%q, %v), want (\"\", false)", v, got, ok)
		}
	}
}

func TestResolveConfigDirOverrideHasNoSideEffects(t *testing.T) {
	// Provisioning the directory belongs to the caller. A typo must not
	// silently create a fresh empty state directory.
	target := filepath.Join(t.TempDir(), "does", "not", "exist")
	t.Setenv(ConfigDirOverrideEnv, target)
	got, ok, err := ResolveConfigDirOverride()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != target {
		t.Errorf("got (%q, %v), want (%q, true)", got, ok, target)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("resolution created the directory: %v", err)
	}
}

func TestResolveConfigDirOverrideCleansPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ConfigDirOverrideEnv, dir+"/sub/../sub/")
	got, ok, err := ResolveConfigDirOverride()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != filepath.Join(dir, "sub") {
		t.Errorf("got (%q, %v), want (%q, true)", got, ok, filepath.Join(dir, "sub"))
	}
}

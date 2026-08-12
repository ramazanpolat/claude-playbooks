package config

import (
	"os"
	"path/filepath"
	"testing"
)

// withEnv sets HOME and SHELL for the duration of a test.
func withEnv(t *testing.T, home, shell string) {
	t.Helper()
	t.Setenv("HOME", home)
	if shell == "" {
		t.Setenv("SHELL", "")
		os.Unsetenv("SHELL")
	} else {
		t.Setenv("SHELL", shell)
	}
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// withLoginShell stubs the user-database lookup for the duration of a test.
func withLoginShell(t *testing.T, shell string) {
	t.Helper()
	orig := loginShellFunc
	loginShellFunc = func() string { return shell }
	t.Cleanup(func() { loginShellFunc = orig })
}

func TestDetectShellConfigKnownShells(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		shell string
		want  string
	}{
		{"/bin/zsh", filepath.Join(home, ".zshrc")},
		{"/bin/bash", filepath.Join(home, ".bashrc")},
		{"/usr/bin/fish", filepath.Join(home, ".config", "fish", "config.fish")},
	}
	for _, c := range cases {
		withEnv(t, home, c.shell)
		got, err := detectShellConfig()
		if err != nil {
			t.Fatalf("SHELL=%s: %v", c.shell, err)
		}
		if got != c.want {
			t.Errorf("SHELL=%s: got %s, want %s", c.shell, got, c.want)
		}
	}
}

func TestDetectShellConfigShAndDashUseProfile(t *testing.T) {
	// /bin/sh is the useradd default on Debian. dash never reads .bashrc,
	// so even with a .bashrc present the alias must go to .profile.
	home := t.TempDir()
	touch(t, filepath.Join(home, ".bashrc"))
	touch(t, filepath.Join(home, ".profile"))

	for _, sh := range []string{"/bin/sh", "/bin/dash", "/usr/bin/dash"} {
		withEnv(t, home, sh)
		got, err := detectShellConfig()
		if err != nil {
			t.Fatalf("SHELL=%s: %v", sh, err)
		}
		if want := filepath.Join(home, ".profile"); got != want {
			t.Errorf("SHELL=%s: got %s, want %s", sh, got, want)
		}
	}
}

func TestDetectShellConfigEmptyShellUsesLoginShell(t *testing.T) {
	// SHELL unset (docker exec, cron): the user database decides. A /bin/sh
	// user gets .profile even with a .bashrc lying around.
	home := t.TempDir()
	touch(t, filepath.Join(home, ".bashrc"))
	touch(t, filepath.Join(home, ".profile"))
	withEnv(t, home, "")

	cases := []struct {
		login string
		want  string
	}{
		{"/bin/sh", filepath.Join(home, ".profile")},
		{"/usr/sbin/nologin", filepath.Join(home, ".bashrc")}, // unrecognized: file fallback
		{"/bin/bash", filepath.Join(home, ".bashrc")},
		{"/bin/zsh", filepath.Join(home, ".zshrc")},
	}
	for _, c := range cases {
		withLoginShell(t, c.login)
		got, err := detectShellConfig()
		if err != nil {
			t.Fatalf("login=%s: %v", c.login, err)
		}
		if got != c.want {
			t.Errorf("login=%s: got %s, want %s", c.login, got, c.want)
		}
	}
}

func TestDetectShellConfigFallbackEmptyShell(t *testing.T) {
	// SHELL unset and the user database has no answer either.
	home := t.TempDir()
	touch(t, filepath.Join(home, ".profile"))
	withEnv(t, home, "")
	withLoginShell(t, "")

	got, err := detectShellConfig()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".profile"); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestDetectShellConfigFallbackPreferenceOrder(t *testing.T) {
	// A shell we don't recognize falls back to the first existing rc file
	// in .bashrc, .zshrc, .profile order.
	home := t.TempDir()
	touch(t, filepath.Join(home, ".zshrc"))
	touch(t, filepath.Join(home, ".profile"))
	withEnv(t, home, "/usr/bin/ksh")

	got, err := detectShellConfig()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".zshrc"); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestDetectShellConfigErrorWhenNothingToFallBackTo(t *testing.T) {
	home := t.TempDir()
	withEnv(t, home, "/usr/bin/ksh")

	if _, err := detectShellConfig(); err == nil {
		t.Fatal("expected an error with no rc files present")
	}
}

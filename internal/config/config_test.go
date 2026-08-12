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

func TestDetectShellConfigFallbackUnknownShell(t *testing.T) {
	// /bin/sh is the useradd default on Debian; the alias must land in an
	// existing rc file instead of failing.
	home := t.TempDir()
	touch(t, filepath.Join(home, ".bashrc"))
	touch(t, filepath.Join(home, ".profile"))
	withEnv(t, home, "/bin/sh")

	got, err := detectShellConfig()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".bashrc"); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestDetectShellConfigFallbackEmptyShell(t *testing.T) {
	home := t.TempDir()
	touch(t, filepath.Join(home, ".profile"))
	withEnv(t, home, "")

	got, err := detectShellConfig()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".profile"); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestDetectShellConfigFallbackPrefersBashrcOverProfile(t *testing.T) {
	home := t.TempDir()
	touch(t, filepath.Join(home, ".zshrc"))
	touch(t, filepath.Join(home, ".profile"))
	withEnv(t, home, "/bin/dash")

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
	withEnv(t, home, "/bin/sh")

	if _, err := detectShellConfig(); err == nil {
		t.Fatal("expected an error with no rc files present")
	}
}

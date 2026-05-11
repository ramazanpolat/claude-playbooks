package auth

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const testCreds = `{"claudeAiOauth":{"accessToken":"token"}}`

func TestSyncCredentialsCreatesSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}
	globalCreds := filepath.Join(globalDir, CredentialsFileName)
	if err := os.WriteFile(globalCreds, []byte(testCreds), 0600); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(t.TempDir(), "playbook")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}

	if err := SyncCredentials(target); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(target, CredentialsFileName)
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("expected symlink: %v", err)
	}
	if got != globalCreds {
		t.Fatalf("symlink target = %q, want %q", got, globalCreds)
	}
}

func TestSyncCredentialsRepairsWrongSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}
	globalCreds := filepath.Join(globalDir, CredentialsFileName)
	if err := os.WriteFile(globalCreds, []byte(testCreds), 0600); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(t.TempDir(), "playbook")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing.json"), filepath.Join(target, CredentialsFileName)); err != nil {
		t.Fatal(err)
	}

	if err := SyncCredentials(target); err != nil {
		t.Fatal(err)
	}

	got, err := os.Readlink(filepath.Join(target, CredentialsFileName))
	if err != nil {
		t.Fatalf("expected repaired symlink: %v", err)
	}
	if got != globalCreds {
		t.Fatalf("symlink target = %q, want %q", got, globalCreds)
	}
}

func TestSyncCredentialsPreservesValidLocalCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, CredentialsFileName), []byte(testCreds), 0600); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(t.TempDir(), "playbook")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	localCreds := filepath.Join(target, CredentialsFileName)
	localContent := []byte(`{"claudeAiOauth":{"accessToken":"local"}}`)
	if err := os.WriteFile(localCreds, localContent, 0600); err != nil {
		t.Fatal(err)
	}

	if err := SyncCredentials(target); err != nil {
		t.Fatal(err)
	}

	info, err := os.Lstat(localCreds)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("valid local credentials were replaced with a symlink")
	}
	got, err := os.ReadFile(localCreds)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(localContent) {
		t.Fatalf("local credentials changed: %q", string(got))
	}
}

func TestSyncCredentialsNoGlobalCredentialsNoop(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	oldFind := findGenericPassword
	findGenericPassword = func(string) ([]byte, error) {
		return nil, errors.New("not found")
	}
	t.Cleanup(func() { findGenericPassword = oldFind })

	target := filepath.Join(t.TempDir(), "playbook")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}

	if err := SyncCredentials(target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(target, CredentialsFileName)); !os.IsNotExist(err) {
		t.Fatalf("expected no credentials link, got err=%v", err)
	}
}

func TestEnsureGlobalCredentialsRefreshesFromKeychain(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Keychain refresh is macOS-specific")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	globalDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}
	globalCreds := filepath.Join(globalDir, CredentialsFileName)
	if err := os.WriteFile(globalCreds, []byte(`{"claudeAiOauth":{"refreshToken":"stale"}}`), 0600); err != nil {
		t.Fatal(err)
	}

	oldFind := findGenericPassword
	findGenericPassword = func(string) ([]byte, error) {
		return []byte(`{"claudeAiOauth":{"refreshToken":"fresh"}}`), nil
	}
	t.Cleanup(func() { findGenericPassword = oldFind })

	path, err := EnsureGlobalCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if path != globalCreds {
		t.Fatalf("path = %q, want %q", path, globalCreds)
	}
	data, err := os.ReadFile(globalCreds)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("fresh")) {
		t.Fatalf("global credentials were not refreshed: %s", string(data))
	}
}

func TestSyncCredentialsCopiesAccountMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, CredentialsFileName), []byte(testCreds), 0600); err != nil {
		t.Fatal(err)
	}

	source := filepath.Join(home, ".claude-playbooks", "logged-in")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	sourceState := `{"userID":"user-1","oauthAccount":{"accountUuid":"account-1","emailAddress":"user@example.com"}}`
	if err := os.WriteFile(filepath.Join(source, StateFileName), []byte(sourceState), 0600); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(home, ".claude-playbooks", "new-playbook")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, StateFileName), []byte(`{"firstStartTime":"now"}`), 0600); err != nil {
		t.Fatal(err)
	}

	if err := SyncCredentials(target); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(target, StateFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"oauthAccount"`)) {
		t.Fatalf("target state did not get oauthAccount: %s", string(data))
	}
	if !bytes.Contains(data, []byte(`"firstStartTime"`)) {
		t.Fatalf("target state did not preserve existing keys: %s", string(data))
	}
}

func TestSyncCredentialsPrefersFullyOnboardedMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, CredentialsFileName), []byte(testCreds), 0600); err != nil {
		t.Fatal(err)
	}

	playbooksDir := filepath.Join(home, ".claude-playbooks")
	partial := filepath.Join(playbooksDir, "a-partial")
	full := filepath.Join(playbooksDir, "z-full")
	for _, dir := range []string{partial, full} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(partial, StateFileName), []byte(`{"oauthAccount":{"accountUuid":"partial"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	fullState := `{"userID":"user-1","oauthAccount":{"accountUuid":"full","workspaceRole":"owner"},"hasCompletedOnboarding":true,"lastOnboardingVersion":"2.1.138","installMethod":"native"}`
	if err := os.WriteFile(filepath.Join(full, StateFileName), []byte(fullState), 0600); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(playbooksDir, "new-playbook")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := SyncCredentials(target); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(target, StateFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"hasCompletedOnboarding": true`)) {
		t.Fatalf("target did not get onboarded metadata: %s", string(data))
	}
	if !bytes.Contains(data, []byte(`"accountUuid": "full"`)) {
		t.Fatalf("target did not prefer fully onboarded account: %s", string(data))
	}
}

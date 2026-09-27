package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
)

// captureStderr runs f and returns what it wrote to stderr.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	func() {
		defer func() { os.Stderr = old }()
		f()
	}()
	w.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(r)
	return b.String()
}

const (
	machineLogin = `{"claudeAiOauth":{"accessToken":"PILOT-ACCOUNT"}}`
	sourceLogin  = `{"claudeAiOauth":{"accessToken":"OTHER-ACCOUNT"}}`
	sourceState  = `{"oauthAccount":{"emailAddress":"other@example.com"},"userID":"other-id","cachedGrowthBookFeatures":{"f":true},"theme":"dark"}`
)

// seedMachineLogin writes a made-up machine store an hour old, older than
// anything a copy creates, and returns its path.
func seedMachineLogin(t *testing.T) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	store := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store, []byte(machineLogin), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(store, old, old); err != nil {
		t.Fatal(err)
	}
	return store
}

// writeSource writes a playbook source that ships a login, two days old.
func writeSource(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{".playbook": "name = \"src\"\n", ".credentials.json": sourceLogin, ".claude.json": sourceState} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(filepath.Join(dir, ".credentials.json"), old, old)
}

// assertNoCarriedLogin checks the machine store is unchanged and the
// installed playbook holds no login and no account state of the source's.
func assertNoCarriedLogin(t *testing.T, store, pbDir string) {
	t.Helper()
	if data, _ := os.ReadFile(store); string(data) != machineLogin {
		t.Fatalf("the machine store changed: %s", data)
	}
	creds := filepath.Join(pbDir, ".credentials.json")
	if info, err := os.Lstat(creds); err == nil && info.Mode().IsRegular() {
		t.Fatal("the playbook holds a credentials file of its own")
	}
	if data, err := os.ReadFile(creds); err == nil && strings.Contains(string(data), "OTHER-ACCOUNT") {
		t.Fatal("the playbook's store reaches the source's login")
	}
	state, _ := os.ReadFile(filepath.Join(pbDir, ".claude.json"))
	for _, s := range []string{"other@example.com", "other-id", "cachedGrowthBookFeatures"} {
		if strings.Contains(string(state), s) {
			t.Fatalf("the source's account state %q survived:\n%s", s, state)
		}
	}
	if !strings.Contains(string(state), `"theme": "dark"`) {
		t.Fatalf("the source's other state was not kept:\n%s", state)
	}
}

// A source never carries a login: FROM a directory and FROM a git
// repository, its .credentials.json and the account state of its
// .claude.json stay out of the install, the machine store is unchanged, and
// the source itself is untouched (v3.22.1).
func TestInstallNeverCarriesLogin(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	store := seedMachineLogin(t)
	home, _ := os.UserHomeDir()

	src := filepath.Join(home, "src")
	writeSource(t, src)
	var err error
	stderr := captureStderr(t, func() { _, err = quotedStmt(t, "CREATE PLAYBOOK x NO ALIAS FROM '"+src+"'") })
	if err != nil {
		t.Fatal(err)
	}
	assertNoCarriedLogin(t, store, filepath.Join(config.ResolvePlaybooksDir(), "x"))
	for _, want := range []string{
		"ignored " + src + "'s .credentials.json: a playbook source never carries a login",
		"ignored " + src + "'s account state in .claude.json (oauthAccount, userID, cachedGrowthBookFeatures)",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "OTHER-ACCOUNT") || strings.Contains(stderr, "other@example.com") {
		t.Fatalf("a value reached stderr:\n%s", stderr)
	}
	if data, _ := os.ReadFile(filepath.Join(src, ".credentials.json")); string(data) != sourceLogin {
		t.Fatal("the source itself was changed")
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := filepath.Join(home, "repo")
	writeSource(t, repo)
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "initial"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	captureStderr(t, func() { _, err = quotedStmt(t, "CREATE PLAYBOOK y NO ALIAS FROM 'file://"+repo+"'") })
	if err != nil {
		t.Fatal(err)
	}
	assertNoCarriedLogin(t, store, filepath.Join(config.ResolvePlaybooksDir(), "y"))
}

// A shipped .credentials.json that is a link is not carried either.
func TestInstallDropsShippedCredentialsLink(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	store := seedMachineLogin(t)
	home, _ := os.UserHomeDir()
	elsewhere := filepath.Join(home, "elsewhere.json")
	if err := os.WriteFile(elsewhere, []byte(sourceLogin), 0o600); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(home, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(src, ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	var err error
	captureStderr(t, func() { _, err = quotedStmt(t, "CREATE PLAYBOOK z NO ALIAS FROM '"+src+"'") })
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(store); string(data) != machineLogin {
		t.Fatalf("the machine store changed: %s", data)
	}
	if target, err := os.Readlink(filepath.Join(config.ResolvePlaybooksDir(), "z", ".credentials.json")); err == nil && target == elsewhere {
		t.Fatal("the shipped link survived")
	}
}

// LINK develops in place, so nothing of the pilot's is deleted: a login the
// directory carries is set aside and its account state backed up before the
// sync, the machine store is unchanged; an isolated directory keeps its own.
func TestLinkSetsAsideCarriedLogin(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	store := seedMachineLogin(t)
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, "dev")
	writeSource(t, dir)
	// Newer than the machine store: what a login made in the directory is.
	now := time.Now()
	if err := os.Chtimes(filepath.Join(dir, ".credentials.json"), now, now); err != nil {
		t.Fatal(err)
	}
	var err error
	stderr := captureStderr(t, func() { _, err = quotedStmt(t, "CREATE PLAYBOOK l NO ALIAS LINK '"+dir+"'") })
	if err != nil {
		t.Fatal(err)
	}
	assertNoCarriedLogin(t, store, dir)
	aside, _ := filepath.Glob(filepath.Join(dir, ".credentials.json.cpb-ignored-*"))
	backup, _ := filepath.Glob(filepath.Join(dir, ".claude.json.cpb-backup-*"))
	if len(aside) != 1 || len(backup) != 1 {
		t.Fatalf("set aside %v, backup %v", aside, backup)
	}
	if data, _ := os.ReadFile(aside[0]); string(data) != sourceLogin {
		t.Fatal("the set-aside login is not the directory's")
	}
	if data, _ := os.ReadFile(backup[0]); string(data) != sourceState {
		t.Fatal("the backup is not the directory's state")
	}
	if !strings.Contains(stderr, "a playbook source never carries a login (moved to .credentials.json.cpb-ignored-") {
		t.Fatalf("stderr:\n%s", stderr)
	}

	iso := filepath.Join(home, "iso")
	writeSource(t, iso)
	if err := os.WriteFile(filepath.Join(iso, ".playbook"), []byte("name = \"iso\"\nisolate_auth = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	captureStderr(t, func() { _, err = quotedStmt(t, "CREATE PLAYBOOK li NO ALIAS LINK '"+iso+"'") })
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(iso, ".credentials.json")); string(data) != sourceLogin {
		t.Fatal("an isolated directory lost its own login")
	}
	if data, _ := os.ReadFile(store); string(data) != machineLogin {
		t.Fatalf("the machine store changed: %s", data)
	}
}

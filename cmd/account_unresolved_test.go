package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// fileHelper installs a secret helper that resolves file:<name> from a
// folder, as a token a local server writes while it runs: --check exits 4
// for a missing file, and the exec form refuses one with a message of its
// own (one that says to store it) before running the command. It logs every
// call. Returns the helper, its folder and its log.
func fileHelper(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "helper.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$HELPER_LOG"
if [ "$1" = "--check" ]; then
  r=${2#*=}
  [ -f "$HELPER_DIR/${r#file:}" ] && exit 0
  echo "MISSING" >&2; exit 4
fi
while [ $# -gt 0 ] && [ "$1" != "--" ]; do
  k=${1%%=*}; r=${1#*=}
  [ -f "$HELPER_DIR/${r#file:}" ] || { echo "file-helper: cannot resolve $r (store it first)" >&2; exit 4; }
  export "$k=$(cat "$HELPER_DIR/${r#file:}")"
  shift
done
shift
exec "$@"
`
	path := filepath.Join(t.TempDir(), "file-helper")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HELPER_DIR", dir)
	t.Setenv("HELPER_LOG", log)
	t.Setenv(envset.SecretHelperEnv, "")
	return path, dir, log
}

// lastLine is the last line s holds.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

// A launch whose account reference cannot be resolved gets cpb's line last,
// naming the account and the reference by kind; on an address on this
// machine it adds that the local server may not be running. A launch that
// succeeds is not checked and gets no line.
func TestUnresolvedAccountReferenceLine(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	writePlaybook(t, root, "local", &manifest.Manifest{IsolatedLogin: true})
	writePlaybook(t, root, "remote", &manifest.Manifest{IsolatedLogin: true})
	helper, dir, helperLog := fileHelper(t)
	claudeLog := stubClaude(t)
	tok := filepath.Join(dir, "tok")
	if err := os.WriteFile(tok, []byte("placeholder-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "ALTER DEFAULTS SET secret_helper = "+helper)
	mustStmt(t, "CREATE ACCOUNT local SET provider=gateway, base_url=http://127.0.0.1:4100, auth_token FROM file:tok")
	mustStmt(t, "CREATE ACCOUNT remote SET provider=gateway, base_url=https://router.example/v1, auth_token FROM file:tok")
	mustStmt(t, "ALTER PLAYBOOK local USE ACCOUNT local")
	mustStmt(t, "ALTER PLAYBOOK remote USE ACCOUNT remote")

	// Succeeds: the token reaches claude; nothing is checked after it.
	os.Remove(helperLog)
	out := captureStderr(t, func() {
		if err := runRun(nil, []string{"local", "--version"}); err != nil {
			t.Fatal(err)
		}
	})
	if got := readLog(t, claudeLog); !strings.Contains(got, "ANTHROPIC_AUTH_TOKEN=placeholder-token") {
		t.Fatalf("claude did not get the token:\n%s", got)
	}
	if strings.Contains(out, "cannot resolve account") || strings.Contains(readLog(t, helperLog), "--check") {
		t.Fatalf("a launch that succeeded was checked:\n%s", out)
	}

	// The token is gone, as when the local server that writes it is not
	// running: the helper refuses, claude never runs, and cpb's line is last.
	if err := os.Remove(tok); err != nil {
		t.Fatal(err)
	}
	os.Remove(claudeLog)
	var err error
	out = captureStderr(t, func() { err = runRun(nil, []string{"local", "--version"}) })
	if err == nil {
		t.Fatal("a launch without its token succeeded")
	}
	if _, serr := os.Stat(claudeLog); serr == nil {
		t.Fatal("claude ran without its token")
	}
	last := lastLine(out)
	for _, want := range []string{"account local's auth_token (a file: reference)", "http://127.0.0.1:4100, is on this machine", "local server behind it may not be running", "cpb SHOW ACCOUNT local"} {
		if !strings.Contains(last, want) {
			t.Fatalf("cpb's line lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "file-helper: cannot resolve file:tok") || strings.Index(out, "file-helper:") > strings.Index(out, last) {
		t.Fatalf("the helper's own message should come first:\n%s", out)
	}
	if strings.Contains(strings.ToLower(last), "store") || strings.Contains(last, "placeholder") {
		t.Fatalf("cpb's line says to store the secret, or shows a value:\n%s", last)
	}

	// An address elsewhere: the same line, without the local server.
	out = captureStderr(t, func() { err = runRun(nil, []string{"remote", "--version"}) })
	if err == nil {
		t.Fatal("a launch without its token succeeded")
	}
	last = lastLine(out)
	if !strings.Contains(last, "account remote's auth_token (a file: reference)") || strings.Contains(last, "on this machine") {
		t.Fatalf("cpb's line for a remote address:\n%s", out)
	}

	// The check sees the launch's environment, not cpb's own: a layer that
	// moves the helper's store to a folder without the token gets the line,
	// though cpb's own environment still points at one that has it.
	if err := os.WriteFile(tok, []byte("placeholder-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	out = captureStderr(t, func() { err = runRun(nil, []string{"--env", "HELPER_DIR=" + elsewhere, "local", "--version"}) })
	if err == nil {
		t.Fatal("a launch whose helper store has no token succeeded")
	}
	if !strings.Contains(lastLine(out), "account local's auth_token (a file: reference)") {
		t.Fatalf("the check did not use the launch's environment:\n%s", out)
	}
}

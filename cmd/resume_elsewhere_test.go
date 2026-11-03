package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// A --resume <id> given to a playbook that holds no transcript of it, while
// another playbook does: refused before claude starts (Claude Code would
// only say it found no conversation), naming the playbook that holds it and
// the command that resumes it there, the launch's --account kept.
func TestRunResumeAnotherPlaybooksSession(t *testing.T) {
	_, work, _ := sessionFixture(t)
	putAccount(t, account.Account{Name: "acc", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Value: "t"}})
	log := resumeClaude(t)
	chdirT(t, work)
	l, _, err := runClaude(t, log, "alpha", "--resume", sidOld)
	if err == nil || !strings.Contains(err.Error(), "is not playbook alpha's: it is playbook beta's") ||
		!strings.Contains(err.Error(), "Resume it there: cpb run beta --resume "+sidOld) || l != "" {
		t.Fatalf("beta's session given to alpha: %v (claude: %q)", err, l)
	}
	l, _, err = runClaude(t, log, "--account", "acc", "alpha", "--resume", sidOld)
	if err == nil || !strings.Contains(err.Error(), "Resume it there: cpb run --account acc beta --resume "+sidOld) || l != "" {
		t.Fatalf("with --account: %v (claude: %q)", err, l)
	}
	// From another folder, the command goes there first.
	chdirT(t, t.TempDir())
	want := "cd " + shellQuoteTest(work) + " && cpb run beta --resume " + sidOld
	if _, _, err := runClaude(t, log, "alpha", "--resume", sidOld); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("from elsewhere: %v, want %q", err, want)
	}
}

// A session still running elsewhere: the refusal says where it runs, and
// offers a copy under a new id (--fork-session, from the session's folder)
// beside the picker, both keeping the launch's --account.
func TestLiveRefusalSaysWhereAndHowOn(t *testing.T) {
	_, work, _ := sessionFixture(t)
	putAccount(t, account.Account{Name: "acc", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Value: "t"}})
	log := resumeClaude(t)
	chdirT(t, work)
	_, _, err := runClaude(t, log, "alpha", "--resume", sidLive)
	if err == nil || !strings.Contains(err.Error(), "in "+tildePath(work)+", named s-1111") ||
		!strings.Contains(err.Error(), "open a copy of it under a new id with al --resume "+sidLive+" --fork-session") ||
		!strings.Contains(err.Error(), "pick another with al --resume") {
		t.Fatalf("here: %v", err)
	}
	_, _, err = runClaude(t, log, "--account", "acc", "alpha", "--resume", sidLive)
	if err == nil || !strings.Contains(err.Error(), "al --account acc --resume "+sidLive+" --fork-session") ||
		!strings.Contains(err.Error(), "pick another with al --account acc --resume") {
		t.Fatalf("with --account: %v", err)
	}
	// Given to beta, which holds no transcript of it: the copy is alpha's.
	_, _, err = runClaude(t, log, "beta", "--resume", sidLive)
	if err == nil || !strings.Contains(err.Error(), "with al --resume "+sidLive+" --fork-session") ||
		!strings.Contains(err.Error(), "pick another with cpb run beta --resume") {
		t.Fatalf("given to beta: %v", err)
	}
	chdirT(t, t.TempDir())
	want := "cd " + shellQuoteTest(work) + " && al --resume " + sidLive + " --fork-session"
	if _, _, err := runClaude(t, log, "alpha", "--resume", sidLive); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("from elsewhere: %v, want %q", err, want)
	}
}

// The launch's --account goes along to another playbook only when that
// playbook may run on it; otherwise the command goes without it, and the
// refusal says why.
func TestResumeAccountNotCarriedWhereRefused(t *testing.T) {
	root, work, _ := sessionFixture(t)
	putAccount(t, account.Account{Name: "acc", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Value: "t"}})
	putAccount(t, account.Account{Name: "other", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Value: "u"}})
	writePlaybook(t, root, "beta", &manifest.Manifest{Env: &manifest.Env{Accounts: []string{"other"}}})
	log := resumeClaude(t)
	chdirT(t, work)
	_, _, err := runClaude(t, log, "--account", "acc", "alpha", "--resume", sidOld)
	if err == nil || !strings.Contains(err.Error(), "Resume it there: cpb run beta --resume "+sidOld+" (without --account acc, which playbook beta cannot run on") {
		t.Fatalf("beta lists only other: %v", err)
	}
	// The fork from alpha, given to beta on acc: alpha lists only other.
	writePlaybook(t, root, "alpha", &manifest.Manifest{Launcher: "al", Env: &manifest.Env{Accounts: []string{"other"}}})
	writePlaybook(t, root, "beta", &manifest.Manifest{})
	_, _, err = runClaude(t, log, "--account", "acc", "beta", "--resume", sidLive)
	if err == nil || !strings.Contains(err.Error(), "with al --resume "+sidLive+" --fork-session (without --account acc, which playbook alpha cannot run on") ||
		!strings.Contains(err.Error(), "pick another with cpb run --account acc beta --resume") {
		t.Fatalf("the fork from alpha: %v", err)
	}
}

// A session recorded in another pid domain (a sandbox, another host) is
// refused as one cpb cannot confirm, with no copy offered: its folder is
// that machine's.
func TestLiveRefusalNoForkFromAnotherPidDomain(t *testing.T) {
	root, work, _ := sessionFixture(t)
	a := filepath.Join(root, "alpha")
	b, _ := json.Marshal(map[string]any{
		"pid": 901, "sessionId": sidDead, "cwd": "/elsewhere/app", "startedAt": time.Now().UnixMilli(), "procStart": liveStart,
		"version": "2.1.283", "kind": "interactive", "entrypoint": "cli", "pidDomain": "sandbox-1", "status": "idle",
	})
	if err := os.WriteFile(filepath.Join(a, "sessions", "901.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	log := resumeClaude(t)
	chdirT(t, work)
	_, _, err := runClaude(t, log, "alpha", "--resume", sidDead)
	if err == nil || !strings.Contains(err.Error(), `another pid domain, "sandbox-1"`) || strings.Contains(err.Error(), "--fork-session") ||
		!strings.Contains(err.Error(), "close that one first, or pick another with al --resume") {
		t.Fatalf("another pid domain: %v", err)
	}
}

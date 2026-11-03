package cmd

import (
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
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

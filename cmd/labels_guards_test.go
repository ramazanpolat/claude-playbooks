package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
)

// A launch record holding a label cpb would refuse is not read: a record
// is cpb's own, so one edited by hand counts as no record.
func TestLaunchRecordWithARefusedLabelIsNotRead(t *testing.T) {
	root, rec := stepFixture(t, "work", "work", nil)
	dir := launchRecordsDir(root)
	path := filepath.Join(dir, rec.LaunchID+".json")
	rec.Labels = map[string]string{"task": "ok"}
	if err := writeLaunchRecord(dir, path, &rec); err != nil {
		t.Fatal(err)
	}
	if got, ok := readLaunchRecordByID(root, rec.LaunchID); !ok || got.Labels["task"] != "ok" {
		t.Fatalf("a good record: %+v %v", got, ok)
	}
	rec.Labels = map[string]string{"task": "a\u200bb"}
	if err := writeLaunchRecord(dir, path, &rec); err != nil {
		t.Fatal(err)
	}
	if _, ok := readLaunchRecordByID(root, rec.LaunchID); ok {
		t.Fatal("a record with a refused label was read")
	}
}

// The cpb key has no labels key when the session has none.
func TestBlockHasNoLabelsWhenEmpty(t *testing.T) {
	root, rec := stepFixture(t, "work", "work", nil)
	out := stepRecord(stepArgs{root: root, launch: rec.LaunchID, original: "true"}, statusInput(stepSID, 10, 1, 0.1))
	if !strings.Contains(string(out), `"cpb":{"v":1`) || strings.Contains(string(out), `"labels"`) {
		t.Fatalf("the cpb key: %s", out)
	}
}

// A labels file is read only for the session it names, only within the
// caps, and only through real folders.
func TestLabelsFileGuards(t *testing.T) {
	root := t.TempDir()
	if _, err := updateSessionLabels(root, sidLive, map[string]string{"k": "v"}, nil); err != nil {
		t.Fatal(err)
	}
	// Another session's id in the file.
	b, _ := json.Marshal(sessionLabelsFile{V: 1, SessionID: "33333333-3333-4333-8333-333333333333", Labels: map[string]string{"k": "v"}})
	if err := os.WriteFile(labelsPath(root, sidLive), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readSessionLabels(root, sidLive); got != nil {
		t.Fatalf("read a file naming another session: %v", got)
	}
	// Over 4 KiB together: 16 values a JSON encoder writes six bytes per
	// character of.
	big := map[string]string{}
	for i := 0; i < labelsPerSource; i++ {
		big[string(rune('a'+i))] = strings.Repeat("<", labelValueMax)
	}
	if err := checkLabelSet(big); err == nil || !strings.Contains(err.Error(), "bytes together") {
		t.Fatalf("over 4 KiB: %v", err)
	}
	if _, err := updateSessionLabels(root, sidLive, big, nil); err == nil {
		t.Fatal("wrote labels over 4 KiB")
	}
	b, _ = json.Marshal(sessionLabelsFile{V: 1, SessionID: sidLive, Labels: big})
	if err := os.WriteFile(labelsPath(root, sidLive), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readSessionLabels(root, sidLive); got != nil {
		t.Fatal("read labels over 4 KiB")
	}
}

// A labels folder that is a symbolic link is neither read, written nor
// swept through; a lock that is a symbolic link is not followed.
func TestLabelsSymlinks(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	b, _ := json.Marshal(sessionLabelsFile{V: 1, SessionID: sidLive, Labels: map[string]string{"k": "v"}})
	target := filepath.Join(elsewhere, sidLive+".json")
	if err := os.WriteFile(target, b, 0o600); err != nil {
		t.Fatal(err)
	}
	long := time.Now().Add(-31 * 24 * time.Hour)
	if err := os.Chtimes(target, long, long); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(usageDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, labelsDir(root)); err != nil {
		t.Fatal(err)
	}
	if got := readSessionLabels(root, sidLive); got != nil {
		t.Fatalf("read through a linked folder: %v", got)
	}
	if _, err := updateSessionLabels(root, sidLive, map[string]string{"k": "w"}, nil); err == nil {
		t.Fatal("wrote through a linked folder")
	}
	sweepUsage(root, time.Now())
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("swept through a linked folder: %v", err)
	}

	// The lock.
	root = t.TempDir()
	if err := os.MkdirAll(labelsDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(elsewhere, "not-a-lock")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(labelsDir(root), "."+sidLive+".lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := updateSessionLabels(root, sidLive, map[string]string{"k": "v"}, nil); err == nil {
		t.Fatal("followed a linked lock")
	}
	if fi, _ := os.Stat(other); fi.ModTime().After(time.Now().Add(-time.Minute)) && readSessionLabels(root, sidLive) != nil {
		t.Fatal("wrote labels through a linked lock")
	}
}

// The sweep keeps the labels of a session still running, however long
// nobody wrote them, and removes those of one that ended.
func TestSweepKeepsARunningSessionsLabels(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", nil)
	work := t.TempDir()
	writeSessionFile(t, filepath.Join(root, "alpha"), 301, sidLive, work, "interactive", liveStart, time.Now())
	fakeProcInfo(t, map[int]procInfo{301: {start: liveStart}})
	const ended = "33333333-3333-4333-8333-333333333333"
	long := time.Now().Add(-31 * 24 * time.Hour)
	for _, sid := range []string{sidLive, ended} {
		if _, err := updateSessionLabels(root, sid, map[string]string{"k": "v"}, nil); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{labelsPath(root, sid), filepath.Join(labelsDir(root), "."+sid+".lock")} {
			if err := os.Chtimes(p, long, long); err != nil {
				t.Fatal(err)
			}
		}
	}
	sweepUsage(root, time.Now())
	if readSessionLabels(root, sidLive)["k"] != "v" {
		t.Fatal("swept a running session's labels")
	}
	if _, err := os.Stat(labelsPath(root, ended)); !os.IsNotExist(err) {
		t.Fatalf("kept an ended session's labels: %v", err)
	}
}

// A registry cpb cannot read is "not in a session cpb launched" too: a hook
// that ignores exit 3 is not broken by it.
func TestSessionLabelRegistryUnreadable(t *testing.T) {
	resetCommandTestState(t)
	file := filepath.Join(t.TempDir(), "not-a-folder")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	config.PlaybooksDir = file
	sessionLabelUnset, sessionLabelList, sessionLabelJSON, sessionLabelSession = nil, false, false, ""
	var err error
	captureStderr(t, func() { err = runSessionLabel(nil, []string{"task=x"}) })
	if code, ok := exitCode(err); !ok || code != exitNotASession {
		t.Fatalf("exit: %v", err)
	}
}

// A session cpb cannot confirm over keeps its labels through the sweep: one
// whose file records no start time, on a pid that is alive. One whose pid
// is gone loses them.
func TestSweepKeepsAnUnconfirmedSessionsLabels(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", nil)
	work := t.TempDir()
	const gone = "33333333-3333-4333-8333-333333333333"
	writeSessionFile(t, filepath.Join(root, "alpha"), 301, sidLive, work, "interactive", "", time.Now())
	writeSessionFile(t, filepath.Join(root, "alpha"), 302, gone, work, "interactive", liveStart, time.Now())
	fakeProcInfo(t, map[int]procInfo{301: {start: liveStart}})
	long := time.Now().Add(-31 * 24 * time.Hour)
	for _, sid := range []string{sidLive, gone} {
		if _, err := updateSessionLabels(root, sid, map[string]string{"k": "v"}, nil); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{labelsPath(root, sid), filepath.Join(labelsDir(root), "."+sid+".lock")} {
			if err := os.Chtimes(p, long, long); err != nil {
				t.Fatal(err)
			}
		}
	}
	sweepUsage(root, time.Now())
	if readSessionLabels(root, sidLive)["k"] != "v" {
		t.Fatal("swept the labels of a session not known to be over")
	}
	if _, err := os.Stat(labelsPath(root, gone)); !os.IsNotExist(err) {
		t.Fatalf("kept the labels of a session whose process is gone: %v", err)
	}
}

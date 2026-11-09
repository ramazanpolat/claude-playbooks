package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
)

// The cpb ask store (SPEC.md, "cpb ask", "The ask store"): <root>/.ask/, with
// a CLAUDE.md of the user's own that every ask hands the model, and
// history.md, one markdown record per answered ask, appended and never
// rewritten. A record's heading is its index line: the time, the playbook
// the ask was addressed to ("-" for none) and its id. There is no second
// index file to keep in step: the index is read from the headings.

const (
	askStoreDirName  = ".ask"
	askHistoryName   = "history.md"
	askStandingName  = "CLAUDE.md"
	askStandingLimit = 8 << 10
	// askIndexWindow is how many index lines every ask hands the model;
	// SHOW ASK HISTORY, in the tool too, pages further back.
	askIndexWindow = grammar.AskHistoryDefault
)

func askStoreDir(root string) string { return filepath.Join(root, askStoreDirName) }

// askEntry is one record read back: its heading's fields, what was asked
// (on one line, as it was recorded), and the record's text.
type askEntry struct {
	Time  string `json:"time"`
	Tag   string `json:"playbook"`
	ID    string `json:"id"`
	Asked string `json:"asked"`
	text  string
}

var askHeading = regexp.MustCompile(`^## (\S+) · (\S+) · (` + strings.Trim(grammar.AskRecordID.String(), "^$") + `)$`)

// askStoreFile says whether path, a file in the ask store, is there to
// read, refusing anything but a regular file in a real directory: a link,
// at the file or at .ask itself, could point at anything, and what is read
// is sent. A missing file is (false, nil). The file is then opened with
// askStoreOpen, which does not follow a link either.
func askStoreFile(path string) (bool, error) {
	dir := filepath.Dir(path)
	if info, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("%s is not a directory (a link, say): cpb ask reads and writes only its own directory there, so nothing was sent. Replace it with a directory", dir)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file (a link, say): cpb ask reads only its own files there, so nothing was sent. Replace it with a file", path)
	}
	return true, nil
}

// askStanding is the user's standing context, the text of <root>/.ask/
// CLAUDE.md: "" when there is none. One that holds a credential refuses the
// ask before anything is sent, naming the file and the key.
func askStanding(root string) (string, error) {
	path := filepath.Join(askStoreDir(root), askStandingName)
	ok, err := askStoreFile(path)
	if !ok || err != nil {
		return "", err
	}
	data, err := askStoreRead(path)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(data))
	if found := askRequestCredential(text); found != "" {
		return "", fmt.Errorf("%s looks like it holds a credential (%s): nothing was sent. Leave the value out and name it instead", path, found)
	}
	if len(text) > askStandingLimit {
		cut := askStandingLimit
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "\n[cut: the file is longer than 8 KB]"
	}
	return text, nil
}

// askStoreRead reads a file of the ask store without following a link.
func askStoreRead(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// readAskHistory reads every record, oldest first. A missing file is no
// history. A record starts at a line that parses as a record heading and
// runs to the next one; any other line, a markdown heading in an answer
// among them, is part of the record it is in. Lines before the first
// heading are skipped.
func readAskHistory(root string) ([]askEntry, error) {
	path := filepath.Join(askStoreDir(root), askHistoryName)
	ok, err := askStoreFile(path)
	if !ok || err != nil {
		return nil, err
	}
	data, err := askStoreRead(path)
	if err != nil {
		return nil, err
	}
	var out []askEntry
	var cur *askEntry
	var body []string
	flush := func() {
		if cur != nil {
			cur.text = strings.TrimRight(strings.Join(body, "\n"), "\n")
			out = append(out, *cur)
		}
		cur, body = nil, nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		if m := askHeading.FindStringSubmatch(line); m != nil {
			flush()
			cur = &askEntry{Time: m[1], Tag: m[2], ID: m[3]}
			body = []string{line}
			continue
		}
		if cur == nil {
			continue
		}
		body = append(body, line)
		if a, ok := strings.CutPrefix(line, "**Asked:** "); ok && cur.Asked == "" {
			cur.Asked = a
		}
	}
	flush()
	return out, nil
}

// askHistoryPrelude is what every ask hands the model of the past: the
// index lines of the last askIndexWindow records, newest first, and the
// last record in full (the last addressed to tag, when the ask is
// addressed to a playbook). "" when there is no history.
func askHistoryPrelude(root, tag string) (string, error) {
	entries, err := readAskHistory(root)
	if err != nil || len(entries) == 0 {
		return "", err
	}
	var b strings.Builder
	b.WriteString("History of your earlier answers, newest first (time · playbook asked · id · what was asked). It is the past: the installed state above is the present. Read a record in full with the query tool's SHOW ASK RECORD <id>, and further back with SHOW ASK HISTORY BEFORE <id>:\n")
	for i := len(entries) - 1; i >= 0 && i >= len(entries)-askIndexWindow; i-- {
		e := entries[i]
		fmt.Fprintf(&b, "%s · %s · %s · %s\n", e.Time, e.Tag, e.ID, e.Asked)
	}
	last := entries[len(entries)-1]
	if tag != "" {
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].Tag == tag {
				last = entries[i]
				break
			}
		}
	}
	b.WriteString("\nThe last exchange in full:\n" + last.text + "\n")
	return b.String(), nil
}

// askRecordText is the record of an answered ask. Every part written after
// what was asked, the guards and the reasons a step was not proposed among
// them, is withheld when it looks like it holds a credential: the request
// passed the same check before it was sent. A line of the record that
// starts with "#" is written as "\#", so nothing the model wrote can start a
// record of its own or end this one.
func askRecordText(now time.Time, tag, id, request string, p *askPlan) string {
	clean := func(s string) string {
		if askRequestCredential(s) != "" {
			return "<withheld: it looked like it held a credential>"
		}
		return s
	}
	if tag == "" {
		tag = "-"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**Asked:** %s\n\n", strings.Join(strings.Fields(request), " "))
	if a := strings.TrimSpace(p.Answer); a != "" {
		fmt.Fprintf(&b, "**Answer:** %s\n\n", clean(a))
	}
	if len(p.Steps) > 0 {
		b.WriteString("**Steps:**\n")
		for _, s := range p.Steps {
			line := "`" + clean(s.Command) + "`"
			switch s.Status {
			case stepRejected:
				line += " not proposed: " + clean(s.Reason)
			case stepPlaceholder:
				line += " " + clean(s.Reason)
			default:
				if why := strings.TrimSpace(s.Why); why != "" {
					line += ": " + clean(why)
				}
			}
			b.WriteString("- " + line + "\n")
			for _, g := range s.Guards {
				b.WriteString("  - ! " + clean(g.What) + "\n")
			}
		}
		b.WriteString("\n")
	}
	if len(p.Notes) > 0 {
		b.WriteString("**Notes:**\n")
		for _, n := range p.Notes {
			b.WriteString("- " + clean(n) + "\n")
		}
		b.WriteString("\n")
	}
	lines := strings.Split(b.String(), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "#") {
			lines[i] = "\\" + l
		}
	}
	return fmt.Sprintf("## %s · %s · %s\n\n", now.Format(time.RFC3339), tag, id) + strings.Join(lines, "\n")
}

// newAskRecordID is r-<stamp>-<4 hex>: unique, and sorted by time.
func newAskRecordID(now time.Time) string {
	var r [2]byte
	rand.Read(r[:])
	return "r-" + now.Format("20060102-150405") + "-" + hex.EncodeToString(r[:])
}

// appendAskRecord appends one record to <root>/.ask/history.md: the
// directory 0700, the file 0600 when it is made, one write under an
// exclusive lock, starting on a line of its own when an earlier write was
// cut off before its last newline. It returns the record's id.
func appendAskRecord(root, tag, request string, p *askPlan) (string, error) {
	dir := askStoreDir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, askHistoryName)
	if _, err := askStoreFile(path); err != nil {
		return "", err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return "", err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", err
	}
	now := time.Now()
	id := newAskRecordID(now)
	text := askRecordText(now, tag, id, request, p)
	if info, err := f.Stat(); err != nil {
		f.Close()
		return "", err
	} else if n := info.Size(); n > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, n-1); err != nil {
			f.Close()
			return "", err
		}
		if last[0] != '\n' {
			text = "\n" + text
		}
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return "", err
	}
	if info, err := f.Stat(); err == nil && info.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(os.Stderr, "Warning: %s can be read by others (mode %o): what you ask cpb is in it\n", path, info.Mode().Perm())
	}
	return id, f.Close()
}

// askHistoryPage is one page of SHOW ASK HISTORY: the records older than
// before ("" for the newest), addressed to tag when one is given, at most
// limit of them (0: the default), newest first, and how many older ones
// match beyond the page. It reads <root>/.ask/history.md, a path cpb
// fixes, and nothing else.
func askHistoryPage(root, tag, before string, limit int) ([]askEntry, int, error) {
	if limit <= 0 {
		limit = grammar.AskHistoryDefault
	}
	limit = min(limit, grammar.AskHistoryMax)
	entries, err := readAskHistory(root)
	if err != nil {
		return nil, 0, err
	}
	out := []askEntry{}
	older := 0
	seen := before == ""
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if !seen {
			seen = e.ID == before
			continue
		}
		if tag != "" && e.Tag != tag {
			continue
		}
		if len(out) < limit {
			out = append(out, e)
		} else {
			older++
		}
	}
	if !seen {
		return nil, 0, fmt.Errorf("no record %s in the ask history", before)
	}
	return out, older, nil
}

// askRecordJSON is one record in full: SHOW ASK RECORD --json.
type askRecordJSON struct {
	askEntry
	Text string `json:"text"`
}

// askRecordByID is SHOW ASK RECORD <id>: one record in full.
func askRecordByID(root, id string) (askRecordJSON, error) {
	entries, err := readAskHistory(root)
	if err != nil {
		return askRecordJSON{}, err
	}
	for _, e := range entries {
		if e.ID == id {
			return askRecordJSON{askEntry: e, Text: e.text}, nil
		}
	}
	return askRecordJSON{}, fmt.Errorf("no record %s in the ask history", id)
}

// showAskStore runs SHOW ASK HISTORY and SHOW ASK RECORD.
func showAskStore(root string, st *grammar.Stmt) error {
	if st.Object == grammar.AskRecord {
		r, err := askRecordByID(root, st.Name)
		if err != nil {
			return err
		}
		if st.JSON {
			return printJSON(r)
		}
		fmt.Println(r.Text)
		return nil
	}
	page, older, err := askHistoryPage(root, st.For, st.HistoryBefore, st.HistoryLimit)
	if err != nil {
		return err
	}
	if st.JSON {
		return printJSON(page)
	}
	if len(page) == 0 {
		switch {
		case st.HistoryBefore != "":
			fmt.Printf("No records before %s.\n", st.HistoryBefore)
		case st.For != "":
			fmt.Printf("No asks addressed to playbook %s are recorded.\n", st.For)
		default:
			fmt.Printf("No asks are recorded (%s).\n", tildePath(filepath.Join(askStoreDir(root), askHistoryName)))
		}
		return nil
	}
	t := newTable("TIME", "PLAYBOOK", "ID", "ASKED").flexible(3)
	for _, e := range page {
		t.add(e.Time, e.Tag, e.ID, e.Asked)
	}
	t.render(os.Stdout)
	if older > 0 {
		next := *st
		next.HistoryBefore = page[len(page)-1].ID
		next.JSON = false
		fmt.Printf("%d older: cpb %s\n", older, next.String())
	}
	return nil
}

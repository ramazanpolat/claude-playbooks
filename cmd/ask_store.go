package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
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
	// the tool's HISTORY pages further back.
	askIndexWindow = 20
	askIndexMax    = 200
)

func askStoreDir(root string) string { return filepath.Join(root, askStoreDirName) }

// askEntry is one record read back: its heading's fields, the first line of
// what was asked, and the record's text.
type askEntry struct {
	Time  string `json:"time"`
	Tag   string `json:"playbook"`
	ID    string `json:"id"`
	Asked string `json:"asked"`
	text  string
}

var askHeading = regexp.MustCompile(`^## (\S+) · (\S+) · (r-[0-9]{8}-[0-9]{6}-[0-9a-f]{4})$`)

// askStoreFile says whether path is there to read, refusing anything but a
// regular file: a link could point at anything, and what is read is sent. A
// missing file is (false, nil).
func askStoreFile(path string) (bool, error) {
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
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(data))
	if found := askRequestCredential(text); found != "" {
		return "", fmt.Errorf("%s looks like it holds a credential (%s): nothing was sent. Leave the value out and name it instead", path, found)
	}
	if len(text) > askStandingLimit {
		text = text[:askStandingLimit] + "\n[cut: the file is longer than 8 KB]"
	}
	return text, nil
}

// readAskHistory reads every record, oldest first. A missing file is no
// history; a record whose heading does not parse (a torn last append) is
// skipped.
func readAskHistory(root string) ([]askEntry, error) {
	path := filepath.Join(askStoreDir(root), askHistoryName)
	ok, err := askStoreFile(path)
	if !ok || err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
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
		if strings.HasPrefix(line, "## ") {
			flush()
			if m := askHeading.FindStringSubmatch(line); m != nil {
				cur = &askEntry{Time: m[1], Tag: m[2], ID: m[3]}
				body = []string{line}
			}
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
	b.WriteString("History of your earlier answers, newest first (time · playbook asked · id · what was asked). It is the past: the installed state above is the present. Read a record in full with the query tool's HISTORY RECORD <id>, and further back with HISTORY BEFORE <id>:\n")
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

// askRecordText is the record of an answered ask. A part that looks like it
// holds a credential is withheld: the request passed the same check before
// it was sent, so only the model's own words can trip it.
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
	fmt.Fprintf(&b, "## %s · %s · %s\n\n", now.Format(time.RFC3339), tag, id)
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
				line += " not proposed: " + s.Reason
			case stepPlaceholder:
				line += " " + s.Reason
			default:
				if why := strings.TrimSpace(s.Why); why != "" {
					line += ": " + clean(why)
				}
			}
			b.WriteString("- " + line + "\n")
			for _, g := range s.Guards {
				b.WriteString("  - ! " + g.What + "\n")
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
	return b.String()
}

// newAskRecordID is r-<stamp>-<4 hex>: unique, and sorted by time.
func newAskRecordID(now time.Time) string {
	var r [2]byte
	rand.Read(r[:])
	return "r-" + now.Format("20060102-150405") + "-" + hex.EncodeToString(r[:])
}

// appendAskRecord appends one record to <root>/.ask/history.md: the
// directory 0700, the file 0600 when it is made, one write under an
// exclusive lock. It returns the record's id.
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
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", err
	}
	now := time.Now()
	id := newAskRecordID(now)
	if _, err := f.WriteString(askRecordText(now, tag, id, request, p)); err != nil {
		f.Close()
		return "", err
	}
	if info, err := f.Stat(); err == nil && info.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(os.Stderr, "Warning: %s can be read by others (mode %o): what you ask cpb is in it\n", path, info.Mode().Perm())
	}
	return id, f.Close()
}

// askHistoryQuery answers the tool's HISTORY statements:
//
//	HISTORY [FOR <playbook>] [BEFORE <id>] [LIMIT <n>]   index lines, newest first
//	HISTORY RECORD <id>                                  one record in full
//
// They read <root>/.ask/history.md, a path cpb fixes, and nothing else.
func askHistoryQuery(root string, words []string) (string, error) {
	usage := errors.New("HISTORY [FOR <playbook>] [BEFORE <id>] [LIMIT <n>], or HISTORY RECORD <id>")
	entries, err := readAskHistory(root)
	if err != nil {
		return "", err
	}
	if len(words) == 2 && strings.EqualFold(words[0], "RECORD") {
		for _, e := range entries {
			if e.ID == words[1] {
				return e.text, nil
			}
		}
		return "", fmt.Errorf("no record %s", words[1])
	}
	tag, before, limit := "", "", askIndexWindow
	for i := 0; i < len(words); i += 2 {
		if i+1 >= len(words) {
			return "", usage
		}
		switch strings.ToUpper(words[i]) {
		case "FOR":
			tag = words[i+1]
		case "BEFORE":
			before = words[i+1]
		case "LIMIT":
			n, err := strconv.Atoi(words[i+1])
			if err != nil || n < 1 {
				return "", usage
			}
			limit = min(n, askIndexMax)
		default:
			return "", usage
		}
	}
	var out []askEntry
	seen := before == ""
	for i := len(entries) - 1; i >= 0 && len(out) < limit; i-- {
		e := entries[i]
		if !seen {
			seen = e.ID == before
			continue
		}
		if tag == "" || e.Tag == tag {
			out = append(out, e)
		}
	}
	if !seen {
		return "", fmt.Errorf("no record %s", before)
	}
	if out == nil {
		out = []askEntry{}
	}
	data, err := json.Marshal(map[string]any{"records": out})
	return string(data), err
}

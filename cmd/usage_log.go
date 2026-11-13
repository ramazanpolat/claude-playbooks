package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// The usage log (SPEC.md, "SHOW USAGE" and "Files and state"): what the
// status line input says about a session, written by cpb's status line step
// (statusline_step.go) under <playbooks root>/.state/usage/:
//
//	sessions/<YYYY-MM>/<session id>.jsonl   one snapshot per line, append-only
//	accounts/<hash>.json                    the latest figure per usage key
//
// Every line describes itself (time, session, config dir, account, usage
// key, identity, playbook), so a reader that ships them elsewhere needs no
// other file. Directories are 0700 and files 0600. Nothing in them is a
// credential: the status line input carries none, and cpb adds names only.

const (
	usageVersion = 1
	// usageRotateBytes is the size at which a session's log becomes
	// <id>.1.jsonl (replacing an older one) and a new one starts.
	usageRotateBytes = 1 << 20
	// usageFloor is the least time between two lines that differ only in
	// cost or context: a reply changes them, a figure the person reads does
	// not need every one.
	usageFloor = 60 * time.Second
	// usageSweepAge is how long a log or a latest figure nobody wrote stays.
	usageSweepAge = 30 * 24 * time.Hour
	// usageRawCap bounds what one object from the input may take in a line.
	usageRawCap = 2 << 10
	// usageStringCap bounds every string a line takes from the input.
	usageStringCap = 512
)

func usageDir(root string) string { return filepath.Join(root, ".state", "usage") }

// statusSessionID is the only session id shape the step writes a file for:
// Claude Code's own (a UUID). Anything else is recorded nowhere.
var statusSessionID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// snapshot is one line of a session's log, version 1.
type usageSnapshot struct {
	V             int             `json:"v"`
	At            string          `json:"at"`
	SessionID     string          `json:"session_id"`
	ConfigDir     string          `json:"config_dir"`
	Playbook      string          `json:"playbook"`
	Account       string          `json:"account"`
	Key           string          `json:"key"`
	Identity      *string         `json:"identity"`
	ClaudeVersion *string         `json:"claude_version"`
	SessionName   *string         `json:"session_name"`
	Model         *string         `json:"model"`
	Effort        *string         `json:"effort"`
	FastMode      *bool           `json:"fast_mode"`
	Thinking      *bool           `json:"thinking"`
	RateLimits    json.RawMessage `json:"rate_limits"`
	Cost          json.RawMessage `json:"cost"`
	ContextWindow json.RawMessage `json:"context_window"`
	PromptCache   json.RawMessage `json:"prompt_cache"`
	ProjectDir    *string         `json:"project_dir"`
	Repo          json.RawMessage `json:"repo"`
	GitWorktree   *string         `json:"git_worktree"`
	Agent         json.RawMessage `json:"agent"`
}

// capString keeps at most usageStringCap bytes of s, cut at a rune
// boundary; nil for "".
func capString(s string) *string {
	if s == "" {
		return nil
	}
	if len(s) > usageStringCap {
		s = s[:usageStringCap]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return &s
}

// capRaw keeps an object or a value from the input when it is valid and
// small; null otherwise.
func capRaw(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > usageRawCap || !json.Valid(raw) || string(raw) == "null" {
		return nil
	}
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return nil
	}
	return b.Bytes()
}

// rawField decodes raw into v; false when it is absent or of another type.
func rawField(raw json.RawMessage, v any) bool {
	return len(raw) > 0 && json.Unmarshal(raw, v) == nil
}

// pickRaw is the subset of a raw object holding only keys, compacted; nil
// when none of them is there.
func pickRaw(raw json.RawMessage, keys ...string) json.RawMessage {
	var m map[string]json.RawMessage
	if !rawField(raw, &m) {
		return nil
	}
	out := map[string]json.RawMessage{}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	b, _ := json.Marshal(out)
	return capRaw(b)
}

// snapshotOf reads the parts of a status line input a snapshot keeps. Each
// part is decoded on its own, so one of an unexpected type costs that part
// only. ok is false when the input is not an object or its session id is
// not Claude Code's shape: then nothing is recorded.
func snapshotOf(in []byte) (usageSnapshot, bool) {
	var top map[string]json.RawMessage
	if json.Unmarshal(in, &top) != nil {
		return usageSnapshot{}, false
	}
	var s usageSnapshot
	var id string
	if !rawField(top["session_id"], &id) || !statusSessionID.MatchString(id) {
		return usageSnapshot{}, false
	}
	s.V, s.SessionID = usageVersion, id
	str := func(raw json.RawMessage) *string {
		var v string
		rawField(raw, &v)
		return capString(v)
	}
	sub := func(raw json.RawMessage, key string) json.RawMessage {
		var m map[string]json.RawMessage
		if !rawField(raw, &m) {
			return nil
		}
		return m[key]
	}
	s.ClaudeVersion = str(top["version"])
	s.SessionName = str(top["session_name"])
	s.Model = str(sub(top["model"], "id"))
	s.Effort = str(sub(top["effort"], "level"))
	var b bool
	if rawField(top["fast_mode"], &b) {
		s.FastMode = &b
	}
	var t bool
	if rawField(sub(top["thinking"], "enabled"), &t) {
		s.Thinking = &t
	}
	s.RateLimits = capRaw(top["rate_limits"])
	s.Cost = capRaw(top["cost"])
	s.ContextWindow = pickRaw(top["context_window"], "context_window_size", "total_input_tokens", "total_output_tokens",
		"current_usage", "used_percentage", "remaining_percentage")
	s.PromptCache = pickRaw(top["prompt_cache"], "requests", "misses", "expected_rebuilds", "hit_ratio", "ttl", "warm",
		"caching_observed", "cache_write_tokens", "miss_recache_tokens")
	s.ProjectDir = str(sub(top["workspace"], "project_dir"))
	s.Repo = pickRaw(sub(top["workspace"], "repo"), "host", "owner", "name")
	s.GitWorktree = str(sub(top["workspace"], "git_worktree"))
	s.Agent = pickRaw(top["agent"], "name", "type")
	return s, true
}

// changeSignature is what makes a snapshot worth a line whenever it
// changes: the usage figures and what the session runs.
func (s usageSnapshot) changeSignature() string {
	b, _ := json.Marshal([]any{s.RateLimits, s.Model, s.Effort, s.FastMode, s.SessionName, s.Account, s.Key})
	return string(b)
}

// floorSignature is what is written at most once per usageFloor.
func (s usageSnapshot) floorSignature() string {
	b, _ := json.Marshal([]any{s.Cost, s.ContextWindow})
	return string(b)
}

// worthWriting reports whether s goes into the log after last (nil for
// none): a change in the figures, the model, the effort, fast mode, the
// session's name or its account; or, at most once per usageFloor, in cost
// or context. A render with nothing new writes nothing.
func worthWriting(s usageSnapshot, last *usageSnapshot) bool {
	if last == nil {
		return true
	}
	if s.changeSignature() != last.changeSignature() {
		return true
	}
	if s.floorSignature() == last.floorSignature() {
		return false
	}
	at, err1 := time.Parse(time.RFC3339Nano, s.At)
	prev, err2 := time.Parse(time.RFC3339Nano, last.At)
	return err1 != nil || err2 != nil || at.Sub(prev) >= usageFloor
}

// sessionLogPath is where a session's log lives: under the month of its
// first snapshot. An existing log is looked for in this month and the one
// before (a session that runs over the turn of a month keeps its file);
// a new one starts in this month.
func sessionLogPath(root, id string, now time.Time) string {
	base := filepath.Join(usageDir(root), "sessions")
	for _, m := range []time.Time{now, now.AddDate(0, -1, 0)} {
		p := filepath.Join(base, m.UTC().Format("2006-01"), id+".jsonl")
		if _, err := os.Lstat(p); err == nil {
			return p
		}
	}
	return filepath.Join(base, now.UTC().Format("2006-01"), id+".jsonl")
}

// lastSnapshot is the last whole line of an open log, nil when there is
// none or it does not decode.
func lastSnapshot(f *os.File) *usageSnapshot {
	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return nil
	}
	off := max(int64(0), fi.Size()-16<<10)
	buf := make([]byte, fi.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return nil
	}
	buf = bytes.TrimRight(buf, "\n")
	if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
		buf = buf[i+1:]
	}
	var s usageSnapshot
	if json.Unmarshal(buf, &s) != nil || s.V != usageVersion {
		return nil
	}
	return &s
}

// appendSnapshot writes s to its session's log when the write rule says
// so, rotating a full log first. It takes the log's lock without waiting:
// another render of the same session holding it means this one is not
// needed, and the status line never waits on cpb. It reports whether a
// line was written.
func appendSnapshot(root string, s usageSnapshot, now time.Time) bool {
	path := sessionLogPath(root, s.SessionID, now)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return false
	}
	if !worthWriting(s, lastSnapshot(f)) {
		return false
	}
	if fi, err := f.Stat(); err == nil && fi.Size() >= usageRotateBytes {
		// Under the lock, so no other render appends to the old file after
		// the rename; the new file is opened with the same rules.
		if os.Rename(path, strings.TrimSuffix(path, ".jsonl")+".1.jsonl") != nil {
			return false
		}
		nf, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return false
		}
		defer nf.Close()
		f = nf
	}
	line, err := json.Marshal(s)
	if err != nil {
		return false
	}
	// One write: a line is whole or absent.
	_, err = f.Write(append(line, '\n'))
	return err == nil
}

// latestFigure is accounts/<hash>.json: the newest figure seen for one
// usage key, which SHOW USAGE and the status line block read.
type latestFigure struct {
	V          int             `json:"v"`
	Key        string          `json:"key"`
	Account    string          `json:"account"`
	Identity   *string         `json:"identity"`
	SeenAt     string          `json:"seen_at"`
	SessionID  string          `json:"session_id"`
	Playbook   string          `json:"playbook"`
	ConfigDir  string          `json:"config_dir"`
	RateLimits json.RawMessage `json:"rate_limits"`
}

// latestPath names a key's latest file by a hash: a key may hold
// characters no file name should (a layer route's host and port).
func latestPath(root, key string) string {
	h := sha256.Sum256([]byte(key))
	return filepath.Join(usageDir(root), "accounts", hex.EncodeToString(h[:8])+".json")
}

// writeLatest records s as its key's newest figure when it carries one and
// is not older than the figure there. A race between two sessions of one
// key costs at most a figure a moment older; the file is always whole.
func writeLatest(root string, s usageSnapshot) {
	if len(s.RateLimits) == 0 || s.Key == "" {
		return
	}
	path := latestPath(root, s.Key)
	if old, ok := readLatest(path); ok && old.SeenAt > s.At {
		return
	}
	b, err := json.Marshal(latestFigure{V: usageVersion, Key: s.Key, Account: s.Account, Identity: s.Identity,
		SeenAt: s.At, SessionID: s.SessionID, Playbook: s.Playbook, ConfigDir: s.ConfigDir, RateLimits: s.RateLimits})
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".latest-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), 0o600)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
	}
}

// readLatest reads one latest file, defensively.
func readLatest(path string) (latestFigure, bool) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 16<<10 {
		return latestFigure{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return latestFigure{}, false
	}
	var l latestFigure
	if json.Unmarshal(b, &l) != nil || l.V != usageVersion || l.Key == "" || !validRecordedAccount(l.Key) {
		return latestFigure{}, false
	}
	return l, true
}

// readLatestFigures is every latest figure, by usage key.
func readLatestFigures(root string) map[string]latestFigure {
	out := map[string]latestFigure{}
	dir := filepath.Join(usageDir(root), "accounts")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if l, ok := readLatest(filepath.Join(dir, e.Name())); ok {
			if old, seen := out[l.Key]; !seen || l.SeenAt > old.SeenAt {
				out[l.Key] = l
			}
		}
	}
	return out
}

// sweepUsage removes logs and latest figures nobody wrote for
// usageSweepAge, month directories left empty, and temporary files a
// killed writer left. A launch runs it; the status line step never does.
func sweepUsage(root string, now time.Time) {
	old := func(fi os.FileInfo, age time.Duration) bool { return now.Sub(fi.ModTime()) > age }
	sessions := filepath.Join(usageDir(root), "sessions")
	months, _ := os.ReadDir(sessions)
	for _, m := range months {
		if !m.IsDir() {
			continue
		}
		dir := filepath.Join(sessions, m.Name())
		ents, _ := os.ReadDir(dir)
		left := 0
		for _, e := range ents {
			if fi, err := e.Info(); err == nil && fi.Mode().IsRegular() && old(fi, usageSweepAge) {
				_ = os.Remove(filepath.Join(dir, e.Name()))
				continue
			}
			left++
		}
		if left == 0 {
			_ = os.Remove(dir)
		}
	}
	accounts := filepath.Join(usageDir(root), "accounts")
	ents, _ := os.ReadDir(accounts)
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if (strings.HasPrefix(e.Name(), ".latest-") && old(fi, time.Minute)) || old(fi, usageSweepAge) {
			_ = os.Remove(filepath.Join(accounts, e.Name()))
		}
	}
}

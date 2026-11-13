package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
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
// line was written, and whether that line's figures differ from this
// session's line before: only then is it news for the account's latest
// figure (an idle session re-rendering an old figure is not).
func appendSnapshot(root string, s usageSnapshot, now time.Time) (wrote, newFigures bool) {
	path := sessionLogPath(root, s.SessionID, now)
	if !mkdirsReal(usageDir(root), filepath.Dir(filepath.Dir(path)), filepath.Dir(path)) {
		return false, false
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return false, false
	}
	defer f.Close()
	appendOpened(path)
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return false, false
	}
	// A render that opened the log just before another rotated it holds
	// the old file: it writes nothing rather than renaming the new one.
	if held, err := f.Stat(); err != nil {
		return false, false
	} else if cur, err := os.Lstat(path); err != nil || !os.SameFile(held, cur) {
		return false, false
	}
	last := lastSnapshot(f)
	if !worthWriting(s, last) {
		return false, false
	}
	newFigures = len(s.RateLimits) > 0 && (last == nil || string(last.RateLimits) != string(s.RateLimits))
	if fi, err := f.Stat(); err == nil && fi.Size() >= usageRotateBytes {
		// Under the lock, so no other render appends to the old file after
		// the rename; the new file is opened with the same rules.
		if os.Rename(path, strings.TrimSuffix(path, ".jsonl")+".1.jsonl") != nil {
			return false, false
		}
		nf, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			return false, false
		}
		defer nf.Close()
		f = nf
	}
	line, err := json.Marshal(s)
	if err != nil {
		return false, false
	}
	// One write: a line is whole or absent.
	if _, err = f.Write(append(line, '\n')); err != nil {
		return false, false
	}
	return true, newFigures
}

// appendOpened runs between opening a log and locking it; a hook for tests.
var appendOpened = func(string) {}

// mkdirsReal makes each folder in turn, each inside the one before, and
// reports whether all are folders themselves: a symbolic link at any of
// them stops it before anything is made through the link.
func mkdirsReal(paths ...string) bool {
	for _, p := range paths {
		if os.MkdirAll(p, 0o700) != nil || !realDirs(p) {
			return false
		}
	}
	return true
}

// realDirs reports whether every path is a directory itself, not a
// symbolic link to one: cpb writes and sweeps under .state/usage only.
func realDirs(paths ...string) bool {
	for _, p := range paths {
		fi, err := os.Lstat(p)
		if err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}

// latestFigure is accounts/<hash>.json: the newest figure seen for one
// usage key, which SHOW USAGE and the status line block read.
type latestFigure struct {
	V          int             `json:"v"`
	Key        string          `json:"key"`
	Account    string          `json:"account"`
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

// writeLatest records s, a session's new figures, as its key's newest
// figure, unless the one there is newer. For a window that resets at the
// same time, the higher use stands: two sessions of one account seeing the
// window at different moments never lower it. A race between two sessions
// of one key costs at most a figure a moment older; the file is always
// whole.
func writeLatest(root string, s usageSnapshot) {
	if len(s.RateLimits) == 0 || s.Key == "" {
		return
	}
	path := latestPath(root, s.Key)
	rates := s.RateLimits
	at, _ := time.Parse(time.RFC3339Nano, s.At)
	// A figure there that claims to be seen later than now (a clock that
	// was ahead) is no figure: it never holds back a real one.
	if old, ok := readLatest(path); ok {
		oldAt, _ := time.Parse(time.RFC3339Nano, old.SeenAt)
		if !oldAt.After(at.Add(time.Minute)) {
			rates = keepNewerWindow(old.RateLimits, oldAt, rates, at)
		}
	}
	b, err := json.Marshal(latestFigure{V: usageVersion, Key: s.Key, Account: s.Account,
		SeenAt: s.At, SessionID: s.SessionID, Playbook: s.Playbook, ConfigDir: s.ConfigDir, RateLimits: rates})
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if !mkdirsReal(usageDir(root), dir) {
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

// pathShown reports whether a field holding a folder path (or a playbook,
// which for cpb start is a folder) can be printed: valid UTF-8 and no
// control character. Folder names may hold zero-width characters.
func pathShown(p string) bool {
	return utf8.ValidString(p) && !hasControl(p)
}

// keepNewerWindow is now's rate limits (seen at nowSeen), with each window
// of old's kept where old's is the newer: now's window had already reset
// when now was seen (a session's first render may carry such a window)
// and old's resets later, or both reset at the same time and old's use is
// higher. old's reset counts only up to its seen time plus the window's
// length.
func keepNewerWindow(old json.RawMessage, oldSeen time.Time, now json.RawMessage, nowSeen time.Time) json.RawMessage {
	var o, n map[string]json.RawMessage
	if json.Unmarshal(old, &o) != nil || json.Unmarshal(now, &n) != nil {
		return now
	}
	for k, length := range usageWindowLength {
		var ow, nw usageWindow
		if json.Unmarshal(o[k], &ow) != nil || json.Unmarshal(n[k], &nw) != nil {
			continue
		}
		oldReset := min(ow.ResetsAt, oldSeen.Add(length).Unix())
		stale := nw.ResetsAt <= nowSeen.Unix() && oldReset > nw.ResetsAt
		if stale || (ow.ResetsAt == nw.ResetsAt && ow.UsedPercentage > nw.UsedPercentage) {
			n[k] = o[k]
		}
	}
	b, err := json.Marshal(n)
	if err != nil {
		return now
	}
	return b
}

// readLatest reads one latest file, defensively: every name in it has the
// shape a launch writes, so nothing from it reaches a terminal raw.
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
	if json.Unmarshal(b, &l) != nil || l.V != usageVersion || !validRecordedAccount(l.Key) || !validRecordedAccount(l.Account) ||
		!pathShown(l.Playbook) || !pathShown(l.ConfigDir) ||
		(l.SessionID != "" && !grammar.ValidSessionID(l.SessionID)) {
		return latestFigure{}, false
	}
	if _, err := time.Parse(time.RFC3339Nano, l.SeenAt); err != nil {
		return latestFigure{}, false
	}
	return l, true
}

// forgetUsage removes key's latest figure: its account was dropped, or now
// draws on another credential.
func forgetUsage(root, key string) {
	_ = os.Remove(latestPath(root, key))
}

// usageClock is the time latest figures are read at; a variable for tests.
var usageClock = time.Now

// readLatestFigures is every latest figure, by usage key: each from the
// file its key's hash names, never one seen later than now, its windows
// bounded (boundRates).
func readLatestFigures(root string) map[string]latestFigure {
	out := map[string]latestFigure{}
	dir := filepath.Join(usageDir(root), "accounts")
	if !realDirs(usageDir(root), dir) {
		return out
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	now := usageClock()
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		l, ok := readLatest(filepath.Join(dir, e.Name()))
		if !ok || e.Name() != filepath.Base(latestPath(root, l.Key)) {
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, l.SeenAt)
		if at.After(now.Add(time.Minute)) {
			continue
		}
		l.RateLimits = boundRates(l.RateLimits, at)
		out[l.Key] = l
	}
	return out
}

// usageWindowLength is how long a window Claude Code names lasts.
var usageWindowLength = map[string]time.Duration{"five_hour": 5 * time.Hour, "seven_day": 7 * 24 * time.Hour}

// boundRates is raw, seen at seen, with each window's numbers bounded: use
// between 0 and 1000 percent, and a reset no later than seen plus the
// window's length (a figure cannot hold an account full past its window).
// Other keys pass.
func boundRates(raw json.RawMessage, seen time.Time) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	for k, length := range usageWindowLength {
		var w map[string]json.RawMessage
		if json.Unmarshal(m[k], &w) != nil {
			continue
		}
		var used, resets float64
		if json.Unmarshal(w["used_percentage"], &used) != nil || json.Unmarshal(w["resets_at"], &resets) != nil ||
			math.IsNaN(used) || math.IsNaN(resets) {
			delete(m, k)
			continue
		}
		used = math.Max(0, math.Min(used, 1000))
		resets = math.Max(0, math.Min(resets, float64(seen.Add(length).Unix())))
		w["used_percentage"], _ = json.Marshal(used)
		w["resets_at"], _ = json.Marshal(int64(resets))
		m[k], _ = json.Marshal(w)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return b
}

// sweepUsage removes logs and latest figures nobody wrote for
// usageSweepAge, month directories left empty, and temporary files a
// killed writer left. A launch runs it; the status line step never does.
func sweepUsage(root string, now time.Time) {
	old := func(fi os.FileInfo, age time.Duration) bool { return now.Sub(fi.ModTime()) > age }
	sessions := filepath.Join(usageDir(root), "sessions")
	var months []os.DirEntry
	if realDirs(usageDir(root), sessions) {
		months, _ = os.ReadDir(sessions)
	}
	for _, m := range months {
		// A DirEntry's type is the link's own: a symbolic link is skipped.
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
	var ents []os.DirEntry
	if realDirs(usageDir(root), accounts) {
		ents, _ = os.ReadDir(accounts)
	}
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

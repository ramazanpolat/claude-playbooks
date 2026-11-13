package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// Session labels (SPEC.md, "Session labels"): short key=value pairs a
// session carries, from two sources never mixed. Launch labels are the
// person's own, fixed at launch (--label); session labels are claims made
// from inside a live session (cpb session label, from a hook or a tool).
// Both reach the status line's cpb key, SHOW SESSIONS and the usage log.
// Neither may hold what never goes in a label: a credential, an e-mail
// address, or a character a terminal does not show as written.

var (
	labelKeyShape = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
	// emailShape is a word shaped like an e-mail address.
	emailShape = regexp.MustCompile(`[^\s@<>()",;]+@[^\s@<>()",;]+\.[A-Za-z]{2,}`)
)

const (
	labelValueMax   = 200
	labelsPerSource = 16
	labelsBytesMax  = 4 << 10
	// labelLockWait is the longest a label write waits for another writer
	// of the same session: hooks call it, so a session start never waits
	// long on it.
	labelLockWait = 400 * time.Millisecond
	// exitNotASession is cpb session label's exit code when the caller is
	// not in a session cpb launched, so a hook can ignore exactly that.
	exitNotASession = 3
)

// labelKeyProblem is why k cannot be a label's key, "" when it can.
func labelKeyProblem(k string) string {
	if !labelKeyShape.MatchString(k) {
		return "a label key is a-z, 0-9, '.', '_' and '-', starting with a letter or a digit, at most 32 characters"
	}
	return ""
}

// labelValueProblem is why v cannot be a label's value, "" when it can.
// It never quotes the value.
func labelValueProblem(v string) string {
	switch {
	case len(v) > labelValueMax:
		return fmt.Sprintf("is %d bytes; a label value is at most %d", len(v), labelValueMax)
	case !utf8.ValidString(v):
		return "is not UTF-8"
	case strings.IndexFunc(v, hidden) >= 0:
		return "holds a control, direction or invisible character (a label value is one line, shown as written)"
	case emailShape.MatchString(v):
		return "looks like an e-mail address, which never goes in a label"
	}
	if c := askRequestCredential(v); c != "" {
		return "looks like a credential (" + c + "), which never goes in a label"
	}
	return ""
}

// parseTypedLabel reads key=value as typed: a problem is refused with a
// message that names the key, never the value.
func parseTypedLabel(s string) (string, string, error) {
	k, v, ok := strings.Cut(s, "=")
	if !ok {
		return "", "", errors.New("a label is key=value; the argument has no '='")
	}
	if p := labelKeyProblem(k); p != "" {
		return "", "", fmt.Errorf("label key %q: %s", k, p)
	}
	if p := labelValueProblem(v); p != "" {
		return "", "", fmt.Errorf("label %s: the value %s", k, p)
	}
	return k, v, nil
}

// checkLabelSet refuses a set of labels over the caps: how many, and how
// big together.
func checkLabelSet(m map[string]string) error {
	if len(m) > labelsPerSource {
		return fmt.Errorf("%d labels; at most %d", len(m), labelsPerSource)
	}
	if b, _ := json.Marshal(m); len(b) > labelsBytesMax {
		return fmt.Errorf("the labels are %d bytes together; at most %d", len(b), labelsBytesMax)
	}
	return nil
}

// validLabels reports whether m, read from a file, holds only labels a
// launch or cpb session label writes.
func validLabels(m map[string]string) bool {
	if checkLabelSet(m) != nil {
		return false
	}
	for k, v := range m {
		if labelKeyProblem(k) != "" || labelValueProblem(v) != "" {
			return false
		}
	}
	return true
}

// label_vars (SPEC.md, "Session labels"): the exact names of environment
// variables whose values a launch copies into launch labels, as
// env.<name in lower case>. Only names the person listed, read once at
// launch, each value checked as any label is.
const labelVarsMax = 8

var labelVarNameShape = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,27}$`)

// labelVarNameProblem is why n cannot be a name of label_vars, "" when it
// can: not a variable's exact name, cpb's own or an account's variable,
// or a name that may hold a credential, whatever its value.
func labelVarNameProblem(n string) string {
	if !labelVarNameShape.MatchString(n) {
		return fmt.Sprintf("%q is not an environment variable's exact name (letters, digits and '_', not starting with a digit, at most 28 characters; no pattern)", n)
	}
	up := strings.ToUpper(n)
	if strings.HasPrefix(up, "CPB_") || up == "CLAUDE_CONFIG_DIR" || account.IsReserved(up) {
		return n + " is cpb's own or an account's variable"
	}
	if manifest.LooksLikeSecretKey(n) || strings.Contains(up, "KEY") || strings.Contains(up, "COOKIE") || slices.Contains(strings.Split(up, "_"), "SESSION") {
		return n + " names a variable that may hold a credential"
	}
	return ""
}

// checkLabelVars refuses a label_vars list: at most labelVarsMax names,
// each one label_vars takes, none twice (in any case: two names would be
// one label).
func checkLabelVars(names []string) error {
	if len(names) > labelVarsMax {
		return fmt.Errorf("label_vars lists %d names; at most %d", len(names), labelVarsMax)
	}
	seen := map[string]bool{}
	for _, n := range names {
		if p := labelVarNameProblem(n); p != "" {
			return fmt.Errorf("label_vars: %s", p)
		}
		if seen[labelVarKey(n)] {
			return fmt.Errorf("label_vars names %s twice", n)
		}
		seen[labelVarKey(n)] = true
	}
	return nil
}

// labelVarKey is the launch label a name of label_vars gives.
func labelVarKey(n string) string { return "env." + strings.ToLower(n) }

// splitLabelVars reads a list property's value ('A, B') into its names.
func splitLabelVars(v string) []string {
	var out []string
	for _, n := range strings.Split(v, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// withLabelVars adds to typed (the --label labels) one label per name of
// label_vars: DEFAULTS' first, then the playbook's, each name once. A
// typed label of the same key wins. A name it does not take (a file
// edited by hand), an unset variable, a value a label refuses, and a label
// over the caps are left out, each named in the notes by its key, never
// its value.
func withLabelVars(typed map[string]string, names []string, lookup func(string) (string, bool)) (map[string]string, []string) {
	out := map[string]string{}
	for k, v := range typed {
		out[k] = v
	}
	var notes []string
	done := map[string]bool{}
	for _, n := range names {
		if p := labelVarNameProblem(n); p != "" {
			notes = append(notes, fmt.Sprintf("%s (not a name label_vars takes)", strings.ToLower(n)))
			continue
		}
		k := labelVarKey(n)
		if done[k] {
			continue
		}
		done[k] = true
		if _, ok := typed[k]; ok {
			continue
		}
		v, ok := lookup(n)
		if !ok {
			notes = append(notes, k+" (unset)")
			continue
		}
		if p := labelValueProblem(v); p != "" {
			notes = append(notes, k+" (the value "+p+")")
			continue
		}
		out[k] = v
		if checkLabelSet(out) != nil {
			delete(out, k)
			notes = append(notes, k+" (over the label caps)")
		}
	}
	if len(out) == 0 {
		out = nil
	}
	return out, notes
}

// launchLabelsOf is the launch labels the tokens give: each --label, a
// later one for a key winning. Their values were checked when parsed.
func launchLabelsOf(tokens []launchToken) map[string]string {
	var out map[string]string
	for _, t := range tokens {
		if t.flag != "--label" {
			continue
		}
		k, v, _ := strings.Cut(t.value, "=")
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}

// sessionLabelsFile is usage/labels/<session id>.json.
type sessionLabelsFile struct {
	V         int               `json:"v"`
	SessionID string            `json:"session_id"`
	Labels    map[string]string `json:"labels"`
}

func labelsDir(root string) string { return filepath.Join(usageDir(root), "labels") }

func labelsPath(root, sessionID string) string {
	return filepath.Join(labelsDir(root), sessionID+".json")
}

// readSessionLabels is the session labels of sessionID, nil when it has
// none or the file is not one cpb wrote.
func readSessionLabels(root, sessionID string) map[string]string {
	if !grammar.ValidSessionID(sessionID) || !realDirs(usageDir(root), labelsDir(root)) {
		return nil
	}
	p := labelsPath(root, sessionID)
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 2*labelsBytesMax {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var f sessionLabelsFile
	if json.Unmarshal(b, &f) != nil || f.V != 1 || f.SessionID != sessionID || len(f.Labels) == 0 || !validLabels(f.Labels) {
		return nil
	}
	return f.Labels
}

// errLabelsBusy is a label write that waited its longest for another.
var errLabelsBusy = errors.New("another label write of this session is in progress; try again")

// updateSessionLabels sets and removes session labels of sessionID under
// the session's lock, waiting at most labelLockWait, and returns them as
// they are after.
func updateSessionLabels(root, sessionID string, set map[string]string, unset []string) (map[string]string, error) {
	if !grammar.ValidSessionID(sessionID) {
		return nil, fmt.Errorf("%q is not a session id", sessionID)
	}
	if !mkdirsReal(usageDir(root), labelsDir(root)) {
		return nil, fmt.Errorf("%s is not a folder cpb can write labels in", labelsDir(root))
	}
	lock, err := os.OpenFile(filepath.Join(labelsDir(root), "."+sessionID+".lock"), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	deadline := time.Now().Add(labelLockWait)
	for syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		if time.Now().After(deadline) {
			return nil, errLabelsBusy
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The lock's time is the last write's, so the usage sweep removes a
	// lock only once nobody has written the session's labels for as long
	// as the rest.
	now := time.Now()
	_ = os.Chtimes(lock.Name(), now, now)
	labels := map[string]string{}
	for k, v := range readSessionLabels(root, sessionID) {
		labels[k] = v
	}
	for k, v := range set {
		labels[k] = v
	}
	for _, k := range unset {
		delete(labels, k)
	}
	if err := checkLabelSet(labels); err != nil {
		return nil, err
	}
	p := labelsPath(root, sessionID)
	if len(labels) == 0 {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return labels, nil
	}
	b, err := json.Marshal(sessionLabelsFile{V: 1, SessionID: sessionID, Labels: labels})
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(labelsDir(root), ".labels-*")
	if err != nil {
		return nil, err
	}
	_, werr := tmp.Write(b)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), 0o600)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), p)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
		return nil, werr
	}
	return labels, nil
}

// labelSessionDepth is how far up its ancestry cpb session label looks for
// the session it runs in: a hook is claude's child or grandchild, a tool a
// little further.
const labelSessionDepth = 8

// errNotASession is a caller that is not in a session cpb launched.
var errNotASession = errors.New("not in a session cpb launched (no ancestor of this process is a claude session of a playbook)")

// callerSession is the session the calling process runs in: the first of
// its ancestors, at most labelSessionDepth up, whose Claude Code session
// file, in a config directory cpb knows, names it with a matching start
// time.
func callerSession() (sessionFile, error) {
	dirs, err := knownSessionDirs()
	if err != nil {
		return sessionFile{}, err
	}
	pid := os.Getppid()
	for i := 0; i < labelSessionDepth && pid > 1; i++ {
		info, ok := procStarts([]int{pid})[pid]
		if !ok {
			break
		}
		for _, d := range dirs {
			if f, ok := readSessionFileOf(d.path, pid); ok && sameStart(info.start, f.ProcStart) && grammar.ValidSessionID(f.SessionID) {
				return f, nil
			}
		}
		pid = info.ppid
	}
	return sessionFile{}, errNotASession
}

// liveSessionByID is the live session sessionID, in a config directory
// cpb knows.
func liveSessionByID(sessionID string) (sessionFile, error) {
	dirs, err := knownSessionDirs()
	if err != nil {
		return sessionFile{}, err
	}
	for _, s := range readSessionFiles(dirs) {
		// Only a session confirmed live: not one recorded in another pid
		// domain, nor one whose file records no start time (a reused pid).
		if s.f.SessionID == sessionID && s.state == live {
			return s.f, nil
		}
	}
	return sessionFile{}, fmt.Errorf("no live session %s in a playbook", sessionID)
}

// sortedLabelKeys is m's keys in order.
func sortedLabelKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// withPlaybooksRoot is env with the launch's playbooks root as
// CPB_PLAYBOOKS_DIR (absolute) when the root came from --playbooks-dir,
// replacing an inherited one the flag outranked: a cpb inside the session
// (cpb session label, SHOW SESSIONS) then reads the same root. A root from
// the environment is there already; the default needs nothing. Layers can
// never set the variable (it is reserved); this is cpb's own.
func withPlaybooksRoot(env []string) []string {
	if config.PlaybooksDir == "" {
		return env
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, config.PlaybooksDirEnv+"=") {
			out = append(out, kv)
		}
	}
	return append(out, config.PlaybooksDirEnv+"="+absPath(config.PlaybooksDir))
}

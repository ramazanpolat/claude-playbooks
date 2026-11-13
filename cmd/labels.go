package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
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

// labelInvisible reports a character that is not plain visible text:
// what the by-hand line hides (control and direction characters), every
// other format character (Cf: zero-width spaces and joiners, the
// soft hyphen, the byte-order mark, the tag characters), the line and
// paragraph separators, private-use characters, the variation selectors,
// the combining grapheme joiner, the Hangul fillers and the Mongolian
// free variation selectors.
func labelInvisible(r rune) bool {
	switch {
	case hidden(r), unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Co):
		return true
	case r >= 0xFE00 && r <= 0xFE0F, r >= 0xE0100 && r <= 0xE01EF: // variation selectors
		return true
	case r == 0x034F: // combining grapheme joiner
		return true
	case r == 0x115F, r == 0x1160, r == 0x3164, r == 0xFFA0: // Hangul fillers
		return true
	case r >= 0x180B && r <= 0x180F: // Mongolian free variation selectors, vowel separator
		return true
	case r >= 0xE0000 && r <= 0xE007F: // tag characters, assigned or not
		return true
	}
	return false
}

// labelValueProblem is why v cannot be a label's value, whatever its key,
// "" when it can. It never quotes the value.
func labelValueProblem(v string) string {
	switch {
	case v == "":
		return "is empty (--unset <key> removes a label)"
	case len(v) > labelValueMax:
		return fmt.Sprintf("is %d bytes; a label value is at most %d", len(v), labelValueMax)
	case !utf8.ValidString(v):
		return "is not UTF-8"
	case strings.IndexFunc(v, labelInvisible) >= 0:
		return "holds a character that is not plain visible text (a control, format, direction, separator, private-use or variation character); a label is one line of plain visible text"
	case emailShape.MatchString(v):
		return "looks like an e-mail address, which never goes in a label"
	}
	return ""
}

// labelProblem is why k=v cannot be a label, "" when it can: the value's
// own problems, then the credential check on key and value together, as
// cpb ask checks a request (a key that names a secret, password=…, is
// refused with its value; commit=<hash> is a hash).
func labelProblem(k, v string) string {
	if p := labelValueProblem(v); p != "" {
		return p
	}
	if c := askRequestCredential(k + "=" + v); c != "" {
		return "looks like a credential (" + c + "), which never goes in a label"
	}
	return ""
}

// labelKeyRefusal says a typed key is not valid without echoing it: what
// stands where the key goes may be a value.
func labelKeyRefusal(k string) error {
	return fmt.Errorf("the label key is not valid (%d characters): %s", utf8.RuneCountInString(k), labelKeyProblem(k))
}

// parseTypedLabel reads key=value as typed: a problem is refused with a
// message that names a valid key, never a value.
func parseTypedLabel(s string) (string, string, error) {
	k, v, ok := strings.Cut(s, "=")
	if !ok {
		return "", "", errors.New("a label is key=value; the argument has no '='")
	}
	if labelKeyProblem(k) != "" {
		return "", "", labelKeyRefusal(k)
	}
	if p := labelProblem(k, v); p != "" {
		return "", "", fmt.Errorf("label %s: the value %s", k, p)
	}
	return k, v, nil
}

// checkLaunchLabels checks the --label tokens of a launch, each and
// together, before anything starts or is forwarded. It reads no file.
func checkLaunchLabels(tokens []launchToken) error {
	for _, t := range tokens {
		if t.flag != "--label" {
			continue
		}
		if _, _, err := parseTypedLabel(t.value); err != nil {
			return fmt.Errorf("--label: %w", err)
		}
	}
	if err := checkLabelSet(launchLabelsOf(tokens)); err != nil {
		return fmt.Errorf("--label: %w", err)
	}
	return nil
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
		if labelKeyProblem(k) != "" || labelProblem(k, v) != "" {
			return false
		}
	}
	return true
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

package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"
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

// label_vars (SPEC.md, "Session labels"): the exact names of environment
// variables whose values a launch copies into launch labels, as
// env.<name in lower case>. Only names the person listed, read once at
// launch, each value checked as any label is and more: a value from the
// environment is not typed by the person who sees it, so a URL, a path and
// an address are refused too.
const labelVarsMax = 8

var (
	labelVarNameShape = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,27}$`)
	// labelVarWords refuse a name holding them: it may hold a credential.
	labelVarWords = []string{"KEY", "COOKIE", "WEBHOOK", "DSN", "SALT", "SIGNING"}
	// labelVarIdentities are names whose values say who or where someone
	// is, whatever check a value passes.
	labelVarIdentities = []string{"USER", "LOGNAME", "USERNAME", "HOSTNAME", "HOST", "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GITHUB_ACTOR"}
	urlShape  = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://`)
	ipv4Shape = regexp.MustCompile(`(^|[^0-9.])[0-9]{1,3}(\.[0-9]{1,3}){3}($|[^0-9.])`)
)

// labelVarShowable reports whether a list entry may be shown as written: a
// variable's name, not shaped like a credential. Any other entry is named
// by its length only: what stands where a name goes may be a value.
func labelVarShowable(n string) bool {
	return labelVarNameShape.MatchString(n) && askRequestCredential(n) == ""
}

// labelVarShown is a list entry as SHOW, EXPLAIN and SELECT show it.
func labelVarShown(n string) string {
	if labelVarShowable(n) {
		return n
	}
	return fmt.Sprintf("<an entry of %d characters>", utf8.RuneCountInString(n))
}

// labelVarNameProblem is why n cannot be a name of label_vars, "" when it
// can: not a variable's exact name, shaped like a credential, cpb's own or
// an account's variable, one that says who or where someone is, or a name
// that may hold a credential, whatever its value.
func labelVarNameProblem(n string) string {
	if !labelVarNameShape.MatchString(n) {
		return fmt.Sprintf("an entry of %d characters is not an environment variable's exact name (letters, digits and '_', not starting with a digit, at most 28 characters; no pattern)", utf8.RuneCountInString(n))
	}
	if askRequestCredential(n) != "" {
		return fmt.Sprintf("an entry of %d characters is shaped like a credential, not a variable's name", utf8.RuneCountInString(n))
	}
	up := strings.ToUpper(n)
	if strings.HasPrefix(up, "CPB_") || up == "CLAUDE_CONFIG_DIR" || account.IsReserved(up) {
		return n + " is cpb's own or an account's variable"
	}
	if slices.Contains(labelVarIdentities, up) {
		return n + " says who or where someone is"
	}
	if manifest.LooksLikeSecretKey(n) || slices.Contains(strings.Split(up, "_"), "SESSION") || slices.ContainsFunc(labelVarWords, func(w string) bool { return strings.Contains(up, w) }) {
		return n + " names a variable that may hold a credential"
	}
	return ""
}

// labelVarValueProblem is why v, the value of the variable that gives the
// label k, cannot be a label, "" when it can: a label's own rules, and a
// URL, a path or an address, which a value typed with --label may hold. It
// names a kind, never the value.
func labelVarValueProblem(k, v string) string {
	switch {
	case urlShape.MatchString(v):
		return "a URL"
	case strings.HasPrefix(v, "/") || strings.HasPrefix(v, "~"):
		return "a path"
	case ipv4Shape.MatchString(v) || hasIPv6(v):
		return "an address"
	}
	if p := labelProblem(k, v); p != "" {
		switch {
		case strings.Contains(p, "e-mail"):
			return "an e-mail address"
		case strings.Contains(p, "credential"):
			return "a credential"
		case strings.Contains(p, "plain visible text"):
			return "not plain visible text"
		}
		return "not a label value (" + strings.SplitN(p, ";", 2)[0] + ")"
	}
	return ""
}

// hasIPv6 reports a word of v that is an IPv6 address, with or without a
// zone, brackets or a port.
func hasIPv6(v string) bool {
	for _, w := range strings.FieldsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune("[],;=@/\"'", r) }) {
		if i := strings.IndexByte(w, '%'); i > 0 {
			w = w[:i]
		}
		if strings.Count(w, ":") >= 2 {
			if ip := net.ParseIP(w); ip != nil && ip.To4() == nil {
				return true
			}
		}
	}
	return false
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

// splitLabelVars reads a list property's value (the grammar's items,
// comma-joined) into its entries as written: nothing trimmed, an empty one
// kept, so the check refuses what the person did not write as a name.
func splitLabelVars(v string) []string {
	return strings.Split(v, ",")
}

// labelVarState is what a launch does with one name of label_vars: Name
// as it may be shown, Label the key it gives (none for a refused name),
// State one of copied, unset, empty, typed (a --label of the key wins),
// refused name, refused value and over the caps, and Why the kind of a
// refused value. Never a value.
type labelVarState struct {
	Name  string `json:"name"`
	Label string `json:"label,omitempty"`
	State string `json:"state"`
	Why   string `json:"why,omitempty"`
}

// labelVarsAt is the labels a launch takes: typed (the --label labels) and
// one label per name of label_vars, DEFAULTS' first, then the playbook's,
// each name once, with what it did with each name.
func labelVarsAt(typed map[string]string, names []string, lookup func(string) (string, bool)) (map[string]string, []labelVarState) {
	out := map[string]string{}
	for k, v := range typed {
		out[k] = v
	}
	var states []labelVarState
	done := map[string]bool{}
	for _, n := range names {
		if labelVarNameProblem(n) != "" {
			states = append(states, labelVarState{Name: labelVarShown(n), State: "refused name"})
			continue
		}
		k := labelVarKey(n)
		if done[k] {
			continue
		}
		done[k] = true
		st := labelVarState{Name: n, Label: k}
		v, ok := lookup(n)
		switch _, isTyped := typed[k]; {
		case isTyped:
			st.State = "typed"
		case !ok:
			st.State = "unset"
		case v == "":
			st.State = "empty"
		default:
			if p := labelVarValueProblem(k, v); p != "" {
				st.State, st.Why = "refused value", p
				break
			}
			out[k] = v
			st.State = "copied"
			if checkLabelSet(out) != nil {
				delete(out, k)
				st.State = "over the caps"
			}
		}
		states = append(states, st)
	}
	if len(out) == 0 {
		out = nil
	}
	return out, states
}

// labelVarsLeftOut is the launch's one line on what it left out: a refused
// name, a refused value (by kind) or a label over the caps, by key, never
// by value; "" when nothing was. An unset or empty variable is normal and
// says nothing.
func labelVarsLeftOut(states []labelVarState) string {
	var parts []string
	for _, s := range states {
		switch s.State {
		case "refused name":
			parts = append(parts, s.Name+" (not a name label_vars takes)")
		case "refused value":
			parts = append(parts, s.Label+" (the value is "+s.Why+")")
		case "over the caps":
			parts = append(parts, s.Label+" (over the label caps)")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "cpb: label_vars left out: " + strings.Join(parts, ", ")
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
	proc := procWalker()
	for i := 0; i < labelSessionDepth && pid > 1; i++ {
		info, ok := proc(pid)
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

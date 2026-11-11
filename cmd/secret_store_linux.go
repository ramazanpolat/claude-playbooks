//go:build linux

package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// The Linux store: the Secret Service (GNOME Keyring, KWallet and others)
// through libsecret's secret-tool, attributes service=cpb and name=<name>,
// when one can be reached; otherwise the file store (secret_store_file.go),
// on a server or in a container with no keyring daemon, or with no
// secret-tool at all. Any process of the same user can ask the Secret
// Service for the value while the collection is unlocked, and read the
// file store's files (SPEC.md, "Secrets").

// lookSecretTool finds secret-tool (a test replaces it with a stub).
var lookSecretTool = func() (string, error) { return exec.LookPath("secret-tool") }

// secretServiceMaxValue is the longest value kept whole: secret-tool store
// keeps the first 8192 bytes of its stdin and drops the rest, exiting 0
// (libsecret 0.21, measured), so the helper refuses longer values first.
const secretServiceMaxValue = 4096

type secretServiceStore struct{ tool string }

// linuxStore is the Secret Service when it can answer, then the file
// store. ss is nil when secret-tool is not installed.
type linuxStore struct {
	ss   *secretServiceStore
	file fileStore
}

// helperSecretTool is the secret-tool the running cpb found and handed the
// helper ("" when it found none): the helper never looks it up on a PATH a
// playbook's launch environment may have changed.
var helperSecretTool string

func setHelperSecretTool(p string) { helperSecretTool = p }

// secretToolPath is the secret-tool this store uses, or "": in helper mode
// the one the running cpb handed over, otherwise cpb's own lookup.
func secretToolPath() string {
	if helperHanded {
		return helperSecretTool
	}
	if tool, err := lookSecretTool(); err == nil {
		return tool
	}
	return ""
}

// storeToolForHelper is the secret-tool handed to the default helper.
func storeToolForHelper() string {
	if tool, err := lookSecretTool(); err == nil {
		return tool
	}
	return ""
}

func osDefaultSecretStore() (osSecretStore, error) {
	dir, err := secretsDir()
	if err != nil {
		return nil, err
	}
	st := linuxStore{file: fileStore{dir: dir}}
	if tool := secretToolPath(); tool != "" {
		st.ss = &secretServiceStore{tool: tool}
	}
	return st, nil
}

// keyringState is what the Secret Service says about a name.
type keyringState int

const (
	keyringUnreachable keyringState = iota // no secret-tool, or no Secret Service answers
	keyringAbsent                          // it answers, and holds no such item
	keyringFound                           // it holds the item, unlocked
	keyringLocked                          // it holds the item in a locked collection
	keyringBypassed                        // it fails outright, and CPB_SECRET_FILE_STORE=1 sends values to the file
)

// fileAnswers reports whether the file store answers for a name in state k.
func fileAnswers(k keyringState) bool { return k == keyringUnreachable || k == keyringBypassed }

// keyring asks the Secret Service about name. With deep, a missing answer
// is checked with search, which tells a locked item from a missing one but
// prints the values of unlocked matches: only the helper's own process
// asks deep. Without it (target and where, which cpb's own process calls)
// only lookup runs, its output going to the null device, so no value
// passes through that process; a locked item then reads as absent.
//
// Only no secret-tool at all, or a definite "no Secret Service"
// (noSecretService: lookup's own two messages for it) is
// keyringUnreachable with no error, and lets the file store answer. Any
// other failure (a tool that did not answer in time, one that crashed,
// exited otherwise or said anything else) is an error: the store cannot
// answer, and the value goes nowhere, neither to the keyring nor the file.
func (s linuxStore) keyring(name string, deep bool) (keyringState, error) {
	if s.ss == nil {
		return keyringUnreachable, nil
	}
	found, noService, err := s.ss.lookup(name)
	var ue *unavailableError
	switch {
	case err != nil && errors.As(err, &ue) && ue.keyringFailed && fileStoreOptIn():
		// A keyring that fails outright (never one that is slow or locked),
		// and the person said to use the file store on this machine.
		return keyringBypassed, nil
	case err != nil:
		return keyringUnreachable, err
	case noService != "":
		return keyringUnreachable, nil
	case found:
		return keyringFound, nil
	case !deep:
		return keyringAbsent, nil
	}
	locked, err := s.ss.lockedItem(name)
	switch {
	case err != nil:
		return keyringUnreachable, err
	case locked:
		return keyringLocked, nil
	}
	return keyringAbsent, nil
}

// target is where name's value is kept, or would be kept when it is not
// stored yet: the keyring when it holds it, the file when that does, else
// the keyring when it can answer, else the file store and the file's path.
// A keyring that cannot answer is an error, never "the file".
func (s linuxStore) target(name string) (storeTarget, error) {
	k, err := s.keyring(name, false)
	if err != nil {
		return storeTarget{}, err
	}
	if k == keyringFound {
		return storeTarget{kind: storeSecretService}, nil
	}
	t := storeTarget{kind: storeFile, path: s.file.path(name)}
	switch {
	case s.ss == nil:
		t.why = "secret-tool is not installed"
	case k == keyringUnreachable:
		t.why = "no keyring can be reached"
	case k == keyringBypassed:
		t.why = "the keyring fails here, and " + fileStoreOptInEnv + "=1 sends values to the file"
	}
	inFile, ferr := s.file.exists(name)
	if (ferr == nil && inFile) || fileAnswers(k) {
		return t, nil
	}
	return storeTarget{kind: storeSecretService}, nil
}

func (s linuxStore) where(name string) string {
	t, err := s.target(name)
	if err != nil {
		return "your Secret Service keyring, which cannot answer now (" + err.Error() + ")"
	}
	return s.describe(name, t)
}

// describe words t for the store step's prompt.
func (s linuxStore) describe(name string, t storeTarget) string {
	switch {
	case t.kind == storeSecretService:
		return "your Secret Service keyring, as the item \"cpb " + name + "\" (service cpb, name " + name + ")"
	case s.ss == nil:
		return "the file " + t.path + ", readable by you (and root), since this machine has no keyring: secret-tool is not installed (installing it and a keyring such as GNOME Keyring or KWallet would keep it there instead)"
	case strings.HasPrefix(t.why, "the keyring fails"):
		return "the file " + t.path + ", readable by you (and root), since the keyring fails on this machine and " + fileStoreOptInEnv + "=1 is set"
	case t.why != "":
		return "the file " + t.path + ", readable by you (and root), since no keyring can be reached on this machine (a keyring such as GNOME Keyring or KWallet running in your session would keep it there instead)"
	default:
		return "the file " + t.path + ", readable by you (and root)"
	}
}

// maxValue is the Secret Service's limit, which the file store keeps too,
// so a value fits whichever store takes it.
func (s linuxStore) maxValue() int { return min(secretServiceMaxValue, fileStoreMaxValue) }

// sweepTemps sweeps the file store's temporary files of name.
func (s linuxStore) sweepTemps(name string) { s.file.sweepTemps(name) }

// readyToStore asks what a write of name would meet, with nothing that
// carries a value: a keyring that cannot answer or holds the name locked,
// or, where no keyring can be reached, a file store that cannot be made or
// trusted (a read-only home, an open mode). A keyring that answers asks for
// its own password when the value is stored, where it can.
func (s linuxStore) readyToStore(name string) error {
	k, err := s.keyring(name, true)
	switch {
	case err != nil:
		return err
	case k == keyringLocked:
		return unavailable("cpb:%s is in your Secret Service keyring, which is locked: unlock the keyring (log in to your desktop session, or unlock it in your keyring's tool), then run this again; nothing was asked for or stored", name)
	case fileAnswers(k):
		return s.file.ready()
	}
	return nil
}

// exists looks in the Secret Service when it can answer, then in the file.
// A name in both is the keyring's, and one stderr line says where the file
// copy is. A name in a locked keyring is a store that cannot answer, never
// "not stored", so --store never writes over it, in the keyring or beside
// it in the file. Another error means the file store could not be trusted
// (its mode, owner or a link) while the keyring did not hold the name.
func (s linuxStore) exists(name string) (bool, error) {
	k, err := s.keyring(name, true)
	if err != nil {
		return false, err
	}
	if k == keyringLocked {
		return false, fmt.Errorf("cpb:%s is in your Secret Service keyring, which is locked: unlock the keyring (log in to your desktop session, or unlock it in your keyring's tool), then run this again", name)
	}
	inFile, ferr := s.file.exists(name)
	if k == keyringFound {
		if ferr == nil && inFile {
			fmt.Fprintf(os.Stderr, "cpb:%s is also kept in the file %s; the keyring's value is the one used\n", name, s.file.path(name))
		}
		return true, nil
	}
	if ferr != nil {
		return false, ferr
	}
	return inFile, nil
}

// announceFileReads: a read the file serves while secret-tool is installed
// says so on stderr. --store's own read-back keeps quiet (it just wrote
// there, and its prompt said so).
var announceFileReads = true

// quietFileReads silences the line until the returned func is called.
func quietFileReads() (restore func()) {
	announceFileReads = false
	return func() { announceFileReads = true }
}

// read runs in the helper's own process (a launch, the read-back after a
// store), so it asks deep: a name in a locked keyring is a store that
// cannot answer, and the read stops there; it never falls back to a file
// copy of the same name, which the keyring's value outranks.
func (s linuxStore) read(name string) ([]byte, error) {
	k, err := s.keyring(name, true)
	if err != nil {
		return nil, err
	}
	switch k {
	case keyringFound:
		return s.ss.read(name)
	case keyringLocked:
		return nil, unavailable("cpb:%s is in your Secret Service keyring, which is locked: unlock the keyring (log in to your desktop session, or unlock it in your keyring's tool), then run this again", name)
	}
	v, err := s.file.read(name)
	if err == nil && s.ss != nil && announceFileReads {
		why := "the keyring does not hold it"
		switch k {
		case keyringUnreachable:
			why = "no keyring can be reached"
		case keyringBypassed:
			why = "the keyring fails here, and " + fileStoreOptInEnv + "=1 is set"
		}
		fmt.Fprintf(os.Stderr, "cpb:%s is read from the file %s: %s\n", name, s.file.path(name), why)
	}
	return v, err
}

// write goes to the Secret Service when it can answer, else to the file
// store; neither replaces a stored value, and a locked keyring item is
// never written over or beside. want, when set, is the store the person
// was told (the store step's prompt): a write that would go elsewhere now
// (the keyring gone, or back) stores nothing and cannot answer.
func (s linuxStore) write(name string, value []byte, want string) error {
	k, err := s.keyring(name, true)
	if err != nil {
		return err
	}
	kind := storeSecretService
	switch k {
	case keyringAbsent:
	case keyringUnreachable, keyringBypassed:
		kind = storeFile
	default:
		return fmt.Errorf("cpb:%s is in your Secret Service keyring already; nothing was stored", name)
	}
	if want != "" && want != kind {
		return unavailable("cpb:%s would now be written to %s, not to %s as you were told; nothing was stored. Run this again", name, storeKindWords(kind), storeKindWords(want))
	}
	if kind == storeFile {
		return s.file.write(name, value)
	}
	return s.ss.write(name, value)
}

func (s secretServiceStore) maxValue() int { return secretServiceMaxValue }

// readyToStore has nothing to ask on Linux: exists already tells a locked
// item from a missing one, and secret-tool store asks for the keyring's
// password itself where it can.
func (secretServiceStore) readyToStore() error { return nil }

// run runs secret-tool with args, bounded by toolTimeout; a run that does
// not end in time is killed and reported as a store that cannot answer.
func (s secretServiceStore) run(name string, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), toolTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, s.tool, args...)
	c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
	// The store's environment (cpb's own session bus and home, in helper
	// mode), and messages in English, which lookup's are matched against.
	c.Env = append(storeToolEnv(), "LC_ALL=C", "LANGUAGE=")
	// A child the tool left behind may hold its output open: Run returns
	// a second after the kill all the same.
	c.WaitDelay = time.Second
	err := c.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return unavailable("secret-tool did not answer within %s about cpb %s", toolTimeout, name)
	}
	return err
}

// exists asks lookup, whose exit status is 1 both when the item is not
// there and when no Secret Service can be reached (a server or a container
// with secret-tool but no keyring daemon, or no session bus). Only stderr
// tells them apart: lookup prints nothing there for a missing item, and its
// error otherwise (libsecret 0.21: "The name org.freedesktop.secrets was
// not provided by any .service files", "Cannot autolaunch D-Bus without
// X11 $DISPLAY"). A store that cannot be reached is an error (the helper
// then exits unavailableExit), never "not stored".
//
// A locked collection answers lookup the same way as a missing item, also
// when its unlock prompt was dismissed; secret-tool store would then
// replace the item once the collection is unlocked. So a missing answer is
// checked with search, which lists a locked item's path without unlocking
// it, and a locked item is a store that cannot answer, never "not stored".
func (s secretServiceStore) exists(name string) (bool, error) {
	found, noService, err := s.lookup(name)
	if err != nil || found {
		return found, err
	}
	if noService != "" {
		return false, unavailable("no Secret Service can be reached on this machine (secret-tool: %s), so cpb:%s cannot be checked or stored here; run a keyring such as GNOME Keyring or KWallet in this session, or configure a helper of your own (ALTER DEFAULTS SET secret_helper = '<command>')", noService, name)
	}
	locked, err := s.lockedItem(name)
	if err != nil {
		return false, err
	}
	if locked {
		return false, fmt.Errorf("cpb:%s is in your Secret Service keyring, which is locked: unlock the keyring (log in to your desktop session, or unlock it in your keyring's tool), then run this again", name)
	}
	return false, nil
}

// noSecretServiceMessages are what lookup prints, with exit 1, when no
// Secret Service can be reached (libsecret 0.21.4, LC_ALL=C, measured): a
// session bus with no keyring daemon on it, and no session bus at all.
// Only these mean "no keyring", and let the file store answer.
var noSecretServiceMessages = []string{
	"The name org.freedesktop.secrets was not provided by any .service files",
	"Cannot autolaunch D-Bus without X11 $DISPLAY",
}

// lookup is exists without the locked check: found; missing (or locked:
// exit 1, nothing on stderr); noService, a definite "no Secret Service"
// (exit 1 with one of noSecretServiceMessages, which it holds); or, for
// anything else (a
// tool that did not start, crashed, timed out, exited otherwise or said
// anything else), an error that cannot answer. lookup prints the value on
// stdout: it goes to the null device (a nil Stdout), never through this
// process. Its stderr holds only its own errors, never a value.
func (s secretServiceStore) lookup(name string) (found bool, noService string, err error) {
	var errb bytes.Buffer
	err = s.run(name, nil, nil, &errb, "lookup", "service", "cpb", "name", name)
	if err == nil {
		return true, "", nil
	}
	var ue *unavailableError
	if errors.As(err, &ue) {
		return false, "", err
	}
	why := toolError(errb.Bytes())
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		if why == "" {
			return false, "", nil
		}
		for _, m := range noSecretServiceMessages {
			if strings.Contains(why, m) {
				return false, m, nil
			}
		}
	}
	if why == "" {
		why = err.Error()
	}
	return false, "", &unavailableError{keyringFailed: true, msg: fmt.Sprintf("secret-tool could not answer about cpb:%s (%s); nothing was read or stored, in the keyring or in a file. Check your keyring, then run this again; or, on a machine whose keyring cannot be used, set %s=1 in your own shell (never in a playbook), and cpb keeps cpb:<name> values in the file store instead (plaintext, readable by you and root); or configure a helper of your own (ALTER DEFAULTS SET secret_helper = '<command>')", name, why, fileStoreOptInEnv)}
}

// lockedItem reports whether search lists an item for name: it lists a
// locked item's path and attributes without unlocking it, and for an
// unlocked one also its value. It is run without --unlock (which would
// prompt) and without --all. The output stays in this process (the
// helper), is scanned in place for an item line, with no copy made, and
// is cleared.
func (s secretServiceStore) lockedItem(name string) (bool, error) {
	// One array, grown up front, so no earlier copy of the output is left
	// behind uncleared as it grows.
	var out bytes.Buffer
	out.Grow(64 << 10)
	err := s.run(name, nil, &out, io.Discard, "search", "service", "cpb", "name", name)
	defer func() { b := out.Bytes(); clear(b[:cap(b)]) }()
	if err != nil {
		var ue *unavailableError
		if errors.As(err, &ue) {
			return false, err
		}
		return false, fmt.Errorf("secret-tool could not search the keyring for cpb %s (%v); nothing was stored", name, err)
	}
	for _, line := range bytes.Split(out.Bytes(), []byte("\n")) {
		if bytes.HasPrefix(line, []byte("[/")) {
			return true, nil
		}
	}
	return false, nil
}

// toolError is the first line of a tool's stderr, without its "<tool>: "
// prefix and at most 200 bytes, or "" when it printed nothing.
func toolError(stderr []byte) string {
	line, _, _ := bytes.Cut(bytes.TrimSpace(stderr), []byte("\n"))
	if _, rest, ok := bytes.Cut(line, []byte(": ")); ok && bytes.HasPrefix(line, []byte("secret-tool")) {
		line = rest
	}
	if len(line) > 200 {
		line = line[:200]
	}
	return string(bytes.TrimSpace(line))
}

func (s secretServiceStore) read(name string) ([]byte, error) {
	var out bytes.Buffer
	if err := s.run(name, nil, &out, nil, "lookup", "service", "cpb", "name", name); err != nil {
		clear(out.Bytes())
		return nil, fmt.Errorf("the Secret Service did not return cpb %s (%w)", name, err)
	}
	return out.Bytes(), nil
}

// write stores the value from secret-tool's stdin, never an argument;
// secret-tool's own output is discarded.
func (s secretServiceStore) write(name string, value []byte) error {
	err := s.run(name, bytes.NewReader(value), io.Discard, io.Discard, "store", "--label", "cpb "+name, "service", "cpb", "name", name)
	var ue *unavailableError
	if errors.As(err, &ue) {
		return err
	}
	if err != nil {
		return fmt.Errorf("the Secret Service did not take cpb %s (secret-tool exited: %v); nothing was stored", name, err)
	}
	return nil
}

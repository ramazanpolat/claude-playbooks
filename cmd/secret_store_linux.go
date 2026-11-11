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

func osDefaultSecretStore() (osSecretStore, error) {
	dir, err := secretsDir()
	if err != nil {
		return nil, err
	}
	st := linuxStore{file: fileStore{dir: dir}}
	if tool, err := lookSecretTool(); err == nil {
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
)

// keyring asks the Secret Service about name. With deep, a missing answer
// is checked with search, which tells a locked item from a missing one but
// prints the values of unlocked matches: only the helper's own process
// asks deep. Without it (target and where, which cpb's own process calls)
// only lookup runs, its output going to the null device, so no value
// passes through that process; a locked item then reads as absent.
//
// Only a definite "no Secret Service" (lookup's own error) is
// keyringUnreachable with no error, and lets the file store answer. A
// tool that did not answer in time (an unlock prompt nobody answered,
// say) is an error: the store cannot answer, and the value goes nowhere.
func (s linuxStore) keyring(name string, deep bool) (keyringState, error) {
	if s.ss == nil {
		return keyringUnreachable, nil
	}
	found, err := s.ss.lookup(name)
	var ue *unavailableError
	switch {
	case errors.As(err, &ue):
		return keyringUnreachable, err
	case err != nil:
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
func (s linuxStore) target(name string) (string, string) {
	k, _ := s.keyring(name, false)
	if k == keyringFound {
		return storeSecretService, ""
	}
	if inFile, err := s.file.exists(name); (err == nil && inFile) || k == keyringUnreachable {
		return storeFile, s.file.path(name)
	}
	return storeSecretService, ""
}

func (s linuxStore) where(name string) string {
	kind, path := s.target(name)
	switch {
	case kind == storeSecretService:
		return "your Secret Service keyring, as the item \"cpb " + name + "\" (service cpb, name " + name + ")"
	case s.ss == nil:
		return "the file " + path + ", readable by you (and root), since this machine has no keyring: secret-tool is not installed (installing it and a keyring such as GNOME Keyring or KWallet would keep it there instead)"
	case s.unreachable(name):
		return "the file " + path + ", readable by you (and root), since no keyring can be reached on this machine (a keyring such as GNOME Keyring or KWallet running in your session would keep it there instead)"
	default:
		return "the file " + path + ", readable by you (and root)"
	}
}

// unreachable reports a definite "no Secret Service" for where's wording.
func (s linuxStore) unreachable(name string) bool {
	k, err := s.keyring(name, false)
	return k == keyringUnreachable && err == nil
}

// maxValue is the Secret Service's limit, which the file store keeps too,
// so a value fits whichever store takes it.
func (s linuxStore) maxValue() int { return min(secretServiceMaxValue, fileStoreMaxValue) }

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

func (s linuxStore) read(name string) ([]byte, error) {
	k, err := s.keyring(name, false)
	if err != nil {
		return nil, err
	}
	if k == keyringFound {
		return s.ss.read(name)
	}
	return s.file.read(name)
}

// write goes to the Secret Service when it can answer, else to the file
// store; neither replaces a stored value, and a locked keyring item is
// never written over or beside.
func (s linuxStore) write(name string, value []byte) error {
	k, err := s.keyring(name, true)
	if err != nil {
		return err
	}
	switch k {
	case keyringAbsent:
		return s.ss.write(name, value)
	case keyringUnreachable:
		return s.file.write(name, value)
	}
	return fmt.Errorf("cpb:%s is in your Secret Service keyring already; nothing was stored", name)
}

func (s secretServiceStore) maxValue() int { return secretServiceMaxValue }

// run runs secret-tool with args, bounded by toolTimeout; a run that does
// not end in time is killed and reported as a store that cannot answer.
func (s secretServiceStore) run(name string, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), toolTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, s.tool, args...)
	c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
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
	found, err := s.lookup(name)
	if err != nil || found {
		return found, err
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

// lookup is exists without the locked check: found, missing (or locked),
// or an error when no Secret Service answers. lookup prints the value on
// stdout: it goes to the null device (a nil Stdout), never through this
// process. Its stderr holds only its own errors, never a value.
func (s secretServiceStore) lookup(name string) (bool, error) {
	var errb bytes.Buffer
	err := s.run(name, nil, nil, &errb, "lookup", "service", "cpb", "name", name)
	if err == nil {
		return true, nil
	}
	var ue *unavailableError
	if errors.As(err, &ue) {
		return false, err
	}
	why := toolError(errb.Bytes())
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 || why != "" {
		if why == "" {
			why = err.Error()
		}
		return false, fmt.Errorf("no Secret Service can be reached on this machine (secret-tool: %s), so cpb:%s cannot be checked or stored here; run a keyring such as GNOME Keyring or KWallet in this session, or configure a helper of your own (ALTER DEFAULTS SET secret_helper = '<command>')", why, name)
	}
	return false, nil
}

// lockedItem reports whether search lists an item for name: it lists a
// locked item's path and attributes without unlocking it, and for an
// unlocked one also its value. It is run without --unlock (which would
// prompt) and without --all. The output stays in this process (the
// helper), is scanned in place for an item line, with no copy made, and
// is cleared.
func (s secretServiceStore) lockedItem(name string) (bool, error) {
	var out bytes.Buffer
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

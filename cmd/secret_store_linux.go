//go:build linux

package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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

// keyring reports whether the Secret Service can answer about name, and
// whether it holds it. Its lookup's output goes to the null device, so no
// value passes through this process.
func (s linuxStore) keyring(name string) (reachable, found bool) {
	if s.ss == nil {
		return false, false
	}
	found, err := s.ss.exists(name)
	return err == nil, found
}

// target is where name's value is kept, or would be kept when it is not
// stored yet: the keyring when it holds it, the file when that does, else
// the keyring when it can answer, else the file store and the file's path.
func (s linuxStore) target(name string) (string, string) {
	reachable, found := s.keyring(name)
	if found {
		return storeSecretService, ""
	}
	if inFile, err := s.file.exists(name); (err == nil && inFile) || !reachable {
		return storeFile, s.file.path(name)
	}
	return storeSecretService, ""
}

func (s linuxStore) where(name string) string {
	kind, path := s.target(name)
	reachable, _ := s.keyring(name)
	switch {
	case kind == storeSecretService:
		return "your Secret Service keyring, as the item \"cpb " + name + "\" (service cpb, name " + name + ")"
	case s.ss == nil:
		return "the file " + path + ", readable by you (and root), since this machine has no keyring: secret-tool is not installed (installing it and a keyring such as GNOME Keyring or KWallet would keep it there instead)"
	case !reachable:
		return "the file " + path + ", readable by you (and root), since no keyring can be reached on this machine (a keyring such as GNOME Keyring or KWallet running in your session would keep it there instead)"
	default:
		return "the file " + path + ", readable by you (and root)"
	}
}

// exists looks in the Secret Service when it can answer, then in the file.
// A name in both is the keyring's, and one stderr line says where the file
// copy is. An error means the file store could not be trusted (its mode,
// owner or a link) while the keyring did not hold the name.
func (s linuxStore) exists(name string) (bool, error) {
	_, inKeyring := s.keyring(name)
	inFile, ferr := s.file.exists(name)
	if inKeyring {
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
	if _, found := s.keyring(name); found {
		return s.ss.read(name)
	}
	return s.file.read(name)
}

// write goes to the Secret Service when it can answer, else to the file
// store; neither replaces a stored value.
func (s linuxStore) write(name string, value []byte) error {
	if reachable, _ := s.keyring(name); reachable {
		return s.ss.write(name, value)
	}
	return s.file.write(name, value)
}

// exists asks lookup, whose exit status is 1 both when the item is not
// there and when no Secret Service can be reached (a server or a container
// with secret-tool but no keyring daemon, or no session bus). Only stderr
// tells them apart: lookup prints nothing there for a missing item, and its
// error otherwise (libsecret 0.21: "The name org.freedesktop.secrets was
// not provided by any .service files", "Cannot autolaunch D-Bus without
// X11 $DISPLAY"). A store that cannot be reached is an error (the helper
// then exits unavailableExit), never "not stored".
func (s secretServiceStore) exists(name string) (bool, error) {
	// lookup prints the value on stdout: it goes to the null device (a nil
	// Stdout), never through this process. Its stderr holds only its own
	// errors, never a value.
	var errb bytes.Buffer
	c := exec.Command(s.tool, "lookup", "service", "cpb", "name", name)
	c.Stdout, c.Stderr = nil, &errb
	err := c.Run()
	if err == nil {
		return true, nil
	}
	why := toolError(errb.Bytes())
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 && why == "" {
		return false, nil
	}
	if why == "" {
		why = err.Error()
	}
	return false, fmt.Errorf("no Secret Service can be reached on this machine (secret-tool: %s), so cpb:%s cannot be checked or stored here; run a keyring such as GNOME Keyring or KWallet in this session, or configure a helper of your own (ALTER DEFAULTS SET secret_helper = '<command>')", why, name)
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
	c := exec.Command(s.tool, "lookup", "service", "cpb", "name", name)
	c.Stdout = &out
	if err := c.Run(); err != nil {
		return nil, fmt.Errorf("the Secret Service did not return cpb %s (%v)", name, err)
	}
	return out.Bytes(), nil
}

// write stores the value from secret-tool's stdin, never an argument;
// secret-tool's own output is discarded.
func (s secretServiceStore) write(name string, value []byte) error {
	c := exec.Command(s.tool, "store", "--label", "cpb "+name, "service", "cpb", "name", name)
	c.Stdin = bytes.NewReader(value)
	c.Stdout, c.Stderr = io.Discard, io.Discard
	if err := c.Run(); err != nil {
		return fmt.Errorf("the Secret Service did not take cpb %s (secret-tool exited: %v); nothing was stored", name, err)
	}
	return nil
}

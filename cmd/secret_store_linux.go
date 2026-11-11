//go:build linux

package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// The Linux store: the Secret Service (GNOME Keyring, KWallet and others)
// through libsecret's secret-tool, attributes service=cpb and name=<name>.
// Any process of the same user can ask the Secret Service for the value
// while the collection is unlocked (SPEC.md, "Secrets").

// lookSecretTool finds secret-tool (a test replaces it with a stub).
var lookSecretTool = func() (string, error) { return exec.LookPath("secret-tool") }

// secretServiceMaxValue is the longest value kept whole: secret-tool store
// keeps the first 8192 bytes of its stdin and drops the rest, exiting 0
// (libsecret 0.21, measured), so the helper refuses longer values first.
const secretServiceMaxValue = 4096

type secretServiceStore struct{ tool string }

func osDefaultSecretStore() (osSecretStore, error) {
	tool, err := lookSecretTool()
	if err != nil {
		return nil, errors.New("cpb:<name> references live in the Secret Service through secret-tool (libsecret), and it is not on this machine; install it, or configure a helper of your own (ALTER DEFAULTS SET secret_helper = '<command>')")
	}
	return secretServiceStore{tool: tool}, nil
}

func (s secretServiceStore) where(name string) string {
	return "your Secret Service keyring, as the item \"cpb " + name + "\" (service cpb, name " + name + ")"
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
	// lookup prints the value on stdout: it goes nowhere. Its stderr holds
	// only its own errors, never a value.
	var errb bytes.Buffer
	err := s.run(name, nil, io.Discard, &errb, "lookup", "service", "cpb", "name", name)
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
	locked, err := s.lockedItem(name)
	if err != nil {
		return false, err
	}
	if locked {
		return false, fmt.Errorf("cpb:%s is in your Secret Service keyring, which is locked: unlock the keyring (log in to your desktop session, or unlock it in your keyring's tool), then run this again", name)
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

//go:build linux

package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

// The Linux store: the Secret Service (GNOME Keyring, KWallet and others)
// through libsecret's secret-tool, attributes service=cpb and name=<name>.
// Any process of the same user can ask the Secret Service for the value
// while the collection is unlocked (SPEC.md, "Secrets").

// lookSecretTool finds secret-tool (a test replaces it with a stub).
var lookSecretTool = func() (string, error) { return exec.LookPath("secret-tool") }

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

// exists asks lookup, whose exit status is 1 both when the item is not
// there and when no Secret Service can be reached (a server or a container
// with secret-tool but no keyring daemon, or no session bus). Only stderr
// tells them apart: lookup prints nothing there for a missing item, and its
// error otherwise (libsecret 0.21: "The name org.freedesktop.secrets was
// not provided by any .service files", "Cannot autolaunch D-Bus without
// X11 $DISPLAY"). A store that cannot be reached is an error (the helper
// then exits unavailableExit), never "not stored".
func (s secretServiceStore) exists(name string) (bool, error) {
	// lookup prints the value on stdout: it goes nowhere. Its stderr holds
	// only its own errors, never a value.
	var errb bytes.Buffer
	c := exec.Command(s.tool, "lookup", "service", "cpb", "name", name)
	c.Stdout, c.Stderr = io.Discard, &errb
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

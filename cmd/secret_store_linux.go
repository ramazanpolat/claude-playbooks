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

func (s secretServiceStore) exists(name string) (bool, error) {
	// lookup prints the value: it goes nowhere.
	c := exec.Command(s.tool, "lookup", "service", "cpb", "name", name)
	c.Stdout, c.Stderr = io.Discard, io.Discard
	err := c.Run()
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("the Secret Service could not be asked about cpb %s (%v)", name, err)
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

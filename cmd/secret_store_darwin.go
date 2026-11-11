//go:build darwin

package cmd

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// The macOS store: the default Keychain (normally the login one), a
// generic password with service
// "cpb" and account <name>, written and read by /usr/bin/security.
//
// The item is created by /usr/bin/security without -A or -T, so its access
// list trusts the program that created it, /usr/bin/security, and nothing
// else. The default helper reads it through that same program, so a
// launch needs no prompt; any other application that asks for the value
// gets the Keychain's own prompt. That list protects the value from other
// applications, not from other processes of the same user: any of them can
// run /usr/bin/security too (SPEC.md, "Secrets").

// securityPath is the security tool (a test replaces it with a stub).
var securityPath = "/usr/bin/security"

type keychainStore struct{}

func osDefaultSecretStore() (osSecretStore, error) {
	if _, err := os.Stat(securityPath); err != nil {
		return nil, errors.New("cpb:<name> references live in the macOS Keychain, and " + securityPath + " is not on this machine; configure a helper of your own (ALTER DEFAULTS SET secret_helper = '<command>')")
	}
	return keychainStore{}, nil
}

func (keychainStore) target(string) (string, string) { return storeKeychain, "" }

func (keychainStore) where(name string) string {
	return "your default macOS Keychain, as the item cpb/" + name + " (service cpb, account " + name + ")"
}

func (keychainStore) exists(name string) (bool, error) {
	// Without -w or -g, find-generic-password prints the item's attributes
	// and never its value.
	c := exec.Command(securityPath, "find-generic-password", "-s", "cpb", "-a", name)
	err := c.Run()
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 44 { // errSecItemNotFound
		return false, nil
	}
	return false, fmt.Errorf("the Keychain could not be asked about cpb/%s (%v)", name, err)
}

func (keychainStore) read(name string) ([]byte, error) {
	var out bytes.Buffer
	c := exec.Command(securityPath, "find-generic-password", "-s", "cpb", "-a", name, "-w")
	c.Stdout = &out
	if err := c.Run(); err != nil {
		return nil, fmt.Errorf("the Keychain did not return cpb/%s (%v)", name, err)
	}
	v := out.Bytes()
	if n := len(v); n > 0 && v[n-1] == '\n' {
		v = v[:n-1]
	}
	return v, nil
}

// write adds the item through security's interactive mode, the command on
// its stdin: the value never appears in any argument list, and as hex
// (-X) it needs no quoting. Without -U it never replaces an item.
// security's own output is discarded, since an error from it may repeat
// the command line. security -i exits 0 even when the add fails, so the
// caller reads the value back (runSecretHelper).
func (keychainStore) write(name string, value []byte) error {
	line := []byte("add-generic-password -s cpb -a " + name + " -l cpb/" + name + " -X ")
	enc := make([]byte, hex.EncodedLen(len(value)))
	hex.Encode(enc, value)
	in := append(append(line, enc...), '\n')
	defer clear(in)
	defer clear(enc)
	c := exec.Command(securityPath, "-i")
	c.Stdin = bytes.NewReader(in)
	if err := c.Run(); err != nil {
		return fmt.Errorf("the Keychain did not take cpb/%s (security exited: %v); nothing was stored", name, err)
	}
	return nil
}

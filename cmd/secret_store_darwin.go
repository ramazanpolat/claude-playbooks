//go:build darwin

package cmd

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
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

// keychainMaxValue is the longest value kept whole. security -i reads its
// commands in lines of 4096 bytes and cuts a longer one short, and the add
// command carries the value as hex (two bytes per byte) after up to 172
// bytes of its own for a 64-byte name, so about 1960 bytes fit; the cap
// leaves room under that.
const keychainMaxValue = 1536

// errSecItemNotFound, as security's exit status: the only one that means
// "not there".
const keychainNotFound = 44

// keychainInteractionNotAllowed is errSecInteractionNotAllowed, -25308, as
// security's exit status (its low byte): the Keychain is locked and cannot
// show its unlock dialog, as in an SSH session. Observed on GitHub's
// macos-15-arm64 (macOS 15.8.2) and macos-26-arm64 (macOS 26.7.2) runner
// images, over ssh localhost, for a locked keychain: show-keychain-info,
// find-generic-password -w, a one-shot add and the add through security -i
// each exit 36, while find-generic-password without -w answers as it does
// for an unlocked keychain (0, or 44). Where the dialog can be shown, each
// of the first four waits on it instead.
const keychainInteractionNotAllowed = 36

// lockedExit reports whether a security run ended with
// keychainInteractionNotAllowed.
func lockedExit(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == keychainInteractionNotAllowed
}

// keychainLocked is the answer for a Keychain locked where it cannot ask
// for its password; stored says what became of a value, if one was given.
func keychainLocked(stored string) error {
	return unavailable("the Keychain is locked and cannot ask for its password here (no window to show it in, as in an SSH session)%s. Unlock it (security unlock-keychain), then run this again", stored)
}

// keychainStore is the default Keychain; keychain names another keychain
// file instead, for a test on a throwaway one (never set in cpb itself).
type keychainStore struct{ keychain string }

func osDefaultSecretStore() (osSecretStore, error) {
	if _, err := os.Stat(securityPath); err != nil {
		return nil, errors.New("cpb:<name> references live in the macOS Keychain, and " + securityPath + " is not on this machine; configure a helper of your own (ALTER DEFAULTS SET secret_helper = '<command>')")
	}
	return keychainStore{}, nil
}

func (keychainStore) target(string) (storeTarget, error) {
	return storeTarget{kind: storeKeychain}, nil
}

// There is no file store on macOS: nothing to hand the helper.
func storeDirForHelper() string  { return "" }
func setHelperStoreDir(string)   {}
func quietFileReads() func()     { return func() {} }
func storeToolForHelper() string { return "" }
func setHelperSecretTool(string) {}

func (k keychainStore) describe(name string, _ storeTarget) string { return k.where(name) }

func (keychainStore) where(name string) string {
	return "your default macOS Keychain, as the item cpb/" + name + " (service cpb, account " + name + ")"
}

func (keychainStore) maxValue() int { return keychainMaxValue }

// args appends the keychain file, when one is named, to a command's args.
func (k keychainStore) args(a ...string) []string {
	if k.keychain != "" {
		a = append(a, k.keychain)
	}
	return a
}

// run runs security with args, bounded by toolTimeout (time enough for a
// person to answer an unlock dialog); a run that does not end in time is
// killed and reported as a store that cannot answer.
func (keychainStore) run(name string, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), toolTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, securityPath, args...)
	c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
	// The store's environment: cpb's own home in helper mode, so a launch
	// environment that sets HOME never moves the Keychain.
	c.Env = storeToolEnv()
	// A child the tool left behind may hold its output open: Run returns
	// a second after the kill all the same.
	c.WaitDelay = time.Second
	err := c.Run()
	if ctx.Err() == context.DeadlineExceeded {
		about := ""
		if name != "" {
			about = " about cpb/" + name
		}
		return unavailable("the Keychain did not answer within %s%s (an unlock dialog nobody answered?): unlock it (security unlock-keychain), then run this again", toolTimeout, about)
	}
	return err
}

func (k keychainStore) exists(name string) (bool, error) {
	// Without -w or -g, find-generic-password prints the item's attributes
	// and never its value.
	err := k.run(name, nil, nil, nil, k.args("find-generic-password", "-s", "cpb", "-a", name)...)
	if err == nil {
		return true, nil
	}
	var ue *unavailableError
	if errors.As(err, &ue) {
		return false, err
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == keychainNotFound {
		// The search answers on a locked Keychain too, and brings up no
		// dialog: "not there" says nothing about the lock, which
		// readyToStore asks about when a value is about to be typed.
		return false, nil
	}
	if lockedExit(err) {
		return false, keychainLocked("")
	}
	return false, fmt.Errorf("the Keychain could not be asked about cpb/%s (%v); unlock it (security unlock-keychain), or configure a helper of your own (ALTER DEFAULTS SET secret_helper = '<command>')", name, err)
}

// readyToStore asks the Keychain with show-keychain-info, which carries no
// value: where it is locked and cannot ask for its password, it exits 36,
// as the add would; where it can ask, it brings up the unlock dialog now
// (bounded by toolTimeout), before anything is typed.
func (k keychainStore) readyToStore(string) error {
	err := k.run("", nil, nil, nil, k.args("show-keychain-info")...)
	var ue *unavailableError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ue):
		return err
	case lockedExit(err):
		return keychainLocked("; nothing was asked for or stored")
	}
	return unavailable("the Keychain could not be asked whether it can take a value (%v); nothing was asked for or stored. Unlock it (security unlock-keychain), or configure a helper of your own (ALTER DEFAULTS SET secret_helper = '<command>')", err)
}

func (k keychainStore) read(name string) ([]byte, error) {
	var out bytes.Buffer
	if err := k.run(name, nil, &out, nil, k.args("find-generic-password", "-s", "cpb", "-a", name, "-w")...); err != nil {
		clear(out.Bytes())
		if lockedExit(err) {
			return nil, keychainLocked("")
		}
		return nil, fmt.Errorf("the Keychain did not return cpb/%s (%w)", name, err)
	}
	v := out.Bytes()
	if n := len(v); n > 0 && v[n-1] == '\n' {
		v = v[:n-1]
	}
	return v, nil
}

// keychainLockedMarkers are how security -i reports, on stderr, an add to a
// Keychain that is locked where it cannot show an unlock dialog. Observed
// (see keychainInteractionNotAllowed) as two lines:
//
//	security: SecKeychainItemCreateFromContent (<keychain>): User interaction is not allowed.
//	add-generic-password: returned -25308
//
// The number does not change with the system's language; the English text
// is a second sign. Either one, or the exit status 36, means locked.
var keychainLockedMarkers = [][]byte{[]byte("-25308"), []byte("nteraction is not allowed")}

// write adds the item through security's interactive mode, the command on
// its stdin: the value never appears in any argument list, and as hex
// (-X) it needs no quoting. Without -U it never replaces an item.
// security's output never reaches the person, since an error from it may
// repeat the command line: its stderr is kept in this process only, to
// tell a locked Keychain from other failures, and is cleared. The caller
// reads the value back whatever the exit status says (runSecretHelper).
func (k keychainStore) write(name string, value []byte, want string) error {
	if want != "" && want != storeKeychain {
		return unavailable("cpb/%s would be written to your macOS Keychain, not to %s as you were told; nothing was stored", name, storeKindWords(want))
	}
	line := []byte("add-generic-password -s cpb -a " + name + " -l cpb/" + name + " -X ")
	enc := make([]byte, hex.EncodedLen(len(value)))
	hex.Encode(enc, value)
	in := append(line, enc...)
	if k.keychain != "" {
		in = append(append(in, ' '), k.keychain...)
	}
	in = append(in, '\n')
	defer clear(in)
	defer clear(enc)
	var errb bytes.Buffer
	err := k.run(name, bytes.NewReader(in), io.Discard, &errb, "-i")
	locked := lockedExit(err)
	for _, m := range keychainLockedMarkers {
		locked = locked || bytes.Contains(errb.Bytes(), m)
	}
	clear(errb.Bytes())
	var ue *unavailableError
	switch {
	case errors.As(err, &ue):
		return err
	case locked:
		return keychainLocked("; nothing was stored")
	case err != nil:
		return fmt.Errorf("the Keychain did not take cpb/%s (security exited: %v); nothing was stored", name, err)
	}
	return nil
}

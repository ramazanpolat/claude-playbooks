package cmd

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// cpb's default secret helper (SPEC.md, "Secrets"): this binary run with a
// hidden first argument, used only when no helper is configured and every
// reference involved is cpb:<name>. It keeps values in the operating
// system's own store (secret_store_*.go) and speaks the helper interface
// every helper does:
//
//	--check KEY=cpb:<name>             presence only; never prints a value
//	--store cpb:<name>                 the value on stdin; never overwrites
//	K=cpb:<name> … -- <command> <args> resolves into that command's
//	                                    environment and execs it
//
// It runs as a process of its own: the cpb that launches never holds a
// value, as with any helper.
const secretHelperArg = "__secret-helper"

// The helper's exit statuses for --check (SPEC.md, "The helper
// interface"): notStoredExit says the reference is not stored, the one
// failure the store step acts on; unavailableExit says the store cannot
// answer (no keyring daemon, a locked or unreachable store), so a value
// could not be stored there either.
const (
	notStoredExit   = 4
	unavailableExit = 5
)

// storeCannotAnswer is the helper's answer when its store could not answer
// at all (as opposed to "not there"): exists failed, the per-name lock or a
// tool did not answer in time, or the store refused for a reason the
// person must fix first (a locked Keychain or keyring). The reason goes to
// stderr, naming the store and the fix and never a value, and the exit is
// unavailableExit, never notStoredExit.
func storeCannotAnswer(err error) error {
	fmt.Fprintln(os.Stderr, err)
	return &commandExitError{code: unavailableExit}
}

// unavailableError is a store error that storeCannotAnswer reports: the
// store could not answer, as opposed to refusing this value.
type unavailableError struct{ msg string }

func (e *unavailableError) Error() string { return e.msg }

func unavailable(format string, a ...any) error {
	return &unavailableError{fmt.Sprintf(format, a...)}
}

// The bounds on the default helper's own waits (a test shortens them):
// toolTimeout for each call to the store's tool (security, secret-tool),
// long enough for a person to answer a Keychain unlock dialog; lockTimeout
// for the per-name lock. Either ending is unavailableExit.
var (
	toolTimeout = 60 * time.Second
	lockTimeout = 30 * time.Second
)

// osSecretStore is the store the default helper keeps cpb:<name> in.
type osSecretStore interface {
	// where names the store and the item, for the store step's prompt.
	where(name string) string
	// exists reports whether the item is there; an error means the store
	// could not answer (storeCannotAnswer), never "not there".
	exists(name string) (bool, error)
	read(name string) ([]byte, error)
	// write creates the item from value; it never replaces one.
	write(name string, value []byte) error
	// maxValue is the longest value the store keeps whole: its tool cuts a
	// longer one short without saying so, so the helper refuses it first.
	maxValue() int
}

// defaultSecretStore is this machine's store, or an error naming the fix
// when it has none (secret_store_*.go; a test replaces it).
var defaultSecretStore = osDefaultSecretStore

// selfExecutable is this binary (a test replaces it).
var selfExecutable = os.Executable

// defaultHelperExe is the default helper as a helperExe, or why there is
// none on this machine.
func defaultHelperExe() (helperExe, error) {
	if _, err := defaultSecretStore(); err != nil {
		return helperExe{}, err
	}
	self, err := selfExecutable()
	if err != nil {
		return helperExe{}, fmt.Errorf("cpb's default secret helper needs cpb's own binary, and it cannot be found: %w", err)
	}
	return helperExe{path: self, pre: []string{secretHelperArg}, def: true, name: "cpb's default helper"}, nil
}

// storeWhere is where a reference's value is written, for the prompt: the
// default helper names the store and the item; another helper is named.
func storeWhere(x helperExe, ref string) string {
	if x.def {
		if st, err := defaultSecretStore(); err == nil {
			return st.where(strings.TrimPrefix(ref, manifest.CPBRefPrefix))
		}
	}
	return "the store of your secret helper " + x.name
}

// cpbRefName returns the name of a valid cpb:<name> reference.
func cpbRefName(ref string) (string, error) {
	name, ok := strings.CutPrefix(ref, manifest.CPBRefPrefix)
	if !ok {
		return "", errors.New("cpb's default secret helper resolves cpb:<name> references only; configure a helper of your own for any other (ALTER DEFAULTS SET secret_helper = '<command>')")
	}
	if err := manifest.ValidateCPBRef(ref); err != nil {
		return "", err
	}
	return name, nil
}

// maxSecretBytes bounds a value read from stdin.
const maxSecretBytes = 64 << 10

// printableASCII reports whether every byte is a printable ASCII character
// (space to '~'). The default helper keeps only such values: macOS's
// security prints any other value as hex when it is read back, so it would
// not be the value given.
func printableASCII(v []byte) bool {
	for _, c := range v {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// secretLockDir holds the default helper's per-name store locks: empty
// files, never a value (a test replaces it).
var secretLockDir = func() (string, error) {
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "cpb", "secret-locks"), nil
}

// flockName takes the per-name lock, never waiting: the loop below waits,
// bounded by lockTimeout (a test checks the flags).
var flockName = syscall.Flock

// lockSecretName holds an exclusive lock on name until the returned
// function runs, so two default helpers storing the same name never both
// find it absent; the check and the write both happen under it. It waits
// at most lockTimeout, and then says which name was locked and nothing
// else. It does not stop another program from writing the item.
func lockSecretName(name string) (func(), error) {
	dir, err := secretLockDir()
	if err == nil {
		err = os.MkdirAll(dir, 0o700)
	}
	var f *os.File
	if err == nil {
		f, err = os.OpenFile(filepath.Join(dir, name+".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	}
	if err != nil {
		return nil, unavailable("the lock for cpb:%s could not be taken (%v); nothing was stored", name, err)
	}
	deadline := time.Now().Add(lockTimeout)
	for {
		err = flockName(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, unavailable("cpb:%s is being stored by another process, and its lock was not free within %s; nothing was stored", name, lockTimeout)
			}
			return nil, unavailable("the lock for cpb:%s could not be taken (%v); nothing was stored", name, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// runSecretHelper is the default helper's process: args follow
// secretHelperArg. Messages never carry a value.
func runSecretHelper(args []string, stdin io.Reader) error {
	if len(args) == 0 {
		return errors.New("cpb's default secret helper takes --check KEY=REF, --store REF, or KEY=REF … -- <command>; a launch runs it, never a person")
	}
	st, err := defaultSecretStore()
	if err != nil {
		return err
	}
	switch args[0] {
	case "--check":
		if len(args) != 2 {
			return errors.New("--check takes one KEY=REF")
		}
		_, ref, ok := strings.Cut(args[1], "=")
		if !ok {
			return errors.New("--check takes KEY=REF")
		}
		name, err := cpbRefName(ref)
		if err != nil {
			return err
		}
		found, err := st.exists(name)
		if err != nil {
			return storeCannotAnswer(err)
		}
		if !found {
			fmt.Fprintln(os.Stderr, ref+" is not stored on this machine")
			return &commandExitError{code: notStoredExit}
		}
		return nil
	case "--store":
		if len(args) != 2 {
			return errors.New("--store takes one REF, and the value on stdin")
		}
		name, err := cpbRefName(args[1])
		if err != nil {
			return err
		}
		// The value is read before the lock is taken, so the lock is never
		// held while a writer waits on its input.
		value, err := io.ReadAll(io.LimitReader(stdin, maxSecretBytes+1))
		defer clear(value)
		if err != nil {
			return errors.New("the value could not be read from stdin; nothing was stored")
		}
		if len(value) == 0 || len(value) > maxSecretBytes {
			return fmt.Errorf("the value is empty or over %d bytes; nothing was stored", maxSecretBytes)
		}
		if !printableASCII(value) {
			return errors.New("cpb's default helper keeps printable ASCII values only (space to '~': no tab, newline or non-ASCII character); nothing was stored. Use a helper of your own for any other value")
		}
		if max := st.maxValue(); len(value) > max {
			return fmt.Errorf("the value is %d bytes, and this store keeps at most %d bytes whole; nothing was stored. Use a helper of your own for a longer value", len(value), max)
		}
		unlock, err := lockSecretName(name)
		if err != nil {
			return storeCannotAnswer(err)
		}
		defer unlock()
		found, err := st.exists(name)
		if err != nil {
			return storeCannotAnswer(err)
		}
		if found {
			// Rotation is add-new, verify, retire old: never in place.
			return fmt.Errorf("%s is stored already, and cpb never replaces a stored secret: store the new value under a new name", args[1])
		}
		if err := st.write(name, value); err != nil {
			var ue *unavailableError
			if errors.As(err, &ue) {
				return storeCannotAnswer(err)
			}
			return err
		}
		// The value is read back: another program may have written the
		// same item at the same moment, so no exit status proves the value
		// stored is the one given.
		got, err := st.read(name)
		defer clear(got)
		var ue *unavailableError
		if errors.As(err, &ue) {
			return storeCannotAnswer(fmt.Errorf("%s: the store did not answer when the value was read back (%v); it may be stored: run this again to see", args[1], err))
		}
		if err != nil || subtle.ConstantTimeCompare(got, value) != 1 {
			return fmt.Errorf("the store does not hold the value given for %s; it was not stored", args[1])
		}
		return nil
	}
	path, argv, env, err := secretHelperExec(st, args, os.Environ())
	var ue *unavailableError
	if errors.As(err, &ue) {
		return storeCannotAnswer(err)
	}
	if err != nil {
		return err
	}
	return syscall.Exec(path, argv, env)
}

// secretHelperExec is exec mode's plan: the command to exec and its
// environment with each KEY set from its reference. Errors name keys and
// references, never a value.
func secretHelperExec(st osSecretStore, args, env []string) (string, []string, []string, error) {
	sep := slices.Index(args, "--")
	if sep < 1 || sep == len(args)-1 {
		return "", nil, nil, errors.New("exec mode takes KEY=REF … -- <command> [args]")
	}
	for _, kv := range args[:sep] {
		key, ref, ok := strings.Cut(kv, "=")
		if !ok || manifest.ValidateEnvKey(key) != nil {
			return "", nil, nil, errors.New("exec mode takes KEY=REF pairs before --")
		}
		name, err := cpbRefName(ref)
		if err != nil {
			return "", nil, nil, fmt.Errorf("%s: %w", key, err)
		}
		value, err := st.read(name)
		if err != nil {
			return "", nil, nil, fmt.Errorf("%s: %s could not be read (%w)", key, ref, err)
		}
		env = append(withoutKeys(env, []string{key}), key+"="+string(value))
		clear(value)
	}
	command := args[sep+1:]
	path, err := exec.LookPath(command[0])
	if err != nil {
		return "", nil, nil, fmt.Errorf("%s: not found", command[0])
	}
	return path, command, env, nil
}

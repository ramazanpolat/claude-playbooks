package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"

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

// osSecretStore is the store the default helper keeps cpb:<name> in.
type osSecretStore interface {
	// where names the store and the item, for the store step's prompt.
	where(name string) string
	exists(name string) (bool, error)
	read(name string) ([]byte, error)
	// write creates the item from value; it never replaces one.
	write(name string, value []byte) error
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
			return err
		}
		if !found {
			fmt.Fprintln(os.Stderr, ref+" is not stored on this machine")
			return &commandExitError{code: 4}
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
		found, err := st.exists(name)
		if err != nil {
			return err
		}
		if found {
			// Rotation is add-new, verify, retire old: never in place.
			return fmt.Errorf("%s is stored already, and cpb never replaces a stored secret: store the new value under a new name", args[1])
		}
		value, err := io.ReadAll(io.LimitReader(stdin, maxSecretBytes+1))
		defer clear(value)
		if err != nil {
			return errors.New("the value could not be read from stdin; nothing was stored")
		}
		if len(value) == 0 || len(value) > maxSecretBytes {
			return fmt.Errorf("the value is empty or over %d bytes; nothing was stored", maxSecretBytes)
		}
		if err := st.write(name, value); err != nil {
			return err
		}
		// The store is asked again: macOS's security -i exits 0 even when
		// its add failed, so its exit status alone proves nothing.
		if found, err := st.exists(name); err != nil || !found {
			return fmt.Errorf("the store did not keep %s; nothing was stored", args[1])
		}
		return nil
	}
	path, argv, env, err := secretHelperExec(st, args, os.Environ())
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
			return "", nil, nil, fmt.Errorf("%s: %s could not be read (%v)", key, ref, err)
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

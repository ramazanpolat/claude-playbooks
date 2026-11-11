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

// The stores cpb's default helper keeps a value in, as `cpb play`'s JSON
// names them (secrets[].store).
const (
	storeKeychain      = "keychain"
	storeSecretService = "secret-service"
	storeFile          = "file"
)

// storeTarget is where a value is kept, or would be kept: a store kind,
// the file's path for the file store, and, for the file store, why no
// keyring keeps it ("" when the keyring answers and the file holds it).
type storeTarget struct{ kind, path, why string }

// storeKindWords names a store kind for a person.
func storeKindWords(kind string) string {
	switch kind {
	case storeKeychain:
		return "your macOS Keychain"
	case storeSecretService:
		return "your Secret Service keyring"
	case storeFile:
		return "the file store"
	}
	return kind
}

// The default helper's store is the one of the cpb that runs it: that cpb
// hands it, in these variables, the file store's directory it resolved
// and its own home and session bus, so a launch environment a playbook or
// a recipe sets (HOME, XDG_DATA_HOME, DBUS_SESSION_BUS_ADDRESS) never moves
// the store, and cpb ask, whose child keeps a narrow environment, finds the
// same one. The helper uses them for its own store calls and drops them
// before it runs a command.
const storeEnvPrefix = manifest.SecretStoreEnvPrefix

// storeEnvKeys are the variables of cpb's own environment that reach the
// store's tools: what the Keychain and the keyring are found through.
var storeEnvKeys = []string{"HOME", "DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR", "DISPLAY"}

// fileStoreOptInEnv is the one variable that sends cpb:<name> values to
// the file store on a Linux machine whose keyring is installed but fails
// (a bus cpb does not know, a keyring that cannot be started). cpb reads
// it from its own environment only and hands it to the helper: a
// playbook's environment never sets it (a reserved key), and no helper or
// launched program gets it (helperEnv, a launch's own variables).
const fileStoreOptInEnv = manifest.SecretFileStoreEnv

// helperEnv is env for running a helper (def: cpb's default one): without
// cpb's own secret-store variables (CPB_SECRET_FILE_STORE and every
// CPB_SECRET_STORE_*), whatever put them there, and, for the default
// helper, with the store cpb hands it (defaultHelperEnv). Every helper
// call goes through it: a check, --ready, --store, a launch, cpb ask.
func helperEnv(def bool, env []string) []string {
	env = withoutStoreVars(env)
	if def {
		env = defaultHelperEnv(env)
	}
	return env
}

// withoutStoreVars is env without cpb's own secret-store variables.
func withoutStoreVars(env []string) []string {
	return withoutKeys(withoutPrefix(env, storeEnvPrefix), []string{fileStoreOptInEnv})
}

// defaultHelperEnv is env for running cpb's default helper: env without any
// CPB_SECRET_STORE_* of its own, plus CPB_SECRET_STORE_SET=1, the file
// store's directory (CPB_SECRET_STORE_DIR), the per-name lock directory
// (CPB_SECRET_STORE_LOCK_DIR), the secret-tool cpb found
// (CPB_SECRET_STORE_SECRET_TOOL, absent when it found none), the file
// store opt-in (CPB_SECRET_STORE_FILE_OK=1 when cpb's own
// CPB_SECRET_FILE_STORE is 1), and cpb's own value of each storeEnvKeys
// (CPB_SECRET_STORE_<KEY>; absent when cpb's own is unset).
func defaultHelperEnv(env []string) []string {
	env = append(withoutStoreVars(env), storeEnvPrefix+"SET=1")
	if d := storeDirForHelper(); d != "" {
		env = append(env, storeEnvPrefix+"DIR="+d)
	}
	if d, err := secretLockDir(); err == nil {
		env = append(env, storeEnvPrefix+"LOCK_DIR="+d)
	}
	if t := storeToolForHelper(); t != "" {
		env = append(env, storeEnvPrefix+"SECRET_TOOL="+t)
	}
	if os.Getenv(fileStoreOptInEnv) == "1" {
		env = append(env, storeEnvPrefix+"FILE_OK=1")
	}
	for _, k := range storeEnvKeys {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, storeEnvPrefix+k+"="+v)
		}
	}
	return env
}

// helperToolEnv is the environment the store's tools run with in helper
// mode (nil: the process's own): the process's environment with
// storeEnvKeys set as the running cpb handed them, and no
// CPB_SECRET_STORE_*.
var helperToolEnv []string

// helperHanded: the cpb that runs the helper handed it the store
// (defaultHelperEnv); helperFileOK is its file store opt-in.
var helperHanded, helperFileOK bool

// inHelperProcess: this process runs as the default helper
// (runSecretHelper), whether a cpb started it or anything else did.
var inHelperProcess bool

// fileStoreOptIn reports CPB_SECRET_FILE_STORE=1. A helper takes it only
// from the cpb that runs it (handed, defaultHelperEnv), never from its own
// environment: a helper started without a hand-over (a wrapper around the
// internal verb, a launch environment that sets the variable) has no
// opt-in. cpb's own process reads its own environment.
func fileStoreOptIn() bool {
	if inHelperProcess || helperHanded {
		return helperHanded && helperFileOK
	}
	return os.Getenv(fileStoreOptInEnv) == "1"
}

// The default helper's notices, one stderr line each, never a value: a
// read the file store served while a keyring is installed, and a name the
// file keeps beside the keyring. helperNotices finds them.
const (
	readFromFileNotice = " is read from the file "
	alsoKeptNotice     = " is also kept in the file "
)

// helperNotices are the lines of a helper child's stderr that are the
// default helper's notices, for a caller that keeps the rest (cpb ask
// keeps claude's own stderr for a failure only).
func helperNotices(stderr string) string {
	var b strings.Builder
	for _, l := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(l, manifest.CPBRefPrefix) && (strings.Contains(l, readFromFileNotice) || strings.Contains(l, alsoKeptNotice)) {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

// helperLockDir is the lock directory the running cpb handed the helper.
var helperLockDir string

// applyHelperStoreEnv reads, in helper mode, what the cpb that runs the
// helper handed it (defaultHelperEnv); nothing when it handed nothing. A
// path handed that is not absolute is refused: the hand-over is broken,
// and nothing is read or stored.
func applyHelperStoreEnv() error {
	helperHanded, helperFileOK, helperToolEnv, helperLockDir = false, false, nil, ""
	setHelperStoreDir("")
	setHelperSecretTool("")
	if os.Getenv(storeEnvPrefix+"SET") != "1" {
		return nil
	}
	helperHanded = true
	for _, k := range []string{"DIR", "LOCK_DIR", "SECRET_TOOL"} {
		if v := os.Getenv(storeEnvPrefix + k); v != "" && !filepath.IsAbs(v) {
			return fmt.Errorf("the store handed to cpb's default helper is not usable (%s%s is not an absolute path); nothing was read or stored", storeEnvPrefix, k)
		}
	}
	helperFileOK = os.Getenv(storeEnvPrefix+"FILE_OK") == "1"
	helperLockDir = os.Getenv(storeEnvPrefix + "LOCK_DIR")
	setHelperStoreDir(os.Getenv(storeEnvPrefix + "DIR"))
	setHelperSecretTool(os.Getenv(storeEnvPrefix + "SECRET_TOOL"))
	env := withoutStoreVars(os.Environ())
	env = withoutKeys(env, storeEnvKeys)
	for _, k := range storeEnvKeys {
		if v, ok := os.LookupEnv(storeEnvPrefix + k); ok {
			env = append(env, k+"="+v)
		}
	}
	helperToolEnv = env
	return nil
}

// storeToolEnv is the environment for one call to a store's tool.
func storeToolEnv() []string {
	if helperToolEnv != nil {
		return append([]string{}, helperToolEnv...)
	}
	return withoutStoreVars(os.Environ())
}

// unavailableError is a store error that storeCannotAnswer reports: the
// store could not answer, as opposed to refusing this value. keyringFailed
// marks a keyring that failed outright (not a timeout, not a locked item),
// the one failure CPB_SECRET_FILE_STORE=1 turns into "use the file store".
type unavailableError struct {
	msg           string
	keyringFailed bool
}

func (e *unavailableError) Error() string { return e.msg }

func unavailable(format string, a ...any) error {
	return &unavailableError{msg: fmt.Sprintf(format, a...)}
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
	// describe is where's wording for a target already asked for, so the
	// prompt and the write the store step binds come from one answer.
	describe(name string, t storeTarget) string
	// target is where name's value is kept, or would be kept when it is not
	// stored yet. It reads no value; a store that cannot answer is an
	// error, never a guess.
	target(name string) (storeTarget, error)
	// exists reports whether the item is there; an error means the store
	// could not answer (storeCannotAnswer), never "not there".
	exists(name string) (bool, error)
	read(name string) ([]byte, error)
	// write creates the item from value; it never replaces one. want, when
	// set, is the store kind the person was told: a write that would go
	// elsewhere stores nothing and cannot answer.
	write(name string, value []byte, want string) error
	// maxValue is the longest value the store keeps whole: its tool cuts a
	// longer one short without saying so, so the helper refuses it first.
	maxValue() int
	// readyToStore asks, with nothing that carries a value, whether a value
	// of name given now could be stored; an error says why not (a Keychain
	// locked where it cannot ask for its password, a keyring that cannot
	// answer, a file store that cannot be made). It may bring up the
	// store's own unlock dialog, so only the store step calls it (--ready),
	// right before it asks a person for a value: never a presence check.
	readyToStore(name string) error
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
// files, never a value (a test replaces it). In helper mode it is the one
// the running cpb handed over, so a launch whose playbook sets HOME or
// XDG_CACHE_HOME locks where --store does; otherwise cpb's own.
var secretLockDir = func() (string, error) {
	if helperHanded {
		if helperLockDir == "" {
			return "", errors.New("the cpb that runs the helper handed no lock directory")
		}
		return helperLockDir, nil
	}
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
	return lockSecretNameWithin(name, lockTimeout)
}

// tempSweeper is a store that keeps temporary files a killed write can
// leave (the file store): sweepTemps removes those of name. Called only
// under name's lock.
type tempSweeper interface{ sweepTemps(name string) }

// sweepUnderLock sweeps name's temporary files when the store keeps any
// and name's lock is free at once; a lock someone holds is a write in
// progress, whose temporary file is not stale.
func sweepUnderLock(st osSecretStore, name string) {
	sw, ok := st.(tempSweeper)
	if !ok {
		return
	}
	if unlock, err := lockSecretNameWithin(name, 0); err == nil {
		sw.sweepTemps(name)
		unlock()
	}
}

func lockSecretNameWithin(name string, wait time.Duration) (func(), error) {
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
	deadline := time.Now().Add(wait)
	for {
		err = flockName(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, unavailable("cpb:%s is being stored by another process, and its lock was not free within %s; nothing was stored", name, wait)
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
		return errors.New("cpb's default secret helper takes --check KEY=REF, --ready REF, --store REF [--into STORE], or KEY=REF … -- <command>; a launch runs it, never a person")
	}
	inHelperProcess = true
	defer func() { inHelperProcess = false }()
	if err := applyHelperStoreEnv(); err != nil {
		return err
	}
	st, err := defaultSecretStore()
	if err != nil {
		return err
	}
	switch args[0] {
	case "--ready":
		// The default helper's own verb, not the helper interface's: the
		// store step runs it just before it asks a person for a value.
		if len(args) != 2 {
			return errors.New("--ready takes one REF")
		}
		name, err := cpbRefName(args[1])
		if err != nil {
			return err
		}
		if err := st.readyToStore(name); err != nil {
			return storeCannotAnswer(err)
		}
		return nil
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
		// --into STORE is the default helper's own: the store kind the
		// store step told the person, which the write must still go to.
		want := ""
		if len(args) == 4 && args[2] == "--into" {
			want, args = args[3], args[:2]
		}
		if len(args) != 2 {
			return errors.New("--store takes one REF [--into STORE], and the value on stdin")
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
		// Every store step sweeps what a killed write of name left, the
		// name stored or not: a temporary file linked before the kill is a
		// second name for the value.
		if sw, ok := st.(tempSweeper); ok {
			sw.sweepTemps(name)
		}
		found, err := st.exists(name)
		if err != nil {
			return storeCannotAnswer(err)
		}
		if found {
			// Rotation is add-new, verify, retire old: never in place.
			return fmt.Errorf("%s is stored already, and cpb never replaces a stored secret: store the new value under a new name", args[1])
		}
		if err := st.write(name, value, want); err != nil {
			var ue *unavailableError
			if errors.As(err, &ue) {
				return storeCannotAnswer(err)
			}
			return err
		}
		// The value is read back: another program may have written the
		// same item at the same moment, so no exit status proves the value
		// stored is the one given.
		defer quietFileReads()()
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
	path, argv, env, err := secretHelperExec(st, args, withoutStoreVars(os.Environ()))
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
		sweepUnderLock(st, name)
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

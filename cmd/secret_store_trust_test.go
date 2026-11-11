//go:build linux

package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// helperStoreReset undoes what helper mode reads from its environment.
func helperStoreReset(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { helperStoreDir, helperToolEnv = "", nil })
}

// storeExit runs the helper's --store and returns its exit status (0 for
// none) and stderr.
func storeExit(t *testing.T, args []string, value string) (int, string) {
	t.Helper()
	var err error
	out := captureStderr(t, func() { err = runSecretHelper(args, strings.NewReader(value)) })
	if err == nil {
		return 0, out
	}
	if code, ok := exitCode(err); ok {
		return code, out
	}
	return 1, out + err.Error()
}

// Only secret-tool's two messages for "no Secret Service" (exit 1) let the
// file store answer. Any other failure of lookup (another exit status, any
// other message, a crash) is a store that cannot answer: --store exits 5
// and writes no file, and a read (a launch) never falls back to the file.
func TestSecretToolFailureNeverFallsBackToTheFile(t *testing.T) {
	items, _ := stubSecretTool(t)
	dir := fileStoreHome(t)
	locks := t.TempDir()
	savedLocks := secretLockDir
	secretLockDir = func() (string, error) { return locks, nil }
	t.Cleanup(func() { secretLockDir = savedLocks })
	for i, msg := range noSecretServiceMessages {
		t.Setenv("STUB_NO_SERVICE", msg)
		name := "ok" + string(rune('a'+i))
		if code, out := storeExit(t, []string{"--store", "cpb:" + name}, "v-1"); code != 0 {
			t.Fatalf("%q: --store exit %d: %s", msg, code, out)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%q: not in the file store", msg)
		}
	}
	t.Setenv("STUB_NO_SERVICE", "")
	st, _ := defaultSecretStore()
	if err := (fileStore{dir: dir}).write("both", []byte("file-copy")); err != nil {
		t.Fatal(err)
	}
	for name, env := range map[string][2]string{
		"another message, exit 1": {"STUB_NO_SERVICE", "Could not connect: No such file or directory"},
		"exit 2":                  {"STUB_LOOKUP_EXIT", "2"},
		"a crash":                 {"STUB_LOOKUP_CRASH", "1"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(env[0], env[1])
			code, out := storeExit(t, []string{"--store", "cpb:new"}, "Value-77")
			if code != unavailableExit || strings.Contains(out, "Value-77") {
				t.Fatalf("--store: exit %d: %s", code, out)
			}
			if _, err := os.Stat(filepath.Join(dir, "new")); err == nil {
				t.Fatal("the value went to the file store")
			}
			if _, err := os.Stat(filepath.Join(items, "new")); err == nil {
				t.Fatal("the value went to the keyring")
			}
			var ue *unavailableError
			if v, err := st.read("both"); v != nil || !errors.As(err, &ue) {
				t.Fatalf("read fell back to the file: %q %v", v, err)
			}
			if _, _, env, err := secretHelperExec(st, []string{"K=cpb:both", "--", "true"}, nil); !errors.As(err, &ue) || strings.Contains(strings.Join(env, " "), "file-copy") {
				t.Fatalf("a launch: %v", err)
			}
			if code, _ := storeExit(t, []string{"--check", "K=cpb:new"}, ""); code != unavailableExit {
				t.Fatalf("--check: exit %d", code)
			}
			if tg, err := st.target("new"); err == nil {
				t.Fatalf("target answered %+v", tg)
			}
		})
	}
}

// The store step's prompt names a store (--into), and the write must still
// go there: a keyring that went away, or came back, stores nothing.
func TestStoreGoesOnlyWhereThePersonWasTold(t *testing.T) {
	items, _ := stubSecretTool(t)
	dir := fileStoreHome(t)
	locks := t.TempDir()
	savedLocks := secretLockDir
	secretLockDir = func() (string, error) { return locks, nil }
	t.Cleanup(func() { secretLockDir = savedLocks })
	// Told the keyring; no keyring now.
	t.Setenv("STUB_NO_SERVICE", noSecretServiceMessages[0])
	if code, out := storeExit(t, []string{"--store", "cpb:a", "--into", storeSecretService}, "v-1"); code != unavailableExit || !strings.Contains(out, "as you were told") {
		t.Fatalf("keyring gone: exit %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "a")); err == nil {
		t.Fatal("written to the file")
	}
	// Told the file; a keyring now.
	t.Setenv("STUB_NO_SERVICE", "")
	if code, out := storeExit(t, []string{"--store", "cpb:b", "--into", storeFile}, "v-1"); code != unavailableExit || !strings.Contains(out, "as you were told") {
		t.Fatalf("keyring back: exit %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(items, "b")); err == nil {
		t.Fatal("written to the keyring")
	}
	// Told the keyring, and it answers.
	if code, out := storeExit(t, []string{"--store", "cpb:c", "--into", storeSecretService}, "v-1"); code != 0 {
		t.Fatalf("as told: exit %d: %s", code, out)
	}
}

// The default helper's store is the one of the cpb that runs it: a launch
// environment that sets XDG_DATA_HOME, HOME or the bus, or a planted
// CPB_SECRET_STORE_*, never moves it; secret-tool runs with cpb's own
// home and bus, and in English.
func TestDefaultHelperUsesCPBsOwnStore(t *testing.T) {
	_, log := stubSecretTool(t)
	helperStoreReset(t)
	mine := fileStoreHome(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/cpb/own/bus")
	launch := []string{"XDG_DATA_HOME=/elsewhere", "HOME=/elsewhere", "DBUS_SESSION_BUS_ADDRESS=unix:path=/recipe/bus", storeEnvPrefix + "DIR=/planted"}
	env := defaultHelperEnv(launch)
	has := func(kv string) bool {
		for _, e := range env {
			if e == kv {
				return true
			}
		}
		return false
	}
	if !has(storeEnvPrefix+"DIR="+mine) || has(storeEnvPrefix+"DIR=/planted") || !has(storeEnvPrefix+"DBUS_SESSION_BUS_ADDRESS=unix:path=/cpb/own/bus") || !has("XDG_DATA_HOME=/elsewhere") {
		t.Fatalf("the helper's environment: %q", env)
	}
	lp := &launchPlan{helper: "/bin/true", def: true, refs: map[string]string{"K": "cpb:x"}}
	c := lp.command("/bin/claude", nil, launch)
	if !strings.Contains(strings.Join(c.Env, "\n"), storeEnvPrefix+"DIR="+mine) {
		t.Fatalf("a launch through the default helper: %q", c.Env)
	}
	if c := (&launchPlan{helper: "/bin/true", refs: map[string]string{"K": "x:y"}}).command("/bin/claude", nil, launch); strings.Contains(strings.Join(c.Env, "\n"), storeEnvPrefix+"DIR="+mine) || strings.Contains(strings.Join(c.Env, "\n"), storeEnvPrefix+"SET=1") {
		t.Fatalf("a configured helper got cpb's store: %q", c.Env)
	}
	// In helper mode: the directory and the bus cpb handed over.
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	locks := t.TempDir()
	savedLocks := secretLockDir
	secretLockDir = func() (string, error) { return locks, nil }
	t.Cleanup(func() { secretLockDir = savedLocks })
	t.Setenv("STUB_NO_SERVICE", noSecretServiceMessages[0])
	if code, out := storeExit(t, []string{"--store", "cpb:tok"}, "v-1"); code != 0 {
		t.Fatalf("--store: exit %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(mine, "tok")); err != nil {
		t.Fatal("not in cpb's own file store")
	}
	b, _ := os.ReadFile(log + ".env")
	if !strings.Contains(string(b), "LC_ALL=C ") || !strings.Contains(string(b), "BUS=unix:path=/cpb/own/bus") || strings.Contains(string(b), "/recipe/bus") {
		t.Fatalf("secret-tool's environment: %s", b)
	}
}

// A read served by the file while secret-tool is installed says so, once,
// on stderr.
func TestReadFromTheFileSaysSo(t *testing.T) {
	stubSecretTool(t)
	dir := fileStoreHome(t)
	if err := (fileStore{dir: dir}).write("tok", []byte("v-1")); err != nil {
		t.Fatal(err)
	}
	st, _ := defaultSecretStore()
	var v []byte
	var err error
	out := captureStderr(t, func() { v, err = st.read("tok") })
	if err != nil || string(v) != "v-1" || strings.Count(out, "is read from the file "+filepath.Join(dir, "tok")+": the keyring does not hold it") != 1 {
		t.Fatalf("read %q %v; stderr %q", v, err, out)
	}
}

// A directory above the store that another user could change is refused:
// writable by group or others without the sticky bit, or owned by another
// user. A sticky one, as /tmp is, is not.
func TestFileStoreAncestors(t *testing.T) {
	open := t.TempDir()
	os.Chmod(open, 0o777)
	f := fileStore{dir: filepath.Join(open, "data", "cpb", "secrets")}
	if err := f.write("tok", []byte("v")); err == nil || !strings.Contains(err.Error(), "writable by others or by a shared group") {
		t.Fatalf("an other-writable ancestor: %v", err)
	}
	os.Chmod(open, 0o777|os.ModeSticky)
	if err := f.write("tok", []byte("v")); err != nil {
		t.Fatalf("a sticky ancestor: %v", err)
	}
	var st unix.Stat_t
	unix.Stat(filepath.Join(open, "data"), &st)
	ino := st.Ino
	saved := fileOwner
	fileOwner = func(s *unix.Stat_t) int {
		if s.Ino == ino {
			return os.Getuid() + 4242
		}
		return int(s.Uid)
	}
	t.Cleanup(func() { fileOwner = saved })
	if _, err := f.read("tok"); err == nil || !strings.Contains(err.Error(), "owned by another user") {
		t.Fatalf("an ancestor owned by another user: %v", err)
	}
}

// Group write on an ancestor is someone else's only when the group is not
// the user's own private group.
func TestAncestorGroupWrite(t *testing.T) {
	saved := privateGroup
	privateGroup = func() int { return 500 }
	t.Cleanup(func() { privateGroup = saved })
	for _, c := range []struct {
		mode uint32
		gid  uint32
		bad  bool
	}{
		{0o755, 500, false},
		{0o775, 500, false}, // the user's private group
		{0o775, 600, true},  // a shared group
		{0o757, 500, true},  // others
		{0o1777, 0, false},  // sticky, as /tmp
		{0o2775, 600, true}, // setgid is not sticky
		{unix.S_ISVTX | 0o775, 600, false},
	} {
		st := unix.Stat_t{Mode: unix.S_IFDIR | c.mode, Gid: c.gid}
		if got := ancestorWritableByOthers(&st); got != c.bad {
			t.Errorf("mode %04o gid %d: %v, want %v", c.mode, c.gid, got, c.bad)
		}
	}
}

// The owner check: a file or a directory owned by another user is refused.
func TestFileStoreOwner(t *testing.T) {
	dir := fileStoreHome(t)
	f := fileStore{dir: dir}
	if err := f.write("tok", []byte("v")); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{filepath.Join(dir, "tok"), dir} {
		var st unix.Stat_t
		unix.Stat(target, &st)
		ino := st.Ino
		saved := fileOwner
		fileOwner = func(s *unix.Stat_t) int {
			if s.Ino == ino {
				return os.Getuid() + 4242
			}
			return int(s.Uid)
		}
		if _, err := f.read("tok"); err == nil || !strings.Contains(err.Error(), "owned by another user") {
			t.Errorf("%s owned by another: read %v", target, err)
		}
		if _, err := f.exists("tok"); err == nil {
			t.Errorf("%s owned by another: exists answered", target)
		}
		fileOwner = saved
	}
}

// Links: a name that is a link is never followed, to read or to write over
// (a dangling one included: nothing is created where it points).
func TestFileStoreLinks(t *testing.T) {
	dir := fileStoreHome(t)
	f := fileStore{dir: dir}
	if err := f.write("real", []byte("v-real")); err != nil {
		t.Fatal(err)
	}
	os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "tok"))
	if v, err := f.read("tok"); v != nil || err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("read through a link: %q %v", v, err)
	}
	if _, err := f.exists("tok"); err == nil {
		t.Fatal("exists followed a link")
	}
	nowhere := filepath.Join(t.TempDir(), "nowhere")
	os.Symlink(nowhere, filepath.Join(dir, "dangling"))
	if err := f.write("dangling", []byte("v")); err == nil {
		t.Fatal("wrote over a dangling link")
	}
	if _, err := os.Lstat(nowhere); err == nil {
		t.Fatal("created where the dangling link points")
	}
}

// A read refuses what --store would never have written, naming the file
// and never the value: an empty file, a line end at the end, a byte outside
// printable ASCII, more than 4096 bytes.
func TestFileStoreReadRefusesWhatStoreWouldNotWrite(t *testing.T) {
	dir := fileStoreHome(t)
	f := fileStore{dir: dir}
	if err := f.write("seed", []byte("v")); err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]string{
		"empty":   "",
		"newline": "Value-77\n",
		"crlf":    "Value-77\r\n",
		"tab":     "Value\t77",
		"utf8":    "Värde-77",
		"long":    strings.Repeat("v", fileStoreMaxValue+1),
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := f.read(name)
		if got != nil || err == nil || !strings.Contains(err.Error(), p) || (len(v) > 3 && strings.Contains(err.Error(), strings.TrimSpace(v))) {
			t.Errorf("%s: read %q %v", name, got, err)
		}
	}
	os.WriteFile(filepath.Join(dir, "fits"), []byte(strings.Repeat("v", fileStoreMaxValue)), 0o600)
	if v, err := f.read("fits"); err != nil || len(v) != fileStoreMaxValue {
		t.Fatalf("4096 bytes: %d %v", len(v), err)
	}
}

// A write sweeps the temporary files a killed write of the same name left
// (each may hold a value), and only those.
func TestFileStoreSweepsStaleTemps(t *testing.T) {
	dir := fileStoreHome(t)
	f := fileStore{dir: dir}
	if err := f.write("seed", []byte("v")); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, tempPrefix("tok")+"deadbeef")
	other := filepath.Join(dir, tempPrefix("tok-x")+"deadbeef")
	os.WriteFile(stale, []byte("old-value"), 0o600)
	os.WriteFile(other, []byte("other-value"), 0o600)
	if err := f.write("tok", []byte("v-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(stale); err == nil {
		t.Fatal("the stale temporary file of tok is still there")
	}
	if _, err := os.Lstat(other); err != nil {
		t.Fatal("another name's temporary file was removed")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempPrefix("tok")) {
			t.Fatalf("left: %s", e.Name())
		}
	}
}

// readyToStore on Linux asks what a write would meet: a keyring that
// cannot answer or holds the name locked, or a file store that cannot be
// made; a keyring that answers, or a writable store, is ready.
func TestLinuxReadyToStore(t *testing.T) {
	stubSecretTool(t)
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	st, _ := defaultSecretStore()
	if err := st.readyToStore("tok"); err != nil {
		t.Fatalf("a keyring that answers: %v", err)
	}
	t.Setenv("STUB_LOCKED", "1")
	if err := st.readyToStore("tok"); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("locked: %v", err)
	}
	t.Setenv("STUB_LOCKED", "")
	t.Setenv("STUB_LOOKUP_EXIT", "2")
	if err := st.readyToStore("tok"); err == nil {
		t.Fatal("a keyring that failed was ready")
	}
	t.Setenv("STUB_LOOKUP_EXIT", "")
	t.Setenv("STUB_NO_SERVICE", noSecretServiceMessages[1])
	if err := st.readyToStore("tok"); err != nil {
		t.Fatalf("no keyring, a writable home: %v", err)
	}
	if os.Getuid() != 0 {
		os.Chmod(data, 0o500)
		t.Cleanup(func() { os.Chmod(data, 0o700) })
		if err := st.readyToStore("tok"); err == nil || !strings.Contains(err.Error(), "not writable") {
			t.Fatalf("no keyring, a read-only home: %v", err)
		}
	}
}

// --store's own read-back, from the file it just wrote, says nothing about
// reading from the file: its prompt named the file already.
func TestReadBackStaysQuiet(t *testing.T) {
	stubSecretTool(t)
	dir := fileStoreHome(t)
	locks := t.TempDir()
	savedLocks := secretLockDir
	secretLockDir = func() (string, error) { return locks, nil }
	t.Cleanup(func() { secretLockDir = savedLocks })
	t.Setenv("STUB_NO_SERVICE", noSecretServiceMessages[0])
	code, out := storeExit(t, []string{"--store", "cpb:tok"}, "v-1")
	if code != 0 || strings.Contains(out, "is read from the file") || !announceFileReads {
		t.Fatalf("--store: exit %d: %q (announce after: %v)", code, out, announceFileReads)
	}
	if _, err := os.Stat(filepath.Join(dir, "tok")); err != nil {
		t.Fatal("not stored")
	}
}

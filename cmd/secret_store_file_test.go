//go:build linux

package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/envset"
)

// fileStoreHome points the file store at a temp data home and returns its
// directory (which does not exist yet).
func fileStoreHome(t *testing.T) string {
	t.Helper()
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	return filepath.Join(data, "cpb", "secrets")
}

// noSecretTool makes the machine one without secret-tool.
func noSecretTool(t *testing.T) {
	t.Helper()
	saved := lookSecretTool
	lookSecretTool = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { lookSecretTool = saved })
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// A value goes into a 0600 file under a 0700 directory, exactly as given,
// and is never replaced; no temporary file is left.
func TestFileStoreWritesPrivatelyAndNeverReplaces(t *testing.T) {
	dir := fileStoreHome(t)
	f := fileStore{dir: dir}
	if ok, err := f.exists("tok"); ok || err != nil {
		t.Fatalf("before: %v %v", ok, err)
	}
	if err := f.write("tok", []byte("v 1 ~!")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if m := mode(t, dir); m != 0o700 {
		t.Fatalf("directory mode %04o", m)
	}
	if m := mode(t, filepath.Join(dir, "tok")); m != 0o600 {
		t.Fatalf("file mode %04o", m)
	}
	if v, err := f.read("tok"); err != nil || string(v) != "v 1 ~!" {
		t.Fatalf("read %q %v", v, err)
	}
	err := f.write("tok", []byte("v-2"))
	if err == nil || !strings.Contains(err.Error(), "never replaces") {
		t.Fatalf("an overwrite: %v", err)
	}
	if v, _ := f.read("tok"); string(v) != "v 1 ~!" {
		t.Fatalf("the value was replaced: %q", v)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("left in the directory: %v", entries)
	}
}

// A link at the directory or the file is never followed, and a directory
// or file open to group or others is refused, naming the fix.
func TestFileStoreRefusesLinksAndOpenModes(t *testing.T) {
	t.Run("directory link", func(t *testing.T) {
		dir := fileStoreHome(t)
		real := t.TempDir()
		os.Chmod(real, 0o700)
		os.WriteFile(filepath.Join(real, "tok"), []byte("v"), 0o600)
		os.MkdirAll(filepath.Dir(dir), 0o700)
		if err := os.Symlink(real, dir); err != nil {
			t.Fatal(err)
		}
		f := fileStore{dir: dir}
		if _, err := f.exists("tok"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("exists: %v", err)
		}
		if _, err := f.read("tok"); err == nil {
			t.Fatal("read through a linked directory")
		}
		if err := f.write("new", []byte("v")); err == nil {
			t.Fatal("wrote through a linked directory")
		}
		if _, err := os.Stat(filepath.Join(real, "new")); err == nil {
			t.Fatal("a value landed in the link's target")
		}
	})
	t.Run("file link", func(t *testing.T) {
		dir := fileStoreHome(t)
		f := fileStore{dir: dir}
		if err := f.write("real", []byte("v")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "tok")); err != nil {
			t.Fatal(err)
		}
		if _, err := f.exists("tok"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("exists: %v", err)
		}
		if v, err := f.read("tok"); err == nil {
			t.Fatalf("read through a link: %q", v)
		}
	})
	t.Run("open modes", func(t *testing.T) {
		dir := fileStoreHome(t)
		f := fileStore{dir: dir}
		if err := f.write("tok", []byte("v")); err != nil {
			t.Fatal(err)
		}
		os.Chmod(filepath.Join(dir, "tok"), 0o644)
		if _, err := f.read("tok"); err == nil || !strings.Contains(err.Error(), "chmod 0600") {
			t.Fatalf("an open file: %v", err)
		}
		os.Chmod(filepath.Join(dir, "tok"), 0o600)
		os.Chmod(dir, 0o755)
		if _, err := f.exists("tok"); err == nil || !strings.Contains(err.Error(), "chmod 0700") {
			t.Fatalf("an open directory: %v", err)
		}
		if err := f.write("other", []byte("v")); err == nil {
			t.Fatal("wrote into an open directory")
		}
	})
}

// A name that is not a plain file name is never used: each one below has a
// real file reachable through it (planted), so without the name check
// write, exists and read would reach it; with it, all three refuse, and
// nothing in the tree changes.
func TestFileStoreNameSafety(t *testing.T) {
	dir := fileStoreHome(t)
	f := fileStore{dir: dir}
	if err := f.write("seed", []byte("v")); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a", 65)
	plant := map[string]string{
		".hidden": filepath.Join(dir, ".hidden"),
		"a/b":     filepath.Join(dir, "a", "b"),
		"../x":    filepath.Join(filepath.Dir(dir), "x"),
		"A":       filepath.Join(dir, "A"),
		"x\n":     filepath.Join(dir, "x\n"),
		"x y":     filepath.Join(dir, "x y"),
		"x~y":     filepath.Join(dir, "x~y"),
		long:      filepath.Join(dir, long),
	}
	os.Mkdir(filepath.Join(dir, "a"), 0o700)
	for _, p := range plant {
		if err := os.WriteFile(p, []byte("planted"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tree := func() string {
		var b strings.Builder
		filepath.Walk(filepath.Dir(filepath.Dir(dir)), func(p string, info os.FileInfo, err error) error {
			if err == nil {
				b.WriteString(p + "\n")
			}
			return nil
		})
		return b.String()
	}
	before := tree()
	for name := range plant {
		if err := f.write(name, []byte("v")); err == nil {
			t.Errorf("%q was written", name)
		}
		if _, err := f.exists(name); err == nil {
			t.Errorf("%q was looked up", name)
		}
		if v, err := f.read(name); err == nil || v != nil {
			t.Errorf("%q was read: %q", name, v)
		}
	}
	if _, err := f.exists(""); err == nil {
		t.Error("the empty name was looked up")
	}
	if after := tree(); after != before {
		t.Fatalf("the tree changed:\n%s\nwas\n%s", after, before)
	}
}

// A relative XDG_DATA_HOME is ignored, as the XDG rules say.
func TestSecretsDirFollowsXDG(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "relative/data")
	if d, err := secretsDir(); err != nil || d != filepath.Join(home, ".local", "share", "cpb", "secrets") {
		t.Fatalf("relative: %q %v", d, err)
	}
	t.Setenv("XDG_DATA_HOME", "")
	if d, _ := secretsDir(); d != filepath.Join(home, ".local", "share", "cpb", "secrets") {
		t.Fatalf("unset: %q", d)
	}
	abs := t.TempDir()
	t.Setenv("XDG_DATA_HOME", abs)
	if d, _ := secretsDir(); d != filepath.Join(abs, "cpb", "secrets") {
		t.Fatalf("absolute: %q", d)
	}
}

// With no secret-tool, the default helper keeps values in the file store:
// not stored (4), stored, found (0), never replaced, and resolved in exec
// mode; the store step's line names the file and how to get a keyring.
func TestDefaultHelperFileStoreWithoutSecretTool(t *testing.T) {
	dir := fileStoreHome(t)
	noSecretTool(t)
	locks := t.TempDir()
	savedLocks := secretLockDir
	secretLockDir = func() (string, error) { return locks, nil }
	t.Cleanup(func() { secretLockDir = savedLocks })

	if code, ok := exitCode(runSecretHelper([]string{"--check", "K=cpb:tok"}, strings.NewReader(""))); !ok || code != notStoredExit {
		t.Fatalf("--check before: %v %v", code, ok)
	}
	if err := runSecretHelper([]string{"--store", "cpb:tok"}, strings.NewReader("file-Value 9")); err != nil {
		t.Fatalf("--store: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "tok")); err != nil || string(b) != "file-Value 9" {
		t.Fatalf("the file holds %q (%v)", b, err)
	}
	if err := runSecretHelper([]string{"--check", "K=cpb:tok"}, strings.NewReader("")); err != nil {
		t.Fatalf("--check after: %v", err)
	}
	if err := runSecretHelper([]string{"--store", "cpb:tok"}, strings.NewReader("other")); err == nil {
		t.Fatal("a second --store replaced the value")
	}
	st, _ := defaultSecretStore()
	_, _, env, err := secretHelperExec(st, []string{"K=cpb:tok", "--", "sh", "-c", "true"}, []string{"PATH=/usr/bin:/bin"})
	if err != nil || !containsLine(env, "K=file-Value 9") {
		t.Fatalf("exec mode: %v", err)
	}
	tg, terr := st.target("new")
	kind, path := tg.kind, tg.path
	if terr != nil || kind != storeFile || path != filepath.Join(dir, "new") {
		t.Fatalf("target: %s %s %v", kind, path, terr)
	}
	if w := st.where("new"); !strings.Contains(w, path) || !strings.Contains(w, "root") || !strings.Contains(w, "secret-tool is not installed") {
		t.Fatalf("where: %s", w)
	}
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

// Two stores on one machine. A value kept in the file while no keyring ran
// is still found once a keyring answers, read from the file, and refuses a
// second --store; a value in both is the keyring's, and one stderr line
// names the file copy. New values go to the keyring when it answers, to
// the file when it does not.
func TestLinuxStoreTwoStores(t *testing.T) {
	items, _ := stubSecretTool(t) // sets a data home of its own: replaced below
	dir := fileStoreHome(t)
	locks := t.TempDir()
	savedLocks := secretLockDir
	secretLockDir = func() (string, error) { return locks, nil }
	t.Cleanup(func() { secretLockDir = savedLocks })

	// No keyring: the value goes to the file.
	t.Setenv("STUB_NO_SERVICE", "The name org.freedesktop.secrets was not provided by any .service files")
	if err := runSecretHelper([]string{"--store", "cpb:early"}, strings.NewReader("from-the-file")); err != nil {
		t.Fatalf("--store with no keyring: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "early")); err != nil {
		t.Fatal("not in the file store")
	}
	// The keyring starts: still found, read from the file, never replaced.
	t.Setenv("STUB_NO_SERVICE", "")
	if err := runSecretHelper([]string{"--check", "K=cpb:early"}, strings.NewReader("")); err != nil {
		t.Fatalf("--check once the keyring runs: %v", err)
	}
	st, _ := defaultSecretStore()
	if v, err := st.read("early"); err != nil || string(v) != "from-the-file" {
		t.Fatalf("read %q %v", v, err)
	}
	if tg, _ := st.target("early"); tg.kind != storeFile || tg.path != filepath.Join(dir, "early") {
		t.Fatalf("target of a filed value: %+v", tg)
	}
	if err := runSecretHelper([]string{"--store", "cpb:early"}, strings.NewReader("into-the-keyring")); err == nil {
		t.Fatal("--store replaced a value kept in the file")
	}
	if _, err := os.Stat(filepath.Join(items, "early")); err == nil {
		t.Fatal("the refused value reached the keyring")
	}
	// A new value goes to the keyring.
	if err := runSecretHelper([]string{"--store", "cpb:late"}, strings.NewReader("in-the-keyring")); err != nil {
		t.Fatalf("--store with a keyring: %v", err)
	}
	if _, err := os.Stat(filepath.Join(items, "late")); err != nil {
		t.Fatal("not in the keyring")
	}
	if _, err := os.Stat(filepath.Join(dir, "late")); err == nil {
		t.Fatal("also in the file store")
	}
	// In both: the keyring wins, and one line names the file copy.
	if err := (fileStore{dir: dir}).write("late", []byte("stale-copy")); err != nil {
		t.Fatal(err)
	}
	var cerr error
	out := captureStderr(t, func() { cerr = runSecretHelper([]string{"--check", "K=cpb:late"}, strings.NewReader("")) })
	if cerr != nil || !strings.Contains(out, filepath.Join(dir, "late")) || strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Fatalf("--check of a name in both: %v %q", cerr, out)
	}
	if v, _ := st.read("late"); string(v) != "in-the-keyring" {
		t.Fatalf("read %q, want the keyring's", v)
	}
	for _, v := range []string{"from-the-file", "in-the-keyring", "stale-copy"} {
		if strings.Contains(out, v) {
			t.Fatalf("a value was printed: %q", out)
		}
	}
	// In both, with the keyring's item locked: a read stops, as a store
	// that cannot answer, and a launch gets nothing; neither falls back to
	// the file's copy.
	t.Setenv("STUB_LOCKED", "1")
	var ue *unavailableError
	if v, err := st.read("late"); v != nil || !errors.As(err, &ue) || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("a locked keyring item read as %q %v", v, err)
	}
	_, _, env, err := secretHelperExec(st, []string{"K=cpb:late", "--", "true"}, nil)
	if !errors.As(err, &ue) || slices.Contains(env, "K=stale-copy") {
		t.Fatalf("a launch over a locked keyring item: %v %q", err, env)
	}
}

// A file store that cannot be trusted, with no keyring holding the name,
// is a store that cannot answer (exit 5), never "not stored".
func TestUntrustedFileStoreCannotAnswer(t *testing.T) {
	dir := fileStoreHome(t)
	noSecretTool(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.Chmod(dir, 0o755)
	var err error
	out := captureStderr(t, func() { err = runSecretHelper([]string{"--check", "K=cpb:tok"}, strings.NewReader("")) })
	if code, ok := exitCode(err); !ok || code != unavailableExit || !strings.Contains(out, "chmod 0700") {
		t.Fatalf("--check: %v %q", err, out)
	}
}

// fakeSelf stands in for cpb's own binary as the default helper: --check
// exits FAKE_CHECK_EXIT (4 by default). It handles nothing else.
func fakeSelf(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cpb")
	script := "#!/bin/sh\n[ \"$1\" = \"" + secretHelperArg + "\" ] && [ \"$2\" = --check ] || exit 2\nexit \"${FAKE_CHECK_EXIT:-4}\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_CHECK_EXIT", "")
	saved := selfExecutable
	selfExecutable = func() (string, error) { return path, nil }
	t.Cleanup(func() { selfExecutable = saved })
}

// What `cpb play --check --json` and `--dry-run --json` tell an agent
// before the person runs the store step: stored or not, the store, and the
// file store's path. No value is read for it.
func TestSecretReportNamesTheStoreAndThePath(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	t.Setenv(envset.SecretHelperEnv, "")
	dir := fileStoreHome(t)
	noSecretTool(t)
	fakeSelf(t)

	rep := secretReport([]string{"cpb:service-token", "keychain:x"})
	if len(rep) != 2 {
		t.Fatalf("report: %+v", rep)
	}
	e := rep[0]
	if e.Store != storeFile || e.Path != filepath.Join(dir, "service-token") || e.Stored == nil || *e.Stored {
		t.Fatalf("a cpb: reference not stored yet: %+v", e)
	}
	b, _ := json.Marshal(e)
	for _, want := range []string{`"ref":"cpb:service-token"`, `"stored":false`, `"store":"file"`, `"path":"` + filepath.Join(dir, "service-token") + `"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("JSON %s lacks %s", b, want)
		}
	}
	if rep[1].Store != "none" || rep[1].Stored != nil || !strings.Contains(rep[1].Note, "secret_helper") {
		t.Fatalf("another scheme with no helper: %+v", rep[1])
	}
	t.Setenv("FAKE_CHECK_EXIT", "0")
	if e := secretReport([]string{"cpb:service-token"})[0]; e.Stored == nil || !*e.Stored {
		t.Fatalf("a stored reference: %+v", e)
	}
	t.Setenv("FAKE_CHECK_EXIT", "5")
	if e := secretReport([]string{"cpb:service-token"})[0]; e.Stored != nil || e.Note == "" {
		t.Fatalf("a helper that cannot answer: %+v", e)
	}
	// A configured helper is named, and its own store is its business.
	storeHelper(t)
	if e := secretReport([]string{"cpb:service-token"})[0]; e.Store != "helper" || !strings.Contains(e.Helper, "store-helper") || e.Path != "" {
		t.Fatalf("a configured helper: %+v", e)
	}
}

// An export carries a cpb:<name> reference and never its value, and
// dropping the playbook leaves the file store as it was: nothing of cpb's
// playbook handling reaches that directory.
func TestExportCarriesTheReferenceAndTheFileStoreStays(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	dir := fileStoreHome(t)
	const value = "file-Store-Value-77"
	if err := (fileStore{dir: dir}).write("svc-token", []byte(value)); err != nil {
		t.Fatal(err)
	}
	store, _ := storeHelper(t) // the configured helper answers --check
	if err := os.WriteFile(filepath.Join(store, "cpb_svc-token"), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "CREATE PLAYBOOK exp")
	mustStmt(t, "ALTER PLAYBOOK exp SET VAR SVC_TOKEN FROM cpb:svc-token") // as the shell passes it, unquoted
	for _, line := range []string{"SHOW CREATE PLAYBOOK exp", "SHOW CREATE ALL"} {
		out, err := stmt(t, line)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		if !strings.Contains(out, "'cpb:svc-token'") || strings.Contains(out, value) {
			t.Fatalf("%s:\n%s", line, out)
		}
	}
	mustStmt(t, "DROP PLAYBOOK exp --yes")
	if b, err := os.ReadFile(filepath.Join(dir, "svc-token")); err != nil || string(b) != value {
		t.Fatalf("the file store changed: %q %v", b, err)
	}
	if m := mode(t, dir); m != 0o700 {
		t.Fatalf("directory mode %04o", m)
	}
}

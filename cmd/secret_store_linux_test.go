//go:build linux

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A stub secret-tool: items are files, its argv goes to a log, and store
// reads the value from stdin as secret-tool does.
func stubSecretTool(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	items := filepath.Join(dir, "items")
	os.Mkdir(items, 0o700)
	log := filepath.Join(dir, "secret-tool.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + log + `"
case "$1" in
  lookup) if [ -n "$STUB_NO_SERVICE" ]; then echo "secret-tool: $STUB_NO_SERVICE" >&2; exit 1; fi
          f="` + items + `/$5"; [ -f "$f" ] || exit 1; cat "$f"; exit 0 ;;
  store)  f="` + items + `/$7"; cat > "$f.part" && mv "$f.part" "$f"; exit 0 ;;
esac
exit 2
`
	path := filepath.Join(dir, "secret-tool")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUB_NO_SERVICE", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	saved := lookSecretTool
	lookSecretTool = func() (string, error) { return path, nil }
	t.Cleanup(func() { lookSecretTool = saved })
	return items, log
}

func TestSecretServiceStoreKeepsTheValueOffArgv(t *testing.T) {
	_, log := stubSecretTool(t)
	st, err := osDefaultSecretStore()
	if err != nil {
		t.Fatal(err)
	}
	value := []byte("ss-Value 7 with spaces")
	if ok, err := st.exists("tok"); ok || err != nil {
		t.Fatalf("exists before: %v %v", ok, err)
	}
	if err := st.write("tok", value); err != nil {
		t.Fatalf("write: %v", err)
	}
	if ok, err := st.exists("tok"); !ok || err != nil {
		t.Fatalf("exists after: %v %v", ok, err)
	}
	got, err := st.read("tok")
	if err != nil || string(got) != string(value) {
		t.Fatalf("read %q %v", got, err)
	}
	if argv := readLog(t, log); strings.Contains(argv, string(value)) {
		t.Fatal("the value reached secret-tool's argv")
	}
	if !strings.Contains(st.where("tok"), "cpb tok") {
		t.Fatalf("where: %s", st.where("tok"))
	}
}

// With no secret-tool the store is the file store, never a refusal.
func TestNoSecretToolUsesTheFileStore(t *testing.T) {
	dir := fileStoreHome(t)
	noSecretTool(t)
	st, err := osDefaultSecretStore()
	if err != nil {
		t.Fatalf("no secret-tool: %v", err)
	}
	if kind, path := st.target("tok"); kind != storeFile || path != filepath.Join(dir, "tok") {
		t.Fatalf("target: %s %s", kind, path)
	}
}

// lookup exits 1 both for a missing item and for no reachable Secret
// Service (libsecret 0.21, checked on a host with secret-tool and no
// keyring daemon): an empty stderr is "not there", an error on stderr is a
// store that cannot answer, named with its reason and the fix.
func TestSecretServiceUnreachableIsNotNotStored(t *testing.T) {
	stubSecretTool(t)
	tool, _ := lookSecretTool()
	st := secretServiceStore{tool: tool}
	if ok, err := st.exists("tok"); ok || err != nil {
		t.Fatalf("a missing item: %v %v", ok, err)
	}
	// The two errors seen on a real host (a session bus with no provider;
	// no bus at all), and any other: every non-empty stderr is a store
	// that cannot answer.
	for _, reason := range []string{
		"The name org.freedesktop.secrets was not provided by any .service files",
		"Cannot autolaunch D-Bus without X11 $DISPLAY",
		"some error a later libsecret prints",
	} {
		t.Setenv("STUB_NO_SERVICE", reason)
		ok, err := st.exists("tok")
		if ok || err == nil {
			t.Fatalf("%q read as %v %v", reason, ok, err)
		}
		for _, want := range []string{"no Secret Service can be reached", reason, "secret_helper"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("the error does not say %q: %v", want, err)
			}
		}
		if strings.Contains(err.Error(), "secret-tool: secret-tool") {
			t.Fatalf("the tool's prefix is doubled: %v", err)
		}
	}
}

// With the Secret Service out of reach, the default store falls back to
// the file store instead of failing: not there, and a new value goes to
// the file, named with the reason.
func TestUnreachableSecretServiceFallsBackToTheFile(t *testing.T) {
	stubSecretTool(t) // sets a data home of its own: replaced below
	dir := fileStoreHome(t)
	t.Setenv("STUB_NO_SERVICE", "Cannot autolaunch D-Bus without X11 $DISPLAY")
	st, err := osDefaultSecretStore()
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.exists("tok"); ok || err != nil {
		t.Fatalf("exists: %v %v", ok, err)
	}
	if kind, path := st.target("tok"); kind != storeFile || path != filepath.Join(dir, "tok") {
		t.Fatalf("target: %s %s", kind, path)
	}
	if w := st.where("tok"); !strings.Contains(w, "no keyring can be reached") || !strings.Contains(w, "root") {
		t.Fatalf("where: %s", w)
	}
}

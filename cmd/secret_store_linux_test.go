//go:build linux

package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
  lookup) [ -n "$STUB_SLOW" ] && sleep 5
          if [ -n "$STUB_NO_SERVICE" ]; then echo "secret-tool: $STUB_NO_SERVICE" >&2; exit 1; fi
          [ -n "$STUB_LOCKED" ] && exit 1
          f="` + items + `/$5"; [ -f "$f" ] || exit 1; cat "$f"; exit 0 ;;
  store)  f="` + items + `/$7"; cat > "$f.part" && mv "$f.part" "$f"; exit 0 ;;
  search) [ -n "$STUB_SEARCH_FAIL" ] && { echo "secret-tool: search failed" >&2; exit 2; }
          if [ -n "$STUB_LOCKED" ]; then echo "[/7]"; echo "label = cpb $5"; echo "secret-tool: Cannot get secret of a locked object" >&2; exit 0; fi
          f="` + items + `/$5"; [ -f "$f" ] && { echo "[/8]"; echo "secret = $(cat "$f")"; }; exit 0 ;;
esac
exit 2
`
	path := filepath.Join(dir, "secret-tool")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUB_NO_SERVICE", "")
	t.Setenv("STUB_LOCKED", "")
	t.Setenv("STUB_SLOW", "")
	t.Setenv("STUB_SEARCH_FAIL", "")
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

func TestNoSecretToolNamesTheFix(t *testing.T) {
	saved := lookSecretTool
	lookSecretTool = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { lookSecretTool = saved })
	if _, err := osDefaultSecretStore(); err == nil || !strings.Contains(err.Error(), "secret_helper") {
		t.Fatalf("no store: %v", err)
	}
}

// lookup exits 1 both for a missing item and for no reachable Secret
// Service (libsecret 0.21, checked on a host with secret-tool and no
// keyring daemon): an empty stderr is "not there", an error on stderr is a
// store that cannot answer, named with its reason and the fix.
func TestSecretServiceUnreachableIsNotNotStored(t *testing.T) {
	stubSecretTool(t)
	st, err := osDefaultSecretStore()
	if err != nil {
		t.Fatal(err)
	}
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

// A locked keyring answers lookup as a missing item does, also when its
// unlock prompt was dismissed, and secret-tool store would then replace
// the item once unlocked. search lists a locked item without unlocking it
// (libsecret 0.21, checked against GNOME Keyring), so a locked item is a
// store that cannot answer, and --store never runs over it.
func TestSecretServiceLockedItemIsNotNotStored(t *testing.T) {
	_, log := stubSecretTool(t)
	st, err := osDefaultSecretStore()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUB_LOCKED", "1")
	ok, err := st.exists("tok")
	if ok || err == nil || !strings.Contains(err.Error(), "locked") || !strings.Contains(err.Error(), "cpb:tok") {
		t.Fatalf("a locked item: %v %v", ok, err)
	}
	locks := t.TempDir()
	savedLocks, savedStore := secretLockDir, defaultSecretStore
	secretLockDir = func() (string, error) { return locks, nil }
	defaultSecretStore = func() (osSecretStore, error) { return st, nil }
	t.Cleanup(func() { secretLockDir, defaultSecretStore = savedLocks, savedStore })
	var herr error
	captureStderr(t, func() { herr = runSecretHelper([]string{"--store", "cpb:tok"}, strings.NewReader("new-value")) })
	if code, ok := exitCode(herr); !ok || code != unavailableExit {
		t.Fatalf("--store over a locked item: %v", herr)
	}
	if strings.Contains(readLog(t, log), "store --label") {
		t.Fatal("secret-tool store ran over a locked item")
	}
}

// A secret-tool that does not answer is not waited on: the store cannot
// answer, after toolTimeout.
func TestSecretServiceToolTimeout(t *testing.T) {
	stubSecretTool(t)
	saved := toolTimeout
	toolTimeout = 300 * time.Millisecond
	t.Cleanup(func() { toolTimeout = saved })
	t.Setenv("STUB_SLOW", "1")
	st, _ := osDefaultSecretStore()
	start := time.Now()
	ok, err := st.exists("tok")
	var ue *unavailableError
	if ok || !errors.As(err, &ue) || time.Since(start) > 4*time.Second {
		t.Fatalf("after %s: %v %v", time.Since(start), ok, err)
	}
}

// The Secret Service keeps 4096 bytes whole here; secret-tool itself cuts
// stdin at 8192 bytes without saying so (measured).
func TestSecretServiceLimit(t *testing.T) {
	stubSecretTool(t)
	st, _ := osDefaultSecretStore()
	if st.maxValue() != 4096 || st.maxValue() >= 8192 {
		t.Fatalf("maxValue %d", st.maxValue())
	}
}

// A search that fails is a store that cannot answer, never "not stored";
// and search runs without --unlock (which would prompt) or --all.
func TestSecretServiceSearchIsCheckedAndPlain(t *testing.T) {
	_, log := stubSecretTool(t)
	st, _ := osDefaultSecretStore()
	if ok, err := st.exists("tok"); ok || err != nil {
		t.Fatalf("a missing item: %v %v", ok, err)
	}
	for _, line := range strings.Split(readLog(t, log), "\n") {
		if strings.HasPrefix(line, "search") && line != "search service cpb name tok" {
			t.Fatalf("the search: %q", line)
		}
	}
	t.Setenv("STUB_SEARCH_FAIL", "1")
	if ok, err := st.exists("tok"); ok || err == nil {
		t.Fatalf("a failed search read as %v %v", ok, err)
	}
}

// A read secret-tool does not answer in time is a store that cannot
// answer, to errors.As as well (a launch then exits 5).
func TestSecretServiceReadTimeoutIsUnavailable(t *testing.T) {
	stubSecretTool(t)
	saved := toolTimeout
	toolTimeout = 300 * time.Millisecond
	t.Cleanup(func() { toolTimeout = saved })
	t.Setenv("STUB_SLOW", "1")
	tool, _ := lookSecretTool()
	_, err := secretServiceStore{tool: tool}.read("tok")
	var ue *unavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("a read that timed out: %v", err)
	}
}

//go:build darwin

package cmd

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A stub /usr/bin/security: items are files, its argv goes to a log, and
// -i takes the add command on stdin as security does.
func stubSecurity(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	items := filepath.Join(dir, "items")
	os.Mkdir(items, 0o700)
	log := filepath.Join(dir, "security.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + log + `"
case "$1" in
  find-generic-password)
    [ -n "$STUB_EXIT" ] && exit "$STUB_EXIT"
    f="` + items + `/$5"
    [ -f "$f" ] || exit 44
    if [ "$6" = "-w" ]; then cat "$f"; echo; fi
    exit 0 ;;
  -i)
    read -r line
    printf '%s\n' "$line" >> "` + log + `.stdin"
    if [ -n "$STUB_LOCKED" ]; then echo "SecKeychainItemCreateFromContent (<default>): User interaction is not allowed." >&2; exit 0; fi
    set -- $line
    [ "$1" = add-generic-password ] && [ "$8" = -X ] || exit 1
    [ -f "` + items + `/$5" ] && exit 1
    printf '%s' "$9" | /usr/bin/xxd -r -p > "` + items + `/$5.part" && mv "` + items + `/$5.part" "` + items + `/$5"
    exit 0 ;;
esac
exit 2
`
	path := filepath.Join(dir, "security")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUB_EXIT", "")
	t.Setenv("STUB_LOCKED", "")
	saved := securityPath
	securityPath = path
	t.Cleanup(func() { securityPath = saved })
	return items, log
}

func TestKeychainStoreKeepsTheValueOffArgv(t *testing.T) {
	_, log := stubSecurity(t)
	st, err := osDefaultSecretStore()
	if err != nil {
		t.Fatal(err)
	}
	value := []byte("kc-Value 7 with spaces")
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
	argv := readLog(t, log)
	if strings.Contains(argv, string(value)) || strings.Contains(argv, hex.EncodeToString(value)) {
		t.Fatal("the value reached security's argv")
	}
	if !strings.Contains(argv, "find-generic-password -s cpb -a tok") {
		t.Fatalf("argv %q", argv)
	}
	if !strings.Contains(st.where("tok"), "cpb/tok") {
		t.Fatalf("where: %s", st.where("tok"))
	}
}

// Only security's exit 44 (errSecItemNotFound) means "not there"; any
// other failure is a Keychain that could not answer, naming the fix.
func TestKeychainOnlyExit44IsNotStored(t *testing.T) {
	stubSecurity(t)
	st, _ := osDefaultSecretStore()
	for _, code := range []string{"36", "45", "51", "1"} {
		t.Setenv("STUB_EXIT", code)
		ok, err := st.exists("tok")
		if ok || err == nil || !strings.Contains(err.Error(), "security unlock-keychain") {
			t.Fatalf("exit %s read as %v %v", code, ok, err)
		}
	}
	t.Setenv("STUB_EXIT", "44")
	if ok, err := st.exists("tok"); ok || err != nil {
		t.Fatalf("exit 44: %v %v", ok, err)
	}
}

// The add never passes -U, so it never replaces an item, and carries the
// value as hex.
func TestKeychainAddHasNoU(t *testing.T) {
	_, log := stubSecurity(t)
	st, _ := osDefaultSecretStore()
	if err := st.write("tok", []byte("v-1")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(log + ".stdin")
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(b))
	if strings.Contains(" "+line+" ", " -U ") || !strings.Contains(line, " -X "+hex.EncodeToString([]byte("v-1"))) {
		t.Fatalf("the add command: %q", line)
	}
}

// A Keychain that is locked where it cannot ask for its password: the add
// fails with security's "interaction is not allowed" on stderr, which is
// kept in-process; the helper says so plainly and exits 5.
func TestKeychainLockedWithoutUIIsExit5(t *testing.T) {
	stubSecurity(t)
	t.Setenv("STUB_LOCKED", "1")
	st, _ := osDefaultSecretStore()
	err := st.write("tok", []byte("Value-77"))
	var ue *unavailableError
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), "locked") || strings.Contains(err.Error(), "Value-77") || strings.Contains(err.Error(), hex.EncodeToString([]byte("Value-77"))) {
		t.Fatalf("a locked Keychain: %v", err)
	}
	if st.maxValue() != 1536 {
		t.Fatalf("maxValue %d", st.maxValue())
	}
}

// The real limit, on a real store: on CI's macOS runner only, against a
// throwaway keychain file this test makes and deletes, never the default
// or login keychain. A value at keychainMaxValue under the longest name
// (the longest add line) is kept whole; the longest value kept whole is
// logged for the record; a second add of a name never replaces it; a
// missing name is exit 44.
func TestKeychainRealStoreKeepsTheCapWhole(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_OS") != "macOS" {
		t.Skip("runs only on CI's macOS runner, against a throwaway keychain it makes and deletes")
	}
	const real = "/usr/bin/security"
	saved := securityPath
	securityPath = real
	t.Cleanup(func() { securityPath = saved })
	kc := filepath.Join(t.TempDir(), "cpb-cap-test.keychain-db")
	if strings.ContainsAny(kc, " \t\"'") {
		t.Skipf("the throwaway keychain path has a character security -i would split on: %s", kc)
	}
	pw := make([]byte, 16)
	rand.Read(pw)
	if out, err := exec.Command(real, "create-keychain", "-p", hex.EncodeToString(pw), kc).CombinedOutput(); err != nil {
		t.Skipf("the throwaway keychain could not be made (%v: %s); this test never uses the default keychain", err, out)
	}
	t.Cleanup(func() { exec.Command(real, "delete-keychain", kc).Run() })
	if out, err := exec.Command(real, "unlock-keychain", "-p", hex.EncodeToString(pw), kc).CombinedOutput(); err != nil {
		t.Fatalf("unlock the throwaway keychain: %v: %s", err, out)
	}
	exec.Command(real, "set-keychain-settings", kc).Run() // no lock on a timeout
	st := keychainStore{keychain: kc}
	if ok, err := st.exists("missing"); ok || err != nil {
		t.Fatalf("a missing name: %v %v", ok, err)
	}
	value := func(n int) []byte {
		v := make([]byte, n)
		for i := range v {
			v[i] = byte('!' + i%94)
		}
		return v
	}
	name := strings.Repeat("n", 64)
	want := value(keychainMaxValue)
	if err := st.write(name, want); err != nil {
		t.Fatalf("write at the cap: %v", err)
	}
	got, err := st.read(name)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("a value at the cap came back %d bytes (want %d): %v", len(got), len(want), err)
	}
	st.write(name, value(10))
	if got, _ := st.read(name); !bytes.Equal(got, want) {
		t.Fatal("a second add replaced the item")
	}
	// For the record: the longest value kept whole on this runner, under a
	// 64-byte name, with this keychain's path in the line.
	lo, hi := keychainMaxValue, 4096
	for i := 0; lo < hi; i++ {
		mid := (lo + hi + 1) / 2
		n := strings.Repeat("p", 60) + fmt.Sprintf("%04d", i)
		v := value(mid)
		st.write(n, v)
		if got, err := st.read(n); err == nil && bytes.Equal(got, v) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	t.Logf("longest value kept whole: %d bytes (cap %d; the keychain path adds %d bytes to the line)", lo, keychainMaxValue, len(kc)+1)
	if lo < keychainMaxValue {
		t.Fatalf("the cap %d is over the measured limit %d", keychainMaxValue, lo)
	}
}

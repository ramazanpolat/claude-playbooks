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
	"time"
)

// A stub /usr/bin/security: items are files, its argv goes to a log, and
// -i takes the add command on stdin as security does. STUB_LOCKED makes it
// answer as the real one does for a Keychain locked where it cannot show
// its unlock dialog (recorded over ssh localhost on GitHub's macos-15-arm64
// and macos-26-arm64 images): the search without -w answers as usual;
// find -w exits 36 with nothing on stderr; show-keychain-info and the add
// exit 36 with the lines below. Each sign is also tried alone:
// STUB_LOCKED=text prints the add's lines in another language (the number
// only) with exit 0, STUB_LOCKED=english the English text without the
// number with exit 0, and STUB_LOCKED=exit nothing, with exit 36.
// STUB_SLOW makes the searches and show-keychain-info take 5 s.
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
    [ -n "$STUB_SLOW" ] && sleep 5
    [ -n "$STUB_EXIT" ] && exit "$STUB_EXIT"
    f="` + items + `/$5"
    [ -f "$f" ] || exit 44
    if [ "$6" = "-w" ]; then
      [ -n "$STUB_LOCKED" ] && exit 36
      cat "$f"; echo
    fi
    exit 0 ;;
  show-keychain-info)
    [ -n "$STUB_SLOW" ] && sleep 5
    if [ -n "$STUB_LOCKED" ]; then echo "security: SecKeychainCopySettings <keychain>: User interaction is not allowed." >&2; exit 36; fi
    [ -n "$STUB_INFO_EXIT" ] && exit "$STUB_INFO_EXIT"
    exit 0 ;;
  -i)
    read -r line
    printf '%s\n' "$line" >> "` + log + `.stdin"
    case "$STUB_LOCKED" in
      "") ;;
      text) printf 'security: SecKeychainItemCreateFromContent (<keychain>): Interaktion mit dem Benutzer ist nicht erlaubt.\nadd-generic-password: returned -25308\n' >&2; exit 0 ;;
      english) printf 'security: SecKeychainItemCreateFromContent (<keychain>): User interaction is not allowed.\n' >&2; exit 0 ;;
      exit) exit 36 ;;
      *) printf 'security: SecKeychainItemCreateFromContent (<keychain>): User interaction is not allowed.\nadd-generic-password: returned -25308\n' >&2; exit 36 ;;
    esac
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
	t.Setenv("STUB_INFO_EXIT", "")
	t.Setenv("STUB_SLOW", "")
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
	if err := st.write("tok", value, ""); err != nil {
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
	if err := st.write("tok", []byte("v-1"), ""); err != nil {
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
// fails as recorded (exit 36, the lines with -25308 on stderr), and each
// sign alone is enough: the text in another language with exit 0, or exit
// 36 with nothing printed. stderr is kept in-process; the store says the
// Keychain is locked, that nothing was stored, and how to unlock it.
func TestKeychainLockedWithoutUIIsExit5(t *testing.T) {
	stubSecurity(t)
	st, _ := osDefaultSecretStore()
	for _, mode := range []string{"1", "text", "english", "exit"} {
		t.Setenv("STUB_LOCKED", mode)
		err := st.write("tok", []byte("Value-77"), "")
		var ue *unavailableError
		if !errors.As(err, &ue) || !strings.Contains(err.Error(), "locked") || !strings.Contains(err.Error(), "nothing was stored") || !strings.Contains(err.Error(), "security unlock-keychain") || strings.Contains(err.Error(), "Value-77") || strings.Contains(err.Error(), hex.EncodeToString([]byte("Value-77"))) {
			t.Fatalf("a locked Keychain (%s): %v", mode, err)
		}
	}
	if st.maxValue() != 1536 {
		t.Fatalf("maxValue %d", st.maxValue())
	}
}

// The Keychain never takes a write meant for another store.
func TestKeychainWriteKeepsToTheNamedStore(t *testing.T) {
	_, log := stubSecurity(t)
	st, _ := osDefaultSecretStore()
	var ue *unavailableError
	if err := st.write("tok", []byte("v-1"), storeFile); !errors.As(err, &ue) || strings.Contains(readLog(t, log), "-i") {
		t.Fatalf("a write named for the file store: %v", err)
	}
	if err := st.write("tok", []byte("v-1"), storeKeychain); err != nil {
		t.Fatal(err)
	}
}

// A presence check brings up nothing: the search answers on a locked
// Keychain as on an unlocked one, and a name that is not there is "not
// stored" without any further question (no show-keychain-info, which
// could bring up the unlock dialog). Reading a locked Keychain says it is
// locked.
func TestKeychainPresenceAsksNoUnlock(t *testing.T) {
	items, log := stubSecurity(t)
	st, _ := osDefaultSecretStore()
	t.Setenv("STUB_LOCKED", "1")
	if ok, err := st.exists("tok"); ok || err != nil {
		t.Fatalf("locked, absent: %v %v", ok, err)
	}
	os.WriteFile(filepath.Join(items, "tok"), []byte("v"), 0o600)
	if ok, err := st.exists("tok"); !ok || err != nil {
		t.Fatalf("locked, present: %v %v", ok, err)
	}
	if strings.Contains(readLog(t, log), "show-keychain-info") {
		t.Fatal("a presence check asked show-keychain-info")
	}
	var ue *unavailableError
	if v, err := st.read("tok"); v != nil || !errors.As(err, &ue) || !strings.Contains(err.Error(), "locked") || !strings.Contains(err.Error(), "security unlock-keychain") {
		t.Fatalf("locked, read: %q %v", v, err)
	}
}

// readyToStore asks show-keychain-info, which carries no value: unlocked
// it is ready; locked where it cannot ask (exit 36) it says so; any other
// failure, or no answer within toolTimeout, is a store that cannot answer.
func TestKeychainReadyToStore(t *testing.T) {
	_, log := stubSecurity(t)
	st, _ := osDefaultSecretStore()
	if err := st.readyToStore("tok"); err != nil {
		t.Fatalf("unlocked: %v", err)
	}
	if !strings.Contains(readLog(t, log), "show-keychain-info") {
		t.Fatal("readyToStore did not ask show-keychain-info")
	}
	var ue *unavailableError
	t.Setenv("STUB_LOCKED", "1")
	if err := st.readyToStore("tok"); !errors.As(err, &ue) || !strings.Contains(err.Error(), "locked") || !strings.Contains(err.Error(), "security unlock-keychain") || !strings.Contains(err.Error(), "nothing was asked for") {
		t.Fatalf("locked: %v", err)
	}
	t.Setenv("STUB_LOCKED", "")
	t.Setenv("STUB_INFO_EXIT", "51")
	if err := st.readyToStore("tok"); !errors.As(err, &ue) || !strings.Contains(err.Error(), "security unlock-keychain") {
		t.Fatalf("show-keychain-info failing otherwise: %v", err)
	}
	t.Setenv("STUB_INFO_EXIT", "")
	saved := toolTimeout
	toolTimeout = 300 * time.Millisecond
	t.Cleanup(func() { toolTimeout = saved })
	t.Setenv("STUB_SLOW", "1")
	start := time.Now()
	if err := st.readyToStore("tok"); !errors.As(err, &ue) || !strings.Contains(err.Error(), "did not answer within") || strings.Contains(err.Error(), "cpb/") || time.Since(start) > 3*time.Second {
		t.Fatalf("a dialog nobody answers, after %s: %v", time.Since(start), err)
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
	// On the runner the outcome goes to the job's summary, where CI's
	// plain `go test` shows it, and not running there is a failure.
	notRun := func(format string, a ...any) {
		reason := fmt.Sprintf(format, a...)
		jobSummary(t, "Keychain cap test: NOT RUN on the macOS runner: "+reason)
		t.Fatalf("the Keychain cap test did not run on the macOS runner: %s", reason)
	}
	const real = "/usr/bin/security"
	saved := securityPath
	securityPath = real
	t.Cleanup(func() { securityPath = saved })
	kc := filepath.Join(t.TempDir(), "cpb-cap-test.keychain-db")
	if strings.ContainsAny(kc, " \t\"'") {
		notRun("the throwaway keychain path has a character security -i would split on: %s", kc)
	}
	pw := make([]byte, 16)
	rand.Read(pw)
	if out, err := exec.Command(real, "create-keychain", "-p", hex.EncodeToString(pw), kc).CombinedOutput(); err != nil {
		notRun("the throwaway keychain could not be made (%v: %s); this test never uses the default keychain", err, out)
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
	// show-keychain-info, the store step's question before it asks for a
	// value, answers 0 for an unlocked keychain.
	if err := st.readyToStore("tok"); err != nil {
		t.Fatalf("an unlocked keychain is not ready: %v", err)
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
	if err := st.write(name, want, ""); err != nil {
		t.Fatalf("write at the cap: %v", err)
	}
	got, err := st.read(name)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("a value at the cap came back %d bytes (want %d): %v", len(got), len(want), err)
	}
	st.write(name, value(10), "")
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
		st.write(n, v, "")
		if got, err := st.read(n); err == nil && bytes.Equal(got, v) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	t.Logf("longest value kept whole: %d bytes (cap %d; the keychain path adds %d bytes to the line)", lo, keychainMaxValue, len(kc)+1)
	jobSummary(t, fmt.Sprintf("Keychain cap test: ran on the macOS runner. A %d-byte value under a 64-byte name came back whole; the longest value kept whole was %d bytes (the throwaway keychain's path adds %d bytes to the line).", keychainMaxValue, lo, len(kc)+1))
	if lo < keychainMaxValue {
		t.Fatalf("the cap %d is over the measured limit %d", keychainMaxValue, lo)
	}
}

// A security that does not answer is not waited on: the Keychain cannot
// answer, after toolTimeout.
func TestKeychainToolTimeout(t *testing.T) {
	stubSecurity(t)
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

// jobSummary appends a line to the GitHub Actions job summary, when there
// is one.
func jobSummary(t *testing.T, line string) {
	t.Helper()
	p := os.Getenv("GITHUB_STEP_SUMMARY")
	if p == "" {
		return
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Logf("job summary: %v", err)
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

// A read security does not answer in time is a store that cannot answer,
// to errors.As as well (a launch then exits 5).
func TestKeychainReadTimeoutIsUnavailable(t *testing.T) {
	stubSecurity(t)
	saved := toolTimeout
	toolTimeout = 300 * time.Millisecond
	t.Cleanup(func() { toolTimeout = saved })
	t.Setenv("STUB_SLOW", "1")
	_, err := keychainStore{}.read("tok")
	var ue *unavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("a read that timed out: %v", err)
	}
}

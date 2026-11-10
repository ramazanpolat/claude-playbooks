//go:build darwin

package cmd

import (
	"encoding/hex"
	"os"
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
    f="` + items + `/$5"
    [ -f "$f" ] || exit 44
    if [ "$6" = "-w" ]; then cat "$f"; echo; fi
    exit 0 ;;
  -i)
    read -r line
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

package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

// Values are arbitrary bytes from the shell; the manifest must stay readable
// whatever they contain, and refuse what TOML cannot hold at all.
func TestEnvValuesRoundTripThroughTOML(t *testing.T) {
	dir := t.TempDir()
	values := map[string]string{
		"CTRL":  "a\x1bb\vc\ad\x7fe",
		"QUOTE": `say "hi" \ back`,
		"MULTI": "line1\nline2\ttab",
		"UNI":   "héllo wörld ✓",
	}
	if err := Write(dir, &Manifest{Name: "pb", Env: &Env{Set: values}}); err != nil {
		t.Fatal(err)
	}
	m, err := Read(dir)
	if err != nil {
		t.Fatalf("manifest unreadable after writing control characters: %v", err)
	}
	for k, want := range values {
		if got := m.Env.Set[k]; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if err := Write(dir, &Manifest{Name: "pb", Env: &Env{Set: map[string]string{"BAD": "\xff\xfe"}}}); err == nil {
		t.Fatal("invalid UTF-8 value accepted")
	}
	if got := QuoteTOML("\x1b[0m"); got != `"\u001B[0m"` {
		t.Fatalf("QuoteTOML(escape) = %s", got)
	}
	// NUL is valid TOML (\u0000) but os/exec refuses it in an environment:
	// rejected on write, and on read of a hand-edited manifest.
	if err := Write(dir, &Manifest{Name: "pb", Env: &Env{Set: map[string]string{"NUL": "a\x00b"}}}); err == nil {
		t.Fatal("NUL value accepted on write")
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("name = \"pb\"\n[env.set]\nNUL = \"a\\u0000b\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err == nil {
		t.Fatal("NUL value accepted on read")
	}
}

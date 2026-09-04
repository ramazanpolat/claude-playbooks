package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEnvFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "work.env")
	body := strings.Join([]string{
		"# comment",
		"",
		"PLAIN=one",
		"export EXPORTED=two",
		`DQ="three with spaces"`,
		"SQ='four'",
		"EQ=a=b=c",
		"  SPACED  =  five  ",
		"PLAIN=overridden",
	}, "\n") + "\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := ParseEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"PLAIN": "overridden", "EXPORTED": "two", "DQ": "three with spaces",
		"SQ": "four", "EQ": "a=b=c", "SPACED": "five",
	}
	for k, v := range want {
		if e.Set[k] != v {
			t.Errorf("%s = %q, want %q", k, e.Set[k], v)
		}
	}
	if len(e.Set) != len(want) || len(e.Unset) != 0 {
		t.Fatalf("unexpected entries: %#v", e)
	}
}

func TestParseEnvFileRejects(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"no equals":   "JUSTAKEY\n",
		"bad key":     "BAD-KEY=1\n",
		"reserved":    "CLAUDE_CONFIG_DIR=/x\n",
		"nul":         "K=a\x00b\n",
		"invalid utf": "K=\xff\n",
	} {
		p := filepath.Join(dir, "f.env")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ParseEnvFile(p); err == nil {
			t.Errorf("%s: accepted %q", name, body)
		} else if !strings.Contains(err.Error(), "f.env:1") {
			t.Errorf("%s: error does not name file:line: %v", name, err)
		}
	}
	if _, err := ParseEnvFile(filepath.Join(dir, "absent.env")); err == nil {
		t.Fatal("missing file accepted")
	}
}

// A malformed line is reported by position only: the content may be a
// pasted secret, and stderr is often logged.
func TestParseEnvFileDoesNotEchoLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.env")
	if err := os.WriteFile(p, []byte("sk-ant-oat01-SECRETVALUE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ParseEnvFile(p)
	if err == nil || strings.Contains(err.Error(), "SECRETVALUE") {
		t.Fatalf("error echoes the line: %v", err)
	}
}

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
)

func TestTakeLaunchFlagsLeadingRunOnly(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "w.env")
	if err := os.WriteFile(envFile, []byte("FROM_FILE=yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rest, layers, err := takeLaunchFlags([]string{
		"--env-profile", "glm", "--env=K=V", "--unset", "TOKEN", "--env-file", envFile,
		"router", "-p", "hi", "--env", "NOT_OURS=1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 5 || rest[0] != "router" || rest[3] != "--env" {
		t.Fatalf("rest = %v; the scan must stop at the first non-flag argument", rest)
	}
	if len(layers) != 4 {
		t.Fatalf("layers = %d, want 4", len(layers))
	}
	if !layers[0].Uses("glm") || layers[1].Set["K"] != "V" || !layers[2].Unsets("TOKEN") || layers[3].Set["FROM_FILE"] != "yes" {
		t.Fatalf("layers = %#v", layers)
	}
	// No flags: nothing consumed, nothing produced.
	rest, layers, err = takeLaunchFlags([]string{"router", "--env", "K=V"})
	if err != nil || len(rest) != 3 || len(layers) != 0 {
		t.Fatalf("rest=%v layers=%v err=%v", rest, layers, err)
	}
}

func TestTakeLaunchFlagsRejects(t *testing.T) {
	for _, args := range [][]string{
		{"--env"},
		{"--env", "NOEQUALS"},
		{"--env", "CLAUDE_CONFIG_DIR=/x"},
		{"--env", "BAD-KEY=1"},
		{"--unset", "K=V"},
		{"--unset", "CLAUDE_CONFIG_DIR"},
		{"--env-profile", "bad name"},
		{"--env-file", filepath.Join(t.TempDir(), "absent.env")},
	} {
		if _, _, err := takeLaunchFlags(args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

// --help at the name/path position prints usage even when launch flags
// precede it; it must not be taken as a playbook name or a path.
func TestHelpAfterLaunchFlagsPrintsUsage(t *testing.T) {
	resetCommandTestState(t)
	config.PlaybooksDir = t.TempDir()
	for _, tc := range []struct {
		name string
		run  func([]string) error
		args []string
		want string
	}{
		{"run", func(a []string) error { return runRun(nil, a) }, []string{"--env", "K=V", "--help"}, "Usage: claude-playbook run"},
		{"start", func(a []string) error { return runStart(nil, a) }, []string{"--env-file=" + writeTempEnvFile(t), "-h"}, "Usage: claude-playbook start"},
	} {
		out := captureStdout(t, func() {
			if err := tc.run(tc.args); err != nil {
				t.Fatalf("%s %v: %v", tc.name, tc.args, err)
			}
		})
		if !strings.Contains(out, tc.want) {
			t.Fatalf("%s %v printed:\n%s", tc.name, tc.args, out)
		}
	}
	if entries, _ := os.ReadDir(config.PlaybooksDir); len(entries) != 0 {
		t.Fatalf("help created something: %v", entries)
	}
}

func writeTempEnvFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "h.env")
	if err := os.WriteFile(p, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

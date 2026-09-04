package cmd

import (
	"os"
	"path/filepath"
	"testing"
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

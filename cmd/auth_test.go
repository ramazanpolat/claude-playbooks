package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

func TestAuthStatusTableAndJSON(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	home := os.Getenv("HOME")
	root := config.ResolvePlaybooksDir()
	global := filepath.Join(home, ".claude")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(3 * time.Hour).UnixMilli()
	store := filepath.Join(global, ".credentials.json")
	if err := os.WriteFile(store, []byte(`{"claudeAiOauth":{"accessToken":"x","expiresAt":`+jsonInt(exp)+`}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	shared := seedFlatPlaybook(t, "shared")
	if err := os.Symlink(store, filepath.Join(shared, ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	own := seedFlatPlaybook(t, "own")
	if err := manifest.Write(own, &manifest.Manifest{Name: "own", Env: &manifest.Env{Unset: []string{"CLAUDE_CODE_OAUTH_TOKEN"}}}); err != nil {
		t.Fatal(err)
	}
	_ = root

	authStatusJSON, authStatusClaude = false, false
	out := captureStdout(t, func() {
		if err := runAuthStatus(nil, nil); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"NAME", "MODE", "~/.claude", "shared-login", "shared", "symlink -> ~/.claude/.credentials.json", "in 3h00m", "own", "own-login", "no login"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "x\"") || strings.Contains(out, "accessToken") {
		t.Fatal("table leaked store content")
	}
	// An error row carries the documented note prefix.
	broken := seedFlatPlaybook(t, "broken")
	if err := manifest.Write(broken, &manifest.Manifest{Name: "broken", Env: &manifest.Env{Profiles: []string{"ghost"}}}); err != nil {
		t.Fatal(err)
	}
	errOut := captureStdout(t, func() {
		if err := runAuthStatus(nil, []string{"broken"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(errOut, "launch refused: env profile \"ghost\" not found") {
		t.Fatalf("error row note:\n%s", errOut)
	}

	authStatusJSON = true
	t.Cleanup(func() { authStatusJSON = false })
	raw := captureStdout(t, func() {
		if err := runAuthStatus(nil, []string{"shared"}); err != nil {
			t.Fatal(err)
		}
	})
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("json: %v\n%s", err, raw)
	}
	if len(rows) != 1 || rows[0]["name"] != "shared" || rows[0]["mode"] != "shared-login" || rows[0]["store"] != "symlink" || rows[0]["has_grant"] != true {
		t.Fatalf("json rows: %v", rows)
	}
}

func jsonInt(n int64) string { return strconv.FormatInt(n, 10) }

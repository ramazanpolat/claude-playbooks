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
	if err := manifest.Write(own, &manifest.Manifest{Name: "own", Env: &manifest.Env{Block: []string{"CLAUDE_CODE_OAUTH_TOKEN"}}}); err != nil {
		t.Fatal(err)
	}
	_ = root

	authStatusJSON, authStatusClaude = false, false
	out := captureStdout(t, func() {
		if err := runAuthStatus(nil, nil); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"NAME", "MODE", "~/.claude", "shared-login", "shared", "symlink -> ~/.claude/.credentials.json", "in 3h00m", "own", "shared-login (token blocked)", "no login"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "x\"") || strings.Contains(out, "accessToken") {
		t.Fatal("table leaked store content")
	}
	// An error row carries the documented note prefix.
	broken := seedFlatPlaybook(t, "broken")
	if err := manifest.Write(broken, &manifest.Manifest{Name: "broken", Env: &manifest.Env{Sets: []string{"ghost"}}}); err != nil {
		t.Fatal(err)
	}
	errOut := captureStdout(t, func() {
		if err := runAuthStatus(nil, []string{"broken"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(errOut, "launch refused: env set \"ghost\" not found") {
		t.Fatalf("error row note:\n%s", errOut)
	}

	// Times are UTC whatever the local zone.
	local := time.Local
	time.Local = time.FixedZone("UTC+3", 3*3600)
	t.Cleanup(func() { time.Local = local })
	authStatusJSON = true
	t.Cleanup(func() { authStatusJSON = false })
	raw := captureStdout(t, func() {
		if err := runAuthStatus(nil, []string{"shared", "own"}); err != nil {
			t.Fatal(err)
		}
	})
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("json: %v\n%s", err, raw)
	}
	if len(rows) != 2 || rows[0]["name"] != "shared" || rows[0]["mode"] != "shared-login" || rows[0]["token_blocked"] != false ||
		rows[0]["isolated_login"] != false || rows[0]["store"] != "symlink" || rows[0]["has_grant"] != true {
		t.Fatalf("json rows: %v", rows)
	}
	if e, _ := rows[0]["expires_at"].(string); !strings.HasSuffix(e, "Z") {
		t.Errorf("expires_at %q is not UTC", e)
	}
	if _, old := rows[0]["isolated"]; old {
		t.Errorf("the v3 key isolated is still emitted: %v", rows[0])
	}
	// A playbook that blocks the machine's token uses the stored login.
	if rows[1]["name"] != "own" || rows[1]["mode"] != "shared-login" || rows[1]["token_blocked"] != true {
		t.Fatalf("token-blocked row: %v", rows[1])
	}
}

// --claude reports claude auth status --json under cpb's snake_case keys.
func TestAuthStatusClaudeKeys(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "plain")
	bin := filepath.Join(os.Getenv("HOME"), "stub-bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\nprintf '%s\\n' '{\"loggedIn\":true,\"subscriptionType\":\"max\",\"authMethod\":\"claude.ai\"}'\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	authStatusJSON, authStatusClaude = true, true
	t.Cleanup(func() { authStatusJSON, authStatusClaude = false, false })
	raw := captureStdout(t, func() {
		if err := runAuthStatus(nil, []string{"plain"}); err != nil {
			t.Fatal(err)
		}
	})
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("json: %v\n%s", err, raw)
	}
	c, _ := rows[0]["claude"].(map[string]any)
	if c["logged_in"] != true || c["subscription_type"] != "max" || c["auth_method"] != "claude.ai" || len(c) != 3 {
		t.Fatalf("claude field: %v\n%s", c, raw)
	}
}

func jsonInt(n int64) string { return strconv.FormatInt(n, 10) }

// Unknown instants are omitted from JSON, never emitted as 0001-01-01.
func TestAuthStatusJSONOmitsUnknownInstants(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "plain")
	authStatusJSON = true
	t.Cleanup(func() { authStatusJSON = false })
	raw := captureStdout(t, func() {
		if err := runAuthStatus(nil, []string{"plain"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(raw, "0001-01-01") || strings.Contains(raw, "expires_at") || strings.Contains(raw, "daemon_since") {
		t.Fatalf("unknown instants leaked into JSON:\n%s", raw)
	}
}

// --claude output survives JSON encoding: the embedded Report's MarshalJSON
// must not swallow the claude field. With no claude binary on PATH the field
// carries the per-row error rather than aborting the command.
func TestAuthStatusJSONKeepsClaudeField(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "plain")
	t.Setenv("PATH", filepath.Join(os.Getenv("HOME"), "stub-bin"))
	authStatusJSON, authStatusClaude = true, true
	t.Cleanup(func() { authStatusJSON, authStatusClaude = false, false })
	raw := captureStdout(t, func() {
		if err := runAuthStatus(nil, []string{"plain"}); err != nil {
			t.Fatal(err)
		}
	})
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("json: %v\n%s", err, raw)
	}
	c, ok := rows[0]["claude"].(map[string]any)
	if !ok {
		t.Fatalf("claude field missing from JSON:\n%s", raw)
	}
	if c["error"] != "claude not on PATH" || rows[0]["mode"] != "shared-login" {
		t.Fatalf("row: %v", rows[0])
	}
}

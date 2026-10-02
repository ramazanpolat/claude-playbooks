package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/settings"
)

// fakeMCP stands in for `claude mcp`: add-json and remove edit mcpServers in
// the config directory's .claude.json as Claude Code does (add refuses an
// existing name), and every call is logged.
func fakeMCP(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	old := claudeMCP
	claudeMCP = func(dir string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args[:2], " "))
		path := filepath.Join(dir, claudeJSON)
		root := settings.NewObject()
		if data, err := os.ReadFile(path); err == nil {
			root, _ = settings.ParseObject(data)
		}
		servers, _ := root.Object("mcpServers")
		switch args[0] {
		case "add-json":
			if servers.Has(args[1]) {
				return nil, errString("MCP server " + args[1] + " already exists in user config")
			}
			var cfg any
			_ = json.Unmarshal([]byte(args[2]), &cfg)
			_ = servers.Set(args[1], cfg)
		case "remove":
			if !servers.Delete(args[1]) {
				return nil, errString("No MCP server named " + args[1])
			}
		}
		_ = root.Set("mcpServers", servers)
		data, _ := root.MarshalJSON()
		return nil, os.WriteFile(path, data, 0o600)
	}
	t.Cleanup(func() { claudeMCP = old })
	return &calls
}

type errString string

func (e errString) Error() string { return string(e) }

func mcpConfigOf(t *testing.T, dir, name string) map[string]any {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(dir, claudeJSON))
	var root struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	_ = json.Unmarshal(data, &root)
	return root.MCPServers[name]
}

func TestMCPServerLifecycle(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	helper, _ := fakeHelper(t)
	calls := fakeMCP(t)
	root := seedFlatPlaybook(t, "k")
	mustStmt(t, "ALTER DEFAULTS SET SECRET HELPER "+helper)

	add := "ALTER PLAYBOOK k ADD MCP SERVER files COMMAND npx ARGS -y server-fs VAR LOG_LEVEL=debug VAR API_TOKEN FROM keychain:ok/fs"
	mustStmt(t, add)
	cfg := mcpConfigOf(t, root, "files")
	env, _ := cfg["env"].(map[string]any)
	v := mcpVar("files", "E", "API_TOKEN")
	if env["API_TOKEN"] != "${"+v+"}" || env["LOG_LEVEL"] != "debug" {
		t.Fatalf("config holds %v, want the placeholder for %s", env, v)
	}
	m, _ := manifest.Read(root)
	if m.Env == nil || m.Env.Refs[v] != "keychain:ok/fs" || m.MCP["files"] == nil || m.MCP["files"].Vars[0] != v {
		t.Fatalf("manifest: %+v %+v", m.Env, m.MCP)
	}
	if len(*calls) != 1 || (*calls)[0] != "add-json files" {
		t.Fatalf("calls: %v", *calls)
	}

	// Already true: nothing runs.
	if out := mustStmt(t, add); !strings.Contains(out, "unchanged") || len(*calls) != 1 {
		t.Fatalf("a repeat ran %v:\n%s", *calls, out)
	}

	// SHOW shows the reference, SHOW CREATE writes it back as FROM, and the
	// derived variable is not written as a variable of its own.
	show := mustStmt(t, "SHOW PLAYBOOK k --json")
	if !strings.Contains(show, `"ref": "keychain:ok/fs"`) || !strings.Contains(show, `"mcp_servers"`) {
		t.Fatalf("SHOW --json:\n%s", show)
	}
	create := mustStmt(t, "SHOW CREATE PLAYBOOK k")
	if !strings.Contains(create, "ADD MCP SERVER files COMMAND 'npx' ARGS '-y' 'server-fs'") ||
		!strings.Contains(create, "VAR API_TOKEN FROM 'keychain:ok/fs'") || strings.Contains(create, "SET VAR CPB_MCP_") {
		t.Fatalf("SHOW CREATE:\n%s", create)
	}

	// A changed declaration is removed and added again.
	mustStmt(t, "ALTER PLAYBOOK k ADD MCP SERVER files COMMAND npx ARGS -y server-fs /srv VAR LOG_LEVEL=debug VAR API_TOKEN FROM keychain:ok/fs")
	if got := strings.Join((*calls)[1:], ","); got != "remove files,add-json files" {
		t.Fatalf("replacement ran %s", got)
	}

	// Dropping it forgets exactly its derived variable.
	mustStmt(t, "ALTER PLAYBOOK k DROP MCP SERVER files")
	m, _ = manifest.Read(root)
	if mcpConfigOf(t, root, "files") != nil || (m.Env != nil && m.Env.Refs[v] != "") || m.MCP != nil {
		t.Fatalf("after DROP: %+v %+v", m.Env, m.MCP)
	}
}

// A dry run lists the commands and runs none; an unresolvable reference is
// refused before anything is written.
func TestMCPDryRunAndRefCheck(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	helper, _ := fakeHelper(t)
	calls := fakeMCP(t)
	root := seedFlatPlaybook(t, "k")
	mustStmt(t, "ALTER DEFAULTS SET SECRET HELPER "+helper)
	path := writePlaybookFile(t, "ALTER PLAYBOOK k ADD MCP SERVER web URL 'https://mcp.example.com/mcp' HEADER 'Authorization' FROM 'keychain:ok/web';\n")
	out, err := apply(t, path, "--dry-run")
	if err != nil || !strings.Contains(out, "would run: claude mcp add-json web --scope user") || len(*calls) != 0 {
		t.Fatalf("dry run: %v %v\n%s", err, *calls, out)
	}
	if _, err := stmt(t, "ALTER PLAYBOOK k ADD MCP SERVER web URL https://mcp.example.com/mcp HEADER Authorization FROM keychain:gone"); err == nil || len(*calls) != 0 {
		t.Fatalf("an unresolvable reference: %v, calls %v", err, *calls)
	}
	if m, _ := manifest.Read(root); m != nil && m.Env != nil && len(m.Env.Refs) > 0 {
		t.Fatalf("the refused statement wrote %v", m.Env.Refs)
	}
}

func TestMCPVarIsCollisionFree(t *testing.T) {
	a, b := mcpVar("s", "E", "AUTH"), mcpVar("s", "H", "AUTH")
	c, d := mcpVar("a-b", "E", "K"), mcpVar("a_b", "E", "K")
	if a == b || c == d {
		t.Fatalf("collisions: %s %s / %s %s", a, b, c, d)
	}
	if !strings.HasPrefix(a, "CPB_MCP_S_E_AUTH_") {
		t.Fatalf("not readable: %s", a)
	}
}

// APPLY checks an MCP server's references before writing anything, and
// plugin and MCP commands run in the order their clauses are written.
func TestMCPApplyPreflightAndOrder(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	helper, _ := fakeHelper(t)
	calls := fakeMCP(t)
	root := seedFlatPlaybook(t, "k")
	mustStmt(t, "ALTER DEFAULTS SET SECRET HELPER "+helper)
	bad := writePlaybookFile(t, "ALTER PLAYBOOK k SET VAR A=1;\nALTER PLAYBOOK k ADD MCP SERVER web URL 'https://x.example/mcp' HEADER 'Authorization' FROM 'keychain:gone';\n")
	if _, err := apply(t, bad); err == nil || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("preflight: %v", err)
	}
	if m, _ := manifest.Read(root); m != nil && m.Env != nil && m.Env.Set["A"] != "" {
		t.Fatal("the first statement ran before the refused reference")
	}
	if len(*calls) != 0 {
		t.Fatalf("calls: %v", *calls)
	}

	// A failed replacement keeps the old server's reference.
	mustStmt(t, "ALTER PLAYBOOK k ADD MCP SERVER s COMMAND x VAR TOKEN FROM keychain:ok/a")
	v := mcpVar("s", "E", "TOKEN")
	old := claudeMCP
	claudeMCP = func(dir string, args ...string) ([]byte, error) { return nil, errString("boom") }
	if _, err := stmt(t, "ALTER PLAYBOOK k ADD MCP SERVER s COMMAND x VAR OTHER FROM keychain:ok/b"); err == nil {
		t.Fatal("the failing command was not reported")
	}
	claudeMCP = old
	if m, _ := manifest.Read(root); m.Env.Refs[v] != "keychain:ok/a" {
		t.Fatalf("the old reference was forgotten before the command succeeded: %v", m.Env.Refs)
	}
}

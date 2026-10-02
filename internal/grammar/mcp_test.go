package grammar

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseMCPServers(t *testing.T) {
	stmts, err := ParseFile(`ALTER PLAYBOOK k
  ADD MCP SERVER files COMMAND 'npx' ARGS '-y' 'server-fs' '/srv' VAR LOG_LEVEL=debug VAR API_TOKEN FROM 'keychain:fs'
  ADD MCP SERVER sentry URL 'https://mcp.sentry.dev/mcp' HEADER 'Authorization' FROM 'keychain:sentry' HEADER 'X-Team' 'core'
  ADD MCP SERVER old URL 'https://old.example.com/sse' TRANSPORT SSE
  DROP MCP SERVER gone;`)
	if err != nil {
		t.Fatal(err)
	}
	c := stmts[0].Clauses
	want := &MCP{Command: "npx", Args: []string{"-y", "server-fs", "/srv"},
		Env: []Var{{Key: "LOG_LEVEL", Value: "debug"}, {Key: "API_TOKEN", Ref: "keychain:fs"}}}
	if c[0].Kind != AddMCP || c[0].Names[0] != "files" || !reflect.DeepEqual(c[0].MCP, want) {
		t.Errorf("stdio server: %+v %+v", c[0], c[0].MCP)
	}
	if m := c[1].MCP; m.URL != "https://mcp.sentry.dev/mcp" || len(m.Headers) != 2 || m.Headers[0].Ref != "keychain:sentry" || m.Headers[1].Value != "core" {
		t.Errorf("remote server: %+v", m)
	}
	if !c[2].MCP.SSE || c[3].Kind != DropMCP || c[3].Names[0] != "gone" {
		t.Errorf("sse / drop: %+v %+v", c[2], c[3])
	}
	again, err := ParseFile(stmts[0].String())
	if err != nil || !reflect.DeepEqual(strip(again[0]), strip(stmts[0])) {
		t.Fatalf("round trip: %v\n%s", err, stmts[0].String())
	}
}

func TestParseMCPRefusals(t *testing.T) {
	for src, want := range map[string]string{
		`ALTER PLAYBOOK k ADD MCP SERVER s COMMAND 'x' VAR API_TOKEN=sk-live-1234567890;`:                "takes a reference",
		`ALTER PLAYBOOK k ADD MCP SERVER s COMMAND 'x' VAR API_TOKEN=sk-live-1234567890 AS PLAINTEXT;`:   "takes a reference",
		`ALTER PLAYBOOK k ADD MCP SERVER s URL 'https://x.example' HEADER 'Authorization' 'Bearer abc';`: "carries a credential",
		`ALTER PLAYBOOK k ADD MCP SERVER s COMMAND 'x' HEADER 'X-A' 'b';`:                                "HEADER applies to a remote server",
		`ALTER PLAYBOOK k ADD MCP SERVER s URL 'https://u:p@x.example';`:                                 "carrying credentials",
		`ALTER PLAYBOOK k ADD MCP SERVER s;`:                                                             "COMMAND '<command>' or URL '<url>'",
		`ALTER PLAYBOOK k ADD MCP s COMMAND 'x';`:                                                        "takes SERVER",
	} {
		_, err := ParseFile(src)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s\n  error %v, want %q", src, err, want)
		}
		if err != nil && strings.Contains(err.Error(), "sk-live") {
			t.Errorf("an error echoes the value: %v", err)
		}
	}
}

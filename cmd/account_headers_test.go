package cmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// An account's request headers: SET header.<Name> FROM '<ref>', references
// only, shown by name and reference, never a value.
func TestAccountHeadersStatements(t *testing.T) {
	sandboxRoot(t, "pbs")
	helper, _ := fakeHelper(t)
	mustStmt(t, "ALTER DEFAULTS SET secret_helper = "+helper)
	out := mustStmt(t, "CREATE ACCOUNT edge SET provider = gateway, base_url = https://router.example.com/v1, auth = none, header.CF-Access-Client-Id FROM keychain:ok/id, header.CF-Access-Client-Secret FROM keychain:ok/secret")
	if !strings.Contains(out, "header.CF-Access-Client-Id  <from keychain:ok/id>") {
		t.Fatalf("CREATE output:\n%s", out)
	}
	var v struct {
		Headers []string `json:"headers"`
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW ACCOUNT edge --json")), &v); err != nil {
		t.Fatal(err)
	}
	if strings.Join(v.Headers, ",") != "CF-Access-Client-Id,CF-Access-Client-Secret" {
		t.Fatalf("SHOW --json headers: %v", v.Headers)
	}
	if sc := mustStmt(t, "SHOW CREATE ACCOUNT edge"); !strings.Contains(sc, "header.CF-Access-Client-Secret FROM 'keychain:ok/secret'") {
		t.Fatalf("SHOW CREATE:\n%s", sc)
	}

	for stmtText, want := range map[string]string{
		"ALTER ACCOUNT edge SET header.X-Team = blue":                                               "takes a secret reference",
		"ALTER ACCOUNT edge SET header.Authorization FROM keychain:ok/a":                            "the account's own",
		"ALTER ACCOUNT edge SET header.anthropic-beta FROM keychain:ok/b":                           "Anthropic's API",
		"ALTER ACCOUNT edge SET header.Host FROM keychain:ok/h":                                     "frames the request",
		"ALTER ACCOUNT edge SET header.bad_name FROM keychain:ok/n":                                 "letters, digits and dashes",
		"ALTER ACCOUNT edge SET header.X-Team FROM not-a-ref":                                       "not a secret reference",
		"CREATE ACCOUNT aws SET provider = bedrock, auth = cloud, header.X-Team FROM keychain:ok/t": "carries no headers",
	} {
		if _, err := stmt(t, stmtText); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", stmtText, err, want)
		}
	}

	// One header per name, in any case.
	mustStmt(t, "ALTER ACCOUNT edge SET header.cf-access-client-secret FROM keychain:ok/secret2")
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW ACCOUNT edge --json")), &v); err != nil {
		t.Fatal(err)
	}
	if strings.Join(v.Headers, ",") != "CF-Access-Client-Id,cf-access-client-secret" {
		t.Fatalf("a header set again in another case: %v", v.Headers)
	}
	if out := mustStmt(t, "ALTER ACCOUNT edge RESET header.CF-ACCESS-CLIENT-SECRET"); !strings.Contains(out, "(reset)") {
		t.Fatalf("RESET header:\n%s", out)
	}

	// An account that moves keeps only the headers its statement states.
	mustStmt(t, "ALTER ACCOUNT edge SET header.X-Team FROM keychain:ok/team")
	out = mustStmt(t, "ALTER ACCOUNT edge SET base_url = https://other.example.com/v1, auth = none, header.X-Team FROM keychain:ok/team")
	if !strings.Contains(out, "header.CF-Access-Client-Id  (removed: the account moved") {
		t.Fatalf("move output:\n%s", out)
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW ACCOUNT edge --json")), &v); err != nil {
		t.Fatal(err)
	}
	if strings.Join(v.Headers, ",") != "X-Team" {
		t.Fatalf("headers after the move: %v", v.Headers)
	}

	too := "CREATE ACCOUNT many SET provider = gateway, base_url = https://router.example.com/v1, auth = none"
	for i := 0; i < 9; i++ {
		too += ", header.X-H" + string(rune('a'+i)) + " FROM keychain:ok/h"
	}
	if _, err := stmt(t, too); err == nil || !strings.Contains(err.Error(), "at most 8 headers") {
		t.Fatalf("nine headers: %v", err)
	}
}

// At launch the helper resolves each header into a variable of its own and
// cpb's header step joins them into ANTHROPIC_CUSTOM_HEADERS for claude
// only: no value on an argument list, no header variable left over, and
// nothing from the shell standing in.
func TestAccountHeadersLaunch(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	writePlaybook(t, root, "edgepb", &manifest.Manifest{IsolatedLogin: true})
	helper, helperLog := fakeHelper(t)
	claudeLog := stubClaude(t)
	mustStmt(t, "ALTER DEFAULTS SET secret_helper = "+helper)
	mustStmt(t, "CREATE ACCOUNT edge SET provider = gateway, base_url = https://router.example.com/v1, auth_token FROM keychain:ok/key, header.CF-Access-Client-Secret FROM keychain:ok/secret, header.CF-Access-Client-Id FROM keychain:ok/id")
	mustStmt(t, "ALTER PLAYBOOK edgepb USE ACCOUNT edge")
	t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "X-Shell: stale")
	t.Setenv("CPB_ACCOUNT_HEADER_7", "stale")

	if ex := mustStmt(t, "EXPLAIN PLAYBOOK edgepb"); !strings.Contains(ex, "headers CF-Access-Client-Id, CF-Access-Client-Secret from references") {
		t.Fatalf("EXPLAIN:\n%s", ex)
	}
	if err := runRun(nil, []string{"edgepb", "--version"}); err != nil {
		t.Fatal(err)
	}
	got := readLog(t, claudeLog)
	want := "ANTHROPIC_CUSTOM_HEADERS=CF-Access-Client-Id: resolved:keychain:ok/id\nCF-Access-Client-Secret: resolved:keychain:ok/secret\n"
	if !strings.Contains(got, want) || !strings.Contains(got, "ARGS --version") || !strings.Contains(got, "ANTHROPIC_AUTH_TOKEN=resolved:keychain:ok/key") {
		t.Fatalf("claude's environment:\n%s", got)
	}
	if strings.Contains(got, "CPB_ACCOUNT_HEADER_") || strings.Contains(got, "X-Shell") {
		t.Fatalf("a header variable or the shell's headers reached claude:\n%s", got)
	}
	h := readLog(t, helperLog)
	if !strings.Contains(h, "CPB_ACCOUNT_HEADER_0=keychain:ok/id CPB_ACCOUNT_HEADER_1=keychain:ok/secret -- ") ||
		!strings.Contains(h, " __account-headers CF-Access-Client-Id CF-Access-Client-Secret -- ") || strings.Contains(h, "resolved:") {
		t.Fatalf("helper argv:\n%s", h)
	}
}

func TestJoinHeaders(t *testing.T) {
	env := map[string]string{"CPB_ACCOUNT_HEADER_0": "id-1", "CPB_ACCOUNT_HEADER_1": "s3cret"}
	get := func(k string) string { return env[k] }
	got, err := joinHeaders([]string{"X-Id", "X-Secret"}, get)
	if err != nil || got != "X-Id: id-1\nX-Secret: s3cret" {
		t.Fatalf("join: %q %v", got, err)
	}
	if _, err := joinHeaders([]string{"X-Id", "X-Secret", "X-Missing"}, get); err == nil || !strings.Contains(err.Error(), "X-Missing: the secret helper set no value") {
		t.Fatalf("missing: %v", err)
	}
	env["CPB_ACCOUNT_HEADER_1"] = "s3cret\nX-Injected: yes"
	_, err = joinHeaders([]string{"X-Id", "X-Secret"}, get)
	if err == nil || !strings.Contains(err.Error(), "line break") || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("a value with a line break: %v", err)
	}
	if err := runHeaderStep([]string{"X-Id"}); err == nil {
		t.Fatal("the header step without -- ran")
	}
	os.Unsetenv("CPB_ACCOUNT_HEADER_0")
}

// A layer that still sets ANTHROPIC_CUSTOM_HEADERS is refused, pointing at
// the account's header key.
func TestCustomHeadersLayerPointsAtTheAccount(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	writePlaybook(t, root, "old", &manifest.Manifest{IsolatedLogin: true, Env: &manifest.Env{Set: map[string]string{"ANTHROPIC_CUSTOM_HEADERS": "X-Team: blue"}}})
	stubClaude(t)
	err := runRun(nil, []string{"old", "--version"})
	if err == nil || !strings.Contains(err.Error(), "SET header.<Name> FROM '<ref>'") {
		t.Fatalf("a layer's custom headers: %v", err)
	}
}

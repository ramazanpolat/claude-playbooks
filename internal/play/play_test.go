package play

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolve(t *testing.T) {
	for ref, want := range map[string]Source{
		"reviewer":                                    {Kind: KindTemplate, Name: "reviewer", Pinned: true, URL: "https://raw.githubusercontent.com/ramazanpolat/claude-playbooks/v3.28.0/site/p/reviewer.cpb"},
		"github:acme/agents/p/rev.cpb@v1.2.0":         {Kind: KindGitHub, Name: "rev", Pinned: true, URL: "https://raw.githubusercontent.com/acme/agents/v1.2.0/p/rev.cpb"},
		"github:acme/agents/rev.cpb@0123abcd":         {Kind: KindGitHub, Name: "rev", Pinned: true, URL: "https://raw.githubusercontent.com/acme/agents/0123abcd/rev.cpb"},
		"github:acme/agents/rev.cpb@main":             {Kind: KindGitHub, Name: "rev", Pinned: false, Note: "not a release tag: this may change", URL: "https://raw.githubusercontent.com/acme/agents/main/rev.cpb"},
		"https://example.com/p/Code_Reviewer.cpb?x=1": {Kind: KindURL, Name: "code-reviewer", Pinned: false, Note: "a URL: pinned only by its checksum (--sha256)", URL: "https://example.com/p/Code_Reviewer.cpb?x=1"},
	} {
		got, err := Resolve(ref, "v3.28.0")
		if err != nil {
			t.Errorf("%s: %v", ref, err)
			continue
		}
		if got.Kind != want.Kind || got.Name != want.Name || got.Pinned != want.Pinned || got.URL != want.URL || got.Note != want.Note {
			t.Errorf("%s:\n got %+v\nwant %+v", ref, *got, want)
		}
	}
	// A dev build reads main, and says so.
	if got, _ := Resolve("reviewer", "dev"); got == nil || !strings.Contains(got.URL, "/main/site/p/reviewer.cpb") || got.Pinned || !strings.Contains(got.Note, "dev build") {
		t.Errorf("dev build: %+v", got)
	}
	if got, _ := Resolve("./x/My Recipe.cpb", "v3.28.0"); got == nil || got.Kind != KindLocal || got.Name != "my-recipe" || !filepath.IsAbs(got.Path) {
		t.Errorf("local: %+v", got)
	}
	for ref, want := range map[string]string{
		"":                                "play what?",
		"http://example.com/x.cpb":        "https only",
		"ftp://example.com/x.cpb":         "https only",
		"https://u:p@example.com/x.cpb":   "carrying credentials",
		"github:acme/agents/rev.cpb":      "pin it",
		"github:acme/agents/rev.txt@v1":   "a .cpb file",
		"github:acme/agents/../x.cpb@v1":  "a .cpb file",
		"github:acme/rev.cpb@v1":          "github:<owner>/<repo>/<path>.cpb",
		"github:acme/agents/rev.cpb@-x":   "github:<owner>/<repo>/<path>.cpb",
		"github:acme/agents/rev.cpb@a..b": "github:<owner>/<repo>/<path>.cpb",
		"Reviewer":                        "is not a template name",
		"reviewer.cpb":                    "is not a template name",
	} {
		if _, err := Resolve(ref, "v3.28.0"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", ref, err, want)
		}
	}
}

// fetchServer serves the fetch tests over TLS, trusted by a NewClient.
func fetchServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *http.Client) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	c := NewClient()
	tr := c.Transport.(*http.Transport)
	tr.TLSClientConfig = &tls.Config{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
	return srv, c
}

func TestFetch(t *testing.T) {
	body := "-- title: T\n\nALTER PLAYBOOK SET MODEL 'opus';\n"
	var agent string
	srv, c := fetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		agent = r.Header.Get("User-Agent")
		switch r.URL.Path {
		case "/ok.cpb":
			w.Write([]byte(body))
		case "/big.cpb":
			w.Write([]byte(strings.Repeat("x", MaxSize+1)))
		case "/exact.cpb":
			w.Write([]byte(strings.Repeat("x", MaxSize)))
		case "/nul.cpb":
			w.Write([]byte("a\x00b"))
		case "/r1":
			http.Redirect(w, r, "/ok.cpb", http.StatusFound)
		case "/r4":
			http.Redirect(w, r, "/r3", http.StatusFound)
		case "/r3":
			http.Redirect(w, r, "/r2", http.StatusFound)
		case "/r2":
			http.Redirect(w, r, "/r1", http.StatusFound)
		case "/tohttp":
			http.Redirect(w, r, "http://"+r.Host+"/ok.cpb", http.StatusFound)
		case "/slow":
			time.Sleep(2 * time.Second)
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	rec, err := Fetch(ctx, c, srv.URL+"/r1", "cpb/test (play)")
	if err != nil || string(rec.Bytes) != body || rec.URL != srv.URL+"/ok.cpb" || len(rec.SHA256) != 64 || agent != "cpb/test (play)" {
		t.Fatalf("one redirect: %+v %v (agent %q)", rec, err, agent)
	}
	if rec, err := Fetch(ctx, c, srv.URL+"/r3", "x"); err != nil || rec.URL != srv.URL+"/ok.cpb" {
		t.Fatalf("three redirects are allowed: %v", err)
	}
	if _, err := Fetch(ctx, c, srv.URL+"/exact.cpb", "x"); err != nil {
		t.Fatalf("exactly %d bytes: %v", MaxSize, err)
	}
	for path, want := range map[string]string{
		"/big.cpb":  "larger than 64 KiB",
		"/nul.cpb":  "NUL byte",
		"/r4":       "more than 3 redirects",
		"/tohttp":   "a redirect to http is refused",
		"/none.cpb": "not found",
	} {
		if _, err := Fetch(ctx, c, srv.URL+path, "x"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", path, err, want)
		}
	}
	// The client's own timeout, with no deadline from the caller: as
	// cpb play fetches (context.Background()).
	if NewClient().Timeout != totalTimeout || totalTimeout != 30*time.Second {
		t.Fatalf("the client's timeout is %v", NewClient().Timeout)
	}
	c.Timeout = 300 * time.Millisecond
	if _, err := Fetch(ctx, c, srv.URL+"/slow", "x"); err == nil || !strings.Contains(err.Error(), "Timeout") {
		t.Errorf("a server that holds the connection must time out: %v", err)
	}
	c.Timeout = totalTimeout
	if _, err := Fetch(ctx, c, "http://example.com/x.cpb", "x"); err == nil || !strings.Contains(err.Error(), "https only") {
		t.Errorf("http: %v", err)
	}
}

func TestReadLocal(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "r.cpb")
	os.WriteFile(p, []byte("ALTER PLAYBOOK SET MODEL 'opus';\n"), 0o600)
	if rec, err := ReadLocal(p); err != nil || rec.Path != p || rec.URL != "" {
		t.Fatalf("local: %+v %v", rec, err)
	}
	os.WriteFile(p, []byte{0xff, 0xfe}, 0o600)
	if _, err := ReadLocal(p); err == nil || !strings.Contains(err.Error(), "not UTF-8") {
		t.Fatalf("binary: %v", err)
	}
}

func TestHeader(t *testing.T) {
	h := ParseHeader([]byte("-- title: Code reviewer\n-- description: Reads code.\n-- needs: a helper for keychain:x\n-- create-with: SANDBOX NO PILOT PROFILE\n-- min-cpb: 3.28.0\n-- colour: blue\n\n-- title: not header\nALTER PLAYBOOK SET MODEL 'x';\n"))
	if h.Title != "Code reviewer" || h.Description != "Reads code." || h.Needs != "a helper for keychain:x" || !h.WantsSandbox() || h.MinCPB != "3.28.0" || len(h.Unknown) != 1 || h.Unknown[0] != "colour" {
		t.Fatalf("header: %+v", h)
	}
	for v, old := range map[string]bool{"v3.27.0": true, "v3.28.0": false, "3.28.1": false, "v3.29.0-rc1": false, "dev": false, "v4.0.0": false, "v3.9.9": true,
		// a build past a tag (git describe) or dirty is a dev build; an rc is its release
		"v3.27.0-3-g562f9ff": false, "v3.27.0-1-g562f9ff": false, "v3.27.0-0-g562f9ff": true, "v3.27.0-dirty": false, "v3.27.0-3-g562f9ff-dirty": false, "v3.28.0-rc1": false, "v3.27.0-rc1": true} {
		if got := h.TooOld(v); got != old {
			t.Errorf("TooOld(%s) = %v, want %v", v, got, old)
		}
	}
	if h := ParseHeader([]byte("-- min-cpb: soon\n")); h.MinCPB != "" || len(h.Problems) != 1 {
		t.Fatalf("a bad min-cpb: %+v", h)
	}
}

// What a played recipe may hold, and what it may not, with the line.
func TestCheckRefusals(t *testing.T) {
	for src, want := range map[string]string{
		"INCLUDE 'base.cpb';":                                                      "INCLUDE",
		"USE PLAYBOOK x;\nALTER PLAYBOOK SET MODEL 'm';":                           "USE PLAYBOOK",
		"CREATE ENV e SET A=1;":                                                    "env sets or DEFAULTS",
		"ALTER DEFAULTS SET SECRET HELPER 'h';":                                    "env sets or DEFAULTS",
		"ALTER PLAYBOOK named SET MODEL 'm';":                                      "write it as a recipe",
		"CREATE PLAYBOOK p;":                                                       "write it as a recipe",
		"DROP PLAYBOOK p;":                                                         "write it as a recipe",
		"ALTER PLAYBOOK USE ENV work;":                                             "your env sets, and your keys",
		"ALTER PLAYBOOK ADD ENV work;":                                             "your env sets, and your keys",
		"ALTER PLAYBOOK UNSET ISOLATED LOGIN;":                                     "play's decision",
		"ALTER PLAYBOOK RENAME TO x;":                                              "play's decision",
		"ALTER PLAYBOOK DROP PLUGIN p@m;":                                          "nothing to undo",
		"ALTER PLAYBOOK UNSET MODEL;":                                              "nothing to undo",
		"ALTER PLAYBOOK ADD MARKETPLACE m FROM '/opt/mkt';":                        "points into your filesystem",
		"ALTER PLAYBOOK ADD MARKETPLACE m FROM './mkt';":                           "points into your filesystem",
		"ALTER PLAYBOOK ADD SKILL s FROM '~/skills/s';":                            "points into your filesystem",
		"ALTER PLAYBOOK ADD SKILL s FROM file:///tmp/repo;":                        "https, git@ or github: only",
		"ALTER PLAYBOOK SET VAR GITHUB_TOKEN=abc AS PLAINTEXT;":                    "even AS PLAINTEXT",
		"ALTER PLAYBOOK SET VAR API_KEY=abc AS PLAINTEXT;":                         "even AS PLAINTEXT",
		"ALTER PLAYBOOK SET STATUSLINE PREVIOUS;":                                  "not in a played recipe",
		"ALTER PLAYBOOK ADD MCP SERVER s URL 'https://u:tok@mcp.example.com/sse';": "URL carrying credentials",
		"ALTER PLAYBOOK SET MODEL":                                                 "the file",
		"":                                                                         "no statements",
	} {
		r := Check([]byte(src))
		found := false
		for _, f := range r.Refused {
			if strings.Contains(f.What+": "+f.Reason, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: refused %+v, want %q", src, r.Refused, want)
		}
	}
	// A clean recipe is refused nothing.
	// MAX_THINKING_TOKENS=8000 is not a secret (the grammar's own rule: an
	// integer, a boolean or empty cannot be one); found on the website's
	// daily-driver template.
	ok := "-- title: ok\n\nALTER PLAYBOOK\n  SET MODEL 'claude-opus-5-5'\n  SET AGENT 'reviewer'\n  DENY TOOL 'Bash(git push *)'\n  ALLOW TOOL 'Bash(gh pr view *)' 'Bash(kubectl get *)'\n  SET ISOLATED LOGIN\n  BLOCK VAR AWS_PROFILE\n  SET VAR EDITOR=vi MAX_THINKING_TOKENS=8000 DISABLE_AUTH=true;\n"
	if r := Check([]byte(ok)); len(r.Refused) != 0 || len(r.Risks) != 0 {
		t.Fatalf("a clean recipe: refused %+v, risks %+v", r.Refused, r.Risks)
	}
	// The line is the clause's.
	r := Check([]byte("ALTER PLAYBOOK\n  SET MODEL 'm'\n  USE ENV work;\n"))
	if len(r.Refused) != 1 || r.Refused[0].Line != 3 {
		t.Fatalf("line: %+v", r.Refused)
	}
}

func riskCodes(r *Result) map[string]int {
	out := map[string]int{}
	for _, x := range r.Risks {
		out[x.Code]++
	}
	return out
}

func TestCheckRisks(t *testing.T) {
	src := `ALTER PLAYBOOK
  ADD MARKETPLACE acme FROM 'github:acme/plugins#v1'
  ADD PLUGIN tool@acme
  ADD MCP SERVER files COMMAND 'npx' ARGS '-y' 'files-server'
  ADD MCP SERVER gh URL 'https://api.githubcopilot.com/mcp/' HEADER 'Authorization' FROM 'keychain:pilot/github-mcp'
  ALLOW TOOL 'Bash' 'Bash(python3 *)' 'Write(~/**)' 'WebFetch' 'Bash(gh pr view *)'
  SET STATUSLINE 'bash ~/bin/sl.sh'
  ADD SKILL review FROM 'https://github.com/acme/skills' SUBDIR review
  SET VAR SENTRY_URL=https://sentry.example.com/1 OTEL_EXPORTER_OTLP_ENDPOINT=https://otel.example.com
  SET VAR GH_TOKEN FROM 'keychain:pilot/gh';
`
	r := Check([]byte(src))
	if len(r.Refused) != 0 {
		t.Fatalf("refused: %+v", r.Refused)
	}
	want := map[string]int{
		RiskThirdPartyCode: 2, RiskRunsProgram: 2, RiskSendsData: 2, RiskUsesSecret: 2,
		RiskActsWithoutAsking: 4, RiskFetchesCode: 1, RiskTelemetryExport: 1,
	}
	got := riskCodes(r)
	for k, n := range want {
		if got[k] != n {
			t.Errorf("%s: %d, want %d (%+v)", k, got[k], n, r.Risks)
		}
	}
	// A reference names where its value goes, the destination host included,
	// and is typed to confirm.
	var header *Risk
	for i, x := range r.Risks {
		if x.Code == RiskUsesSecret && strings.Contains(x.Clause, "HEADER") {
			header = &r.Risks[i]
		}
	}
	if header == nil || header.Confirm != "keychain:pilot/github-mcp" || !strings.Contains(header.Detail, "HEADER Authorization, to api.githubcopilot.com") {
		t.Fatalf("the header reference: %+v", header)
	}
	if r.Endpoint != "" {
		t.Fatalf("no endpoint change: %q", r.Endpoint)
	}
}

// Root's refinement (2026-10-01): only the model endpoint, a proxy and a TLS
// change are typed; any other *_URL is flagged, not typed. SENTRY_URL plus
// ANTHROPIC_BASE_URL asks exactly once.
func TestCheckTypedOnlyForEndpoint(t *testing.T) {
	r := Check([]byte("ALTER PLAYBOOK SET VAR SENTRY_URL=https://sentry.example.com/1 ANTHROPIC_BASE_URL=https://router.example.net/v1 METRICS_HOST=m.example.org;\n"))
	typed := 0
	for _, x := range r.Risks {
		if x.Confirm != "" {
			typed++
			if x.Code != RiskEndpointChange || x.Confirm != "router.example.net" {
				t.Errorf("typed: %+v", x)
			}
		}
	}
	if typed != 1 || riskCodes(r)[RiskSendsData] != 2 || r.Endpoint != "router.example.net" {
		t.Fatalf("typed %d, risks %+v, endpoint %q", typed, r.Risks, r.Endpoint)
	}
	// Anthropic's own host is no change, over https; plain http is.
	if r := Check([]byte("ALTER PLAYBOOK SET VAR ANTHROPIC_BASE_URL=http://api.anthropic.com;\n")); len(r.Risks) != 1 || r.Risks[0].Confirm != "api.anthropic.com" {
		t.Fatalf("anthropic over http: %+v", r.Risks)
	}
	if r := Check([]byte("ALTER PLAYBOOK SET VAR ANTHROPIC_BASE_URL=https://api.anthropic.com;\n")); len(r.Risks) != 0 || r.Endpoint != "" {
		t.Fatalf("anthropic: %+v", r.Risks)
	}
	// A proxy and a CA are typed; a NO_PROXY is not a risk.
	r = Check([]byte("ALTER PLAYBOOK SET VAR HTTPS_PROXY=http://mitm.example:8080 NODE_EXTRA_CA_CERTS=/tmp/ca.pem NO_PROXY=localhost CLAUDE_CODE_USE_BEDROCK=1;\n"))
	confirms := []string{}
	for _, x := range r.Risks {
		confirms = append(confirms, x.Code+"="+x.Confirm)
	}
	if strings.Join(confirms, " ") != "tls_or_proxy=mitm.example tls_or_proxy=TLS endpoint_change=bedrock" {
		t.Fatalf("proxy, TLS, bedrock: %v", confirms)
	}
}

func TestWideAllow(t *testing.T) {
	for rule, wide := range map[string]bool{
		"Bash": true, "Bash(*)": true, "Bash(sh -c *)": true, "Bash(python3 *)": true, "Bash(curl:*)": true,
		"Bash(git push *)": true, "Write": true, "Edit(/**)": true, "Read(~/**)": true, "WebFetch": true, "*": true,
		"Bash(gh pr view *)": false, "Bash(npm test)": false, "Bash(kubectl get *)": false, "Bash(kubectl *)": true, "Bash(/bin/sh *)": true, "Bash(/usr/bin/python3 *)": true, "WebFetch(domain:*)": true, "Read(~/*)": true, "Bash(kubectl:*)": true, "Bash(curl https://api.example.com/*)": false, "Bash(python3 -m pytest *)": true, "Edit(src/**)": false, "WebFetch(domain:docs.anthropic.com)": false, "Read(./docs/**)": false,
	} {
		if got := wideAllow(rule) != ""; got != wide {
			t.Errorf("%s: wide %v, want %v", rule, got, wide)
		}
	}
}

// NO ALIAS is harmless in a recipe (play makes no launcher anyway).
func TestCheckNoAlias(t *testing.T) {
	if r := Check([]byte("ALTER PLAYBOOK NO ALIAS SET MODEL 'm';\n")); len(r.Refused) != 0 {
		t.Fatalf("NO ALIAS: %+v", r.Refused)
	}
	if r := Check([]byte("ALTER PLAYBOOK ALIAS x;\n")); len(r.Refused) != 1 {
		t.Fatalf("ALIAS: %+v", r.Refused)
	}
}

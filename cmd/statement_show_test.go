package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

const (
	showSecret    = "sk-abcdef0123456789xyz"
	showURLSecret = "hunter2pass"
)

// seedShowFixture builds one playbook with every kind of variable: a
// literal, a credential stored AS PLAINTEXT, a URL carrying a password, a
// blocked key, an attached env set and a default.
func seedShowFixture(t *testing.T) {
	t.Helper()
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "router")
	if err := manifest.Write(root, &manifest.Manifest{Name: "router", Version: "1.2.3", Alias: "rt",
		Source: &manifest.Source{Repository: "https://example.com/r.git", Branch: "v1"}}); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "CREATE ENV base SET FROM_BASE=1 MODEL=base")
	mustStmt(t, "CREATE ENV glm SET MODEL=glm DESCRIBE router")
	mustStmt(t, "ALTER DEFAULTS USE ENV base")
	mustStmt(t, "ALTER PLAYBOOK router USE ENV glm BLOCK VAR HTTP_PROXY")
	mustStmt(t, "ALTER PLAYBOOK router SET VAR API_KEY="+showSecret+" PROXY_URL=https://u:"+showURLSecret+"@proxy AS PLAINTEXT")
	mustStmt(t, "ALTER PLAYBOOK router SET VAR MAX_THINKING_TOKENS=8000")
}

func noSecret(t *testing.T, what, out string) {
	t.Helper()
	for _, s := range []string{showSecret, showURLSecret} {
		if strings.Contains(out, s) {
			t.Fatalf("%s printed a secret:\n%s", what, out)
		}
	}
}

func TestShowPlaybookJSON(t *testing.T) {
	seedShowFixture(t)
	out := mustStmt(t, "SHOW PLAYBOOK router --json")
	noSecret(t, "SHOW PLAYBOOK --json", out)
	var v struct {
		Name     string  `json:"name"`
		Version  *string `json:"version"`
		Launcher *string `json:"launcher"`
		Linked   *string `json:"linked"`
		Source   *struct {
			URL    string  `json:"url"`
			Branch *string `json:"branch"`
			Subdir *string `json:"subdir"`
		} `json:"source"`
		Envs []string         `json:"envs"`
		Vars []map[string]any `json:"vars"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if v.Name != "router" || *v.Version != "1.2.3" || *v.Launcher != "rt" || v.Linked != nil ||
		v.Source.URL != "https://example.com/r.git" || *v.Source.Branch != "v1" || v.Source.Subdir != nil ||
		strings.Join(v.Envs, ",") != "glm" {
		t.Fatalf("fields: %+v", v)
	}
	byKey := map[string]map[string]any{}
	for _, x := range v.Vars {
		byKey[x["key"].(string)] = x
	}
	if byKey["MAX_THINKING_TOKENS"]["value"] != "8000" {
		t.Errorf("literal: %v", byKey["MAX_THINKING_TOKENS"])
	}
	for _, k := range []string{"API_KEY", "PROXY_URL"} {
		x := byKey[k]
		if x["redacted"] != true || x["plaintext"] != true || x["value"] != nil {
			t.Errorf("%s must be redacted with no value: %v", k, x)
		}
	}
	if byKey["HTTP_PROXY"]["blocked"] != true {
		t.Errorf("blocked: %v", byKey["HTTP_PROXY"])
	}
}

func TestShowPlaybookHuman(t *testing.T) {
	seedShowFixture(t)
	out := mustStmt(t, "SHOW PLAYBOOK router")
	noSecret(t, "SHOW PLAYBOOK", out)
	for _, want := range []string{
		"Version:    1.2.3\n", // scripts matched `Version:` in `info`; the label stays
		"Launcher:   rt\n",
		"Source:     https://example.com/r.git (branch v1)\n",
		"Env sets:   glm\n",
		"HTTP_PROXY (blocked)",
		"MAX_THINKING_TOKENS=8000",
		"chars, plaintext)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestShowPlaybooks(t *testing.T) {
	seedShowFixture(t)
	seedFlatPlaybook(t, "bare")
	var all []map[string]any
	out := mustStmt(t, "show playbooks --json")
	noSecret(t, "SHOW PLAYBOOKS --json", out)
	if err := json.Unmarshal([]byte(out), &all); err != nil || len(all) != 2 || all[0]["name"] != "bare" || all[1]["name"] != "router" {
		t.Fatalf("%v\n%s", err, out)
	}
	if all[0]["version"] != nil || all[0]["source"] != nil || all[0]["launcher"] != nil {
		t.Errorf("a bare playbook has null version, source and launcher: %v", all[0])
	}
	human := mustStmt(t, "SHOW PLAYBOOKS")
	if !strings.Contains(human, "NAME") || !strings.Contains(human, "router") || !strings.Contains(human, "glm") {
		t.Fatalf("human form:\n%s", human)
	}
	if bare := mustStmt(t, "SHOW"); bare != human {
		t.Fatalf("a bare SHOW must print SHOW PLAYBOOKS:\n%s\n---\n%s", bare, human)
	}
	if bare := mustStmt(t, "show --json"); bare != out {
		t.Fatalf("SHOW --json must print SHOW PLAYBOOKS --json")
	}
	if _, err := stmt(t, "SHOW FOO"); err == nil {
		t.Fatal("SHOW followed by a non-object must stay an error")
	}
}

func TestShowEnvsAndDefaults(t *testing.T) {
	seedShowFixture(t)
	var envs []map[string]any
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW ENVS --json")), &envs); err != nil || len(envs) != 2 {
		t.Fatalf("SHOW ENVS --json: %v %v", envs, err)
	}
	if envs[0]["name"] != "base" || envs[0]["default"] != true || envs[1]["default"] != false {
		t.Errorf("defaults flag: %v", envs)
	}
	if used := envs[1]["used_by"].([]any); len(used) != 1 || used[0] != "router" {
		t.Errorf("used_by: %v", envs[1])
	}
	list := mustStmt(t, "SHOW ENVS")
	if !strings.Contains(list, "base *") || strings.Contains(list, "glm *") {
		t.Errorf("SHOW ENVS marks the defaults:\n%s", list)
	}
	one := mustStmt(t, "SHOW ENV glm")
	for _, want := range []string{"Description:  router", "Used by:      router", "Default:      no", "MODEL=glm"} {
		if !strings.Contains(one, want) {
			t.Errorf("SHOW ENV missing %q:\n%s", want, one)
		}
	}
	var d struct {
		Envs         []string `json:"envs"`
		SecretHelper any      `json:"secret_helper"`
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW DEFAULTS --json")), &d); err != nil || strings.Join(d.Envs, ",") != "base" || d.SecretHelper != nil {
		t.Fatalf("SHOW DEFAULTS --json: %+v %v", d, err)
	}
	if _, err := stmt(t, "SHOW ENV ghost"); err == nil {
		t.Error("SHOW ENV of a missing set succeeded")
	}
}

func TestExplainPlaybook(t *testing.T) {
	seedShowFixture(t)
	var v struct {
		Playbook string `json:"playbook"`
		Vars     []struct {
			Key      string  `json:"key"`
			Value    *string `json:"value"`
			Redacted bool    `json:"redacted"`
			Blocked  bool    `json:"blocked"`
			Layer    struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"layer"`
		} `json:"vars"`
	}
	out := mustStmt(t, "EXPLAIN PLAYBOOK router --json")
	noSecret(t, "EXPLAIN --json", out)
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	got := map[string]string{}
	for _, x := range v.Vars {
		val := "?"
		switch {
		case x.Blocked:
			val = "(blocked)"
		case x.Redacted:
			val = "(redacted)"
		case x.Value != nil:
			val = *x.Value
		}
		got[x.Key] = val + " <- " + strings.TrimSpace(x.Layer.Kind+" "+x.Layer.Name)
	}
	for k, want := range map[string]string{
		"FROM_BASE":           "1 <- DEFAULTS base",
		"MODEL":               "glm <- ENV glm",
		"HTTP_PROXY":          "(blocked) <- PLAYBOOK",
		"API_KEY":             "(redacted) <- PLAYBOOK",
		"MAX_THINKING_TOKENS": "8000 <- PLAYBOOK",
	} {
		if got[k] != want {
			t.Errorf("%s: got %q, want %q", k, got[k], want)
		}
	}
	human := mustStmt(t, "EXPLAIN PLAYBOOK router")
	noSecret(t, "EXPLAIN", human)
	for _, want := range []string{"DEFAULTS (ENV base)", "ENV glm", "PLAYBOOK router", "(blocked)", "chars, plaintext)"} {
		if !strings.Contains(human, want) {
			t.Errorf("EXPLAIN missing %q:\n%s", want, human)
		}
	}

	// A launch that would be refused is explained as refused.
	mustStmt(t, "ALTER DEFAULTS DROP ENV base")
	if err := writeBroken(t, "glm"); err != nil {
		t.Fatal(err)
	}
	if _, err := stmt(t, "EXPLAIN PLAYBOOK router"); err == nil || !strings.Contains(err.Error(), "would be refused") {
		t.Fatalf("EXPLAIN over a broken env set: %v", err)
	}
}

func writeBroken(t *testing.T, name string) error {
	t.Helper()
	return os.WriteFile(filepath.Join(envprofile.Dir(config.ResolvePlaybooksDir()), name+".toml"), []byte("= [\n"), 0o600)
}

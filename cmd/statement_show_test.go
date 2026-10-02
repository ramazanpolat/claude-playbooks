package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
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
	if err := manifest.Write(root, &manifest.Manifest{Name: "router", Version: "1.2.3", Launcher: "rt",
		Source: &manifest.Source{Repository: "https://example.com/r.git", Branch: "v1"}}); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "CREATE ENV base SET FROM_BASE=1 MODEL=base")
	mustStmt(t, "CREATE ENV glm SET MODEL=glm DESCRIPTION router")
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
	// The labels are aligned to the widest one present, so only the label
	// and its value are pinned: scripts matched `Version:` in `info`, and the
	// label stays.
	for _, re := range []string{
		`(?m)^Version: +1\.2\.3$`,
		`(?m)^Launcher: +rt$`,
		`(?m)^Source: +https://example\.com/r\.git \(branch v1\)$`,
		`(?m)^Env sets: +glm$`,
	} {
		if !regexp.MustCompile(re).MatchString(out) {
			t.Errorf("missing %s in:\n%s", re, out)
		}
	}
	for _, want := range []string{
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
		"FROM_BASE":           "1 <- defaults base",
		"MODEL":               "glm <- env glm",
		"HTTP_PROXY":          "(blocked) <- playbook",
		"API_KEY":             "(redacted) <- playbook",
		"MAX_THINKING_TOKENS": "8000 <- playbook",
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
	return os.WriteFile(filepath.Join(envset.Dir(config.ResolvePlaybooksDir()), name+".toml"), []byte("= [\n"), 0o600)
}

// explainKeys is the sorted keys EXPLAIN PLAYBOOK says a launch of name gets.
func explainKeys(t *testing.T, name string) []string {
	t.Helper()
	var v explainJSON
	if err := json.Unmarshal([]byte(mustStmt(t, "EXPLAIN PLAYBOOK "+name+" --json")), &v); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, x := range v.Vars {
		keys = append(keys, x.Key)
	}
	sort.Strings(keys)
	return keys
}

// A legacy `subdir` playbook whose subdirectory carries its own manifest is
// governed by that manifest at launch (manifest.NearestPath): EXPLAIN shows
// that block under the registry default, never the root block the launch
// ignores.
func TestExplainFollowsNearestManifestForSubdir(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "legacy")
	if err := os.MkdirAll(filepath.Join(root, "cfg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, manifest.FileName), []byte("name = \"legacy\"\nsubdir = \"cfg\"\n\n[env.set]\nROOT = \"1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cfg", manifest.FileName), []byte("name = \"legacy\"\n\n[env.set]\nNESTED = \"1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "CREATE ENV base SET FROM_DEFAULT=yes")
	mustStmt(t, "ALTER DEFAULTS USE ENV base")
	if got := explainKeys(t, "legacy"); !reflect.DeepEqual(got, []string{"FROM_DEFAULT", "NESTED"}) {
		t.Fatalf("EXPLAIN with a nested manifest: %v", got)
	}
	// Without the nested manifest, the root block governs.
	if err := os.Remove(filepath.Join(root, "cfg", manifest.FileName)); err != nil {
		t.Fatal(err)
	}
	if got := explainKeys(t, "legacy"); !reflect.DeepEqual(got, []string{"FROM_DEFAULT", "ROOT"}) {
		t.Fatalf("EXPLAIN without a nested manifest: %v", got)
	}
}

// A manifest-free playbook is governed at launch by the nearest ancestor
// manifest, when one exists; once it has its own, that one governs.
func TestExplainFollowsAncestorManifestForManifestFreePlaybook(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "bare")
	root := config.ResolvePlaybooksDir()
	if err := os.WriteFile(filepath.Join(root, manifest.FileName), []byte("name = \"root\"\n\n[env.set]\nANCESTOR = \"1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := explainKeys(t, "bare"); !reflect.DeepEqual(got, []string{"ANCESTOR"}) {
		t.Fatalf("EXPLAIN of a manifest-free playbook: %v", got)
	}
	mustStmt(t, "ALTER PLAYBOOK bare SET VAR OWN=1")
	if got := explainKeys(t, "bare"); !reflect.DeepEqual(got, []string{"OWN"}) {
		t.Fatalf("EXPLAIN once the playbook has its own manifest: %v", got)
	}
}

// SHOW reports the command a pilot types: the playbook's name when the
// default launcher is in place, its LAUNCHER otherwise, and null only under
// NO LAUNCHER. cpb writes no version nobody gave, and the resume command
// of a session uses the same launcher.
func TestShowPlaybookLauncherIsTheCommand(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	mustStmt(t, "CREATE PLAYBOOK writer")
	mustStmt(t, "CREATE PLAYBOOK sre LAUNCHER ops")
	mustStmt(t, "CREATE PLAYBOOK quiet NO LAUNCHER")
	var rows []struct {
		Name     string  `json:"name"`
		Launcher *string `json:"launcher"`
		Version  *string `json:"version"`
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW PLAYBOOKS --json")), &rows); err != nil || len(rows) != 3 {
		t.Fatalf("SHOW PLAYBOOKS --json: %v %+v", err, rows)
	}
	want := map[string]string{"writer": "writer", "sre": "ops", "quiet": ""}
	for _, r := range rows {
		got := ""
		if r.Launcher != nil {
			got = *r.Launcher
		}
		if got != want[r.Name] {
			t.Errorf("%s: launcher %q, want %q", r.Name, got, want[r.Name])
		}
		if r.Version != nil {
			t.Errorf("%s: version %q, but nobody gave one", r.Name, *r.Version)
		}
	}
	data, err := os.ReadFile(filepath.Join(config.ResolvePlaybooksDir(), "sre", manifest.FileName))
	if err != nil || strings.Contains(string(data), "version") {
		t.Fatalf("the manifest that records the launcher: %v\n%s", err, data)
	}
	if out := mustStmt(t, "SHOW PLAYBOOK writer"); !regexp.MustCompile(`(?m)^Launcher:\s+writer$`).MatchString(out) {
		t.Errorf("SHOW PLAYBOOK writer:\n%s", out)
	}
	pb, err := playbook.Find(config.ResolvePlaybooksDir(), "writer")
	if err != nil || pb == nil {
		t.Fatal(err)
	}
	if got := playbookSessionDir(pb).resumeCommand("abc"); got != "writer --resume abc" {
		t.Errorf("resume command %q, want writer --resume abc", got)
	}
}

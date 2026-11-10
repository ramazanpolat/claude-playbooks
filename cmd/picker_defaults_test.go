package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// launchSettingsArg is the --settings a stub claude was started with,
// decoded; nil when it was started without one.
func launchSettingsArg(t *testing.T, log string) map[string]any {
	t.Helper()
	_, args := stubLaunch(t, log)
	rest, ok := strings.CutPrefix(args, "--settings ")
	if !ok {
		if strings.Contains(args, "--settings") {
			t.Fatalf("--settings not first: %s", args)
		}
		return nil
	}
	// The JSON object is one argument: up to the brace that closes it.
	depth, end := 0, -1
	inStr, esc := false, false
	for i, r := range rest {
		switch {
		case esc:
			esc = false
		case inStr && r == '\\':
			esc = true
		case r == '"':
			inStr = !inStr
		case !inStr && r == '{':
			depth++
		case !inStr && r == '}':
			depth--
			if depth == 0 {
				end = i + 1
			}
		}
		if end > 0 {
			break
		}
	}
	if end < 0 {
		t.Fatalf("no JSON after --settings: %s", args)
	}
	if strings.Count(args, "--settings") != 1 {
		t.Fatalf("two --settings: %s", args)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(rest[:end]), &v); err != nil {
		t.Fatalf("--settings %s: %v", rest[:end], err)
	}
	return v
}

func pickerModels(t *testing.T, s map[string]any) []string {
	t.Helper()
	mp, _ := s["modelPicker"].(map[string]any)
	var out []string
	opts, _ := mp["options"].([]any)
	for _, o := range opts {
		out = append(out, o.(map[string]any)["model"].(string))
	}
	return out
}

const defaultsAliases = "ALTER DEFAULTS ADD MODEL 'opus' LABEL 'Opus (newest)' ADD MODEL 'sonnet' LABEL 'Sonnet (newest)' " +
	"ADD MODEL 'haiku' LABEL 'Haiku (newest)' ADD MODEL 'fable' LABEL 'Fable (newest)'"

// ALTER DEFAULTS ADD MODEL / DROP MODEL keep the rows in the playbooks root
// (0600), key them by model id like a playbook's, and SHOW DEFAULTS, SELECT
// and SHOW CREATE ALL say them back.
func TestDefaultsModelRows(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	writePlaybook(t, root, "p", &manifest.Manifest{IsolatedLogin: true})
	out, err := quotedStmt(t, defaultsAliases)
	if err != nil || !strings.Contains(out, "Altered DEFAULTS") || !strings.Contains(out, "model     fable") {
		t.Fatalf("ADD MODEL: %v\n%s", err, out)
	}
	info, err := os.Stat(filepath.Join(root, ".model-picker"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the rows' file: %v %v", info, err)
	}
	if out, err := quotedStmt(t, defaultsAliases); err != nil || !strings.Contains(out, "DEFAULTS unchanged") {
		t.Fatalf("a repeat changed something: %v\n%s", err, out)
	}
	// In place, keeping the row's place.
	if _, err := quotedStmt(t, "ALTER DEFAULTS ADD MODEL 'opus' DESCRIPTION 'The newest Opus'"); err != nil {
		t.Fatal(err)
	}
	human := mustStmt(t, "SHOW DEFAULTS")
	if !strings.Contains(human, "Model picker") || !strings.Contains(human, "opus (Opus (newest)), sonnet (Sonnet (newest)), haiku (Haiku (newest)), fable (Fable (newest))") {
		t.Fatalf("SHOW DEFAULTS:\n%s", human)
	}
	var v struct {
		ModelPicker *struct {
			Options []struct {
				Model       string  `json:"model"`
				Label       *string `json:"label"`
				Description *string `json:"description"`
			} `json:"options"`
		} `json:"model_picker"`
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW DEFAULTS --json")), &v); err != nil || v.ModelPicker == nil || len(v.ModelPicker.Options) != 4 ||
		v.ModelPicker.Options[0].Model != "opus" || v.ModelPicker.Options[0].Description == nil || *v.ModelPicker.Options[0].Description != "The newest Opus" {
		t.Fatalf("SHOW DEFAULTS --json: %v %+v", err, v.ModelPicker)
	}
	if dump := mustStmt(t, "SHOW CREATE ALL"); !strings.Contains(dump, "ADD MODEL 'opus' LABEL 'Opus (newest)' DESCRIPTION 'The newest Opus'") ||
		!strings.Contains(dump, "ADD MODEL 'fable' LABEL 'Fable (newest)'") {
		t.Fatalf("SHOW CREATE ALL:\n%s", dump)
	}
	if sel := mustStmt(t, "SELECT model_picker FROM defaults --json"); !strings.Contains(sel, `"fable"`) {
		t.Fatalf("SELECT model_picker FROM defaults:\n%s", sel)
	}
	if _, err := quotedStmt(t, "ALTER DEFAULTS DROP MODEL 'gone'"); err == nil || !strings.Contains(err.Error(), "DEFAULTS has no model row for it") {
		t.Fatalf("DROP of a row that is not there: %v", err)
	}
	if _, err := quotedStmt(t, "ALTER DEFAULTS DROP MODEL 'opus' DROP MODEL 'sonnet' DROP MODEL 'haiku' DROP MODEL 'fable'"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".model-picker")); !os.IsNotExist(err) {
		t.Fatalf("the last DROP left the file: %v", err)
	}
	if human := mustStmt(t, "SHOW DEFAULTS"); !strings.Contains(human, "(none)") {
		t.Fatalf("SHOW DEFAULTS with no rows:\n%s", human)
	}
	// A dry run writes nothing.
	f := writePlaybookFile(t, defaultsAliases+";\n")
	if _, err := apply(t, f, "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".model-picker")); !os.IsNotExist(err) {
		t.Fatalf("a dry run wrote the rows: %v", err)
	}
}

// After one DEFAULTS statement, a playbook made before it and one made after
// it both launch with the rows, as one --settings that keeps the playbook's
// own picker whole and first; a launch that must not change does not.
func TestLaunchOffersDefaultsModelRows(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	writePlaybook(t, root, "before", &manifest.Manifest{IsolatedLogin: true})
	writePlaybook(t, root, "own", &manifest.Manifest{IsolatedLogin: true})
	ownSettings := `{"theme": "dark", "modelPicker": {"options": [{"model": "fable", "label": "Fable, latest"}, {"model": "my-row", "label": "Mine", "extra": 1}], "replaceBuiltInOptions": false}}`
	if err := os.WriteFile(filepath.Join(root, "own", "settings.json"), []byte(ownSettings), 0o644); err != nil {
		t.Fatal(err)
	}
	writePlaybook(t, root, "only", &manifest.Manifest{IsolatedLogin: true})
	if err := os.WriteFile(filepath.Join(root, "only", "settings.json"), []byte(`{"modelPicker": {"options": [{"model": "glm-5.3"}], "replaceBuiltInOptions": true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writePlaybook(t, root, "excluded", &manifest.Manifest{IsolatedLogin: true, ModelPickerDefaults: "excluded"})
	writePlaybook(t, root, "routed", &manifest.Manifest{IsolatedLogin: true})
	writePlaybook(t, root, "gw", &manifest.Manifest{IsolatedLogin: true})
	writePlaybook(t, root, "console", &manifest.Manifest{IsolatedLogin: true})
	writePlaybook(t, root, "direct", &manifest.Manifest{IsolatedLogin: true})
	claudeLog := stubClaude(t)

	// Before the statement nothing changes.
	if err := runRun(nil, []string{"before", "--version"}); err != nil {
		t.Fatal(err)
	}
	if s := launchSettingsArg(t, claudeLog); s != nil {
		t.Fatalf("a launch with no DEFAULTS rows passed --settings %v", s)
	}

	if _, err := quotedStmt(t, defaultsAliases); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "CREATE PLAYBOOK newer SET launcher = '', login = 'isolated'")
	want := []string{"opus", "sonnet", "haiku", "fable"}
	for _, name := range []string{"before", "newer"} {
		if err := runRun(nil, []string{name, "--version"}); err != nil {
			t.Fatal(err)
		}
		s := launchSettingsArg(t, claudeLog)
		if got := pickerModels(t, s); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: picker %v, want %v (%v)", name, got, want, s)
		}
		if _, ok := s["apiKeyHelper"]; ok || len(s) != 1 {
			t.Fatalf("%s: --settings holds more than the picker: %v", name, s)
		}
		if _, err := os.Stat(filepath.Join(root, name, "settings.json")); err == nil {
			if data, _ := os.ReadFile(filepath.Join(root, name, "settings.json")); strings.Contains(string(data), "modelPicker") {
				t.Fatalf("%s: the rows were written into settings.json: %s", name, data)
			}
		}
	}

	// The playbook's own rows stay whole and first; an exact duplicate is
	// dropped; its other picker keys stay.
	if err := runRun(nil, []string{"own", "--resume", "abc"}); err != nil {
		t.Fatal(err)
	}
	_, args := stubLaunch(t, claudeLog)
	if !strings.HasSuffix(args, " --resume abc") {
		t.Fatalf("a resumed launch's own arguments: %s", args)
	}
	s := launchSettingsArg(t, claudeLog)
	if got := pickerModels(t, s); !reflect.DeepEqual(got, []string{"fable", "my-row", "opus", "sonnet", "haiku"}) {
		t.Fatalf("own picker merged: %v", got)
	}
	mp := s["modelPicker"].(map[string]any)
	first := mp["options"].([]any)[1].(map[string]any)
	if first["extra"] != float64(1) || first["label"] != "Mine" || mp["replaceBuiltInOptions"] != false {
		t.Fatalf("own picker not kept verbatim: %v", mp)
	}

	// Launches that stay exactly as they were.
	mustStmt(t, "CREATE ENV router SET ANTHROPIC_BASE_URL=http://127.0.0.1:9/v1 ANTHROPIC_AUTH_TOKEN=router-token AS PLAINTEXT")
	mustStmt(t, "ALTER PLAYBOOK routed USE ENV router")
	putAccount(t, account.Account{Name: "gwacct", Provider: account.Gateway, BaseURL: "http://127.0.0.1:9/v1", Auth: account.Auth{Kind: account.None}})
	mustStmt(t, "ALTER PLAYBOOK gw USE ACCOUNT gwacct")
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"only", []string{"only", "--version"}},
		{"excluded", []string{"excluded", "--version"}},
		{"routed", []string{"routed", "--version"}},
		{"gw", []string{"gw", "--version"}},
		{"own settings", []string{"before", "--settings", `{"theme":"light"}`, "--version"}},
	} {
		if err := runRun(nil, tc.args); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		_, args := stubLaunch(t, claudeLog)
		if strings.Contains(args, "modelPicker") || (tc.name != "own settings" && strings.Contains(args, "--settings")) {
			t.Fatalf("%s: the launch changed: %s", tc.name, args)
		}
		if tc.name == "own settings" && !strings.HasPrefix(args, `--settings {"theme":"light"} --version`) {
			t.Fatalf("a launch's own --settings: %s", args)
		}
	}

	// Anthropic's own API with a key of its own still gets the rows; a
	// helper account's apiKeyHelper shares the one --settings.
	putAccount(t, account.Account{Name: "direct", Provider: account.Anthropic, BaseURL: "https://api.anthropic.com", Auth: account.Auth{Kind: account.APIKey, Value: "sk-test"}})
	mustStmt(t, "ALTER PLAYBOOK direct USE ACCOUNT direct")
	if err := runRun(nil, []string{"direct", "--version"}); err != nil {
		t.Fatal(err)
	}
	if got := pickerModels(t, launchSettingsArg(t, claudeLog)); !reflect.DeepEqual(got, want) {
		t.Fatalf("an Anthropic API-key account: %v", got)
	}
	if _, err := quotedStmt(t, "CREATE ACCOUNT console SET provider = 'anthropic', helper = 'my-key-helper --vault work'"); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "ALTER PLAYBOOK console USE ACCOUNT console")
	if err := runRun(nil, []string{"console", "--version"}); err != nil {
		t.Fatal(err)
	}
	s = launchSettingsArg(t, claudeLog)
	if s["apiKeyHelper"] != "my-key-helper --vault work" || !reflect.DeepEqual(pickerModels(t, s), want) {
		t.Fatalf("a helper account's one --settings: %v", s)
	}

	// EXPLAIN says what the launch does, from the same decision.
	if ex := mustStmt(t, "EXPLAIN PLAYBOOK before"); !strings.Contains(ex, "Model picker at launch: adds opus, sonnet, haiku, fable from DEFAULTS") {
		t.Fatalf("EXPLAIN before:\n%s", ex)
	}
	for name, why := range map[string]string{"excluded": pickerSkipExcluded, "only": pickerSkipOnly, "routed": pickerSkipRoute, "gw": pickerSkipRoute} {
		if ex := mustStmt(t, "EXPLAIN PLAYBOOK "+name); !strings.Contains(ex, "Model picker at launch: adds nothing ("+why+")") {
			t.Fatalf("EXPLAIN %s:\n%s", name, ex)
		}
	}
	var ex struct {
		At *struct {
			Added   []string `json:"added"`
			Skipped *string  `json:"skipped"`
		} `json:"model_picker_at_launch"`
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "EXPLAIN PLAYBOOK own --json")), &ex); err != nil || ex.At == nil || ex.At.Skipped != nil ||
		!reflect.DeepEqual(ex.At.Added, []string{"opus", "sonnet", "haiku"}) {
		t.Fatalf("EXPLAIN own --json: %v %+v", err, ex.At)
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "EXPLAIN PLAYBOOK excluded --json")), &ex); err != nil || ex.At == nil || ex.At.Skipped == nil || len(ex.At.Added) != 0 {
		t.Fatalf("EXPLAIN excluded --json: %v %+v", err, ex.At)
	}
}

// A sandboxed launch builds claude's argv inside the sandbox: the one
// --settings goes there too.
func TestSandboxedLaunchOffersDefaultsModelRows(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	writePlaybook(t, root, "boxed", &manifest.Manifest{IsolatedLogin: true, Sandbox: &manifest.Sandbox{Always: true}})
	work := t.TempDir()
	sbxLog := stubSbx(t)
	stubClaude(t)
	if _, err := quotedStmt(t, "ALTER DEFAULTS ADD MODEL 'fable' LABEL 'Fable (newest)'"); err != nil {
		t.Fatal(err)
	}
	if err := runRun(nil, []string{"--workdir", work, "boxed", "--continue"}); err != nil {
		t.Fatal(err)
	}
	var attach string
	for _, c := range sbxCalls(t, sbxLog) {
		if strings.HasPrefix(c, "exec -i") || strings.HasPrefix(c, "exec -it") {
			attach = c
		}
	}
	if !strings.Contains(attach, `claude '--settings' '{"modelPicker":{"options":[{"model":"fable","label":"Fable (newest)"}]}}' '--continue'`) {
		t.Fatalf("the sandbox's claude arguments:\n%s", attach)
	}
}

// model_picker_defaults: SET and RESET write the manifest, SHOW and SHOW
// CREATE say it, a plain directory refuses it.
func TestModelPickerDefaultsProperty(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	writePlaybook(t, root, "p", &manifest.Manifest{IsolatedLogin: true})
	out := mustStmt(t, "ALTER PLAYBOOK p SET model_picker_defaults = 'excluded'")
	if !strings.Contains(out, "model_picker_defaults  excluded") {
		t.Fatalf("SET:\n%s", out)
	}
	m, err := manifest.Read(filepath.Join(root, "p"))
	if err != nil || !m.ExcludesModelPickerDefaults() {
		t.Fatalf("the manifest: %v %+v", err, m)
	}
	if out := mustStmt(t, "ALTER PLAYBOOK p SET model_picker_defaults = 'excluded'"); !strings.Contains(out, "unchanged") {
		t.Fatalf("a repeat:\n%s", out)
	}
	if js := mustStmt(t, "SHOW PLAYBOOK p --json"); !strings.Contains(js, `"model_picker_defaults": "excluded"`) {
		t.Fatalf("SHOW --json:\n%s", js)
	}
	if human := mustStmt(t, "SHOW PLAYBOOK p"); !strings.Contains(human, "Model picker defaults") {
		t.Fatalf("SHOW:\n%s", human)
	}
	if sc := mustStmt(t, "SHOW CREATE PLAYBOOK p"); !strings.Contains(sc, "model_picker_defaults = 'excluded'") {
		t.Fatalf("SHOW CREATE:\n%s", sc)
	}
	mustStmt(t, "ALTER PLAYBOOK p RESET model_picker_defaults")
	if m, _ := manifest.Read(filepath.Join(root, "p")); m.ExcludesModelPickerDefaults() {
		t.Fatal("RESET kept it")
	}
	if data, _ := os.ReadFile(filepath.Join(root, "p", manifest.FileName)); strings.Contains(string(data), "model_picker_defaults") {
		t.Fatalf("the default is written: %s", data)
	}
	if js := mustStmt(t, "SHOW PLAYBOOK p --json"); !strings.Contains(js, `"model_picker_defaults": "inherited"`) {
		t.Fatalf("SHOW --json after RESET:\n%s", js)
	}
	// CREATE takes it, as an ALTER of the new playbook; SET IF UNSET too.
	mustStmt(t, "CREATE PLAYBOOK q SET launcher = '', model_picker_defaults = 'excluded'")
	if m, _ := manifest.Read(filepath.Join(root, "q")); !m.ExcludesModelPickerDefaults() {
		t.Fatal("CREATE … SET model_picker_defaults was dropped")
	}
	mustStmt(t, "ALTER PLAYBOOK p SET IF UNSET model_picker_defaults = 'excluded'")
	if m, _ := manifest.Read(filepath.Join(root, "p")); !m.ExcludesModelPickerDefaults() {
		t.Fatal("SET IF UNSET on the default")
	}
	dir := t.TempDir()
	f := writePlaybookFile(t, "ALTER PLAYBOOK SET model_picker_defaults = 'excluded';\n")
	if _, err := apply(t, f, "TO", dir, "--yes"); err == nil || !strings.Contains(err.Error(), "model_picker_defaults cannot apply") {
		t.Fatalf("a plain directory: %v", err)
	}
}

func TestPinnedModel(t *testing.T) {
	for id, want := range map[string]string{
		"claude-fable-5-1[1m]":       "fable[1m]",
		"claude-fable-5-2":           "fable",
		"claude-opus-5-5[1m]":        "opus[1m]",
		"claude-haiku-4-5-20251001":  "haiku",
		"claude-sonnet-5":            "sonnet",
		"claude-3-5-sonnet-20240620": "sonnet",
		"claude-mythos-1":            "",
	} {
		alias, pinned := pinnedModel(id)
		if !pinned || alias != want {
			t.Errorf("%s: pinned %v alias %q, want %q", id, pinned, alias, want)
		}
	}
	for _, id := range []string{"", "fable", "fable[1m]", "opus", "default", "cc/claude-fable-5-1", "glm-5.3", "anthropic.claude-opus-5-5",
		"claude-opus-4@20250514", "claude-opus", "claude-fable-5-1[2x]", "Claude-Opus-5"} {
		if alias, pinned := pinnedModel(id); pinned {
			t.Errorf("%q pinned (alias %q)", id, alias)
		}
	}
	if got := markPinned("claude-fable-5-1[1m]"); got != "claude-fable-5-1[1m] (pinned; fable[1m] follows releases)" {
		t.Errorf("markPinned: %q", got)
	}
	if got := markPinned("fable"); got != "fable" {
		t.Errorf("markPinned of an alias: %q", got)
	}
}

// A pinned id set or added says so on stderr after the result; stdout and
// the exit status stay as they were, and nothing is refused.
func TestPinnedModelNote(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	writePlaybook(t, root, "p", &manifest.Manifest{IsolatedLogin: true})
	for _, tc := range []struct{ line, note string }{
		{"ALTER PLAYBOOK p SET model = 'claude-fable-5-1[1m]'", "Note: 'claude-fable-5-1[1m]' names one release; 'fable[1m]' follows the newest the installed Claude Code knows.\n"},
		{"ALTER PLAYBOOK p ADD MODEL 'claude-haiku-4-5-20251001'", "Note: 'claude-haiku-4-5-20251001' names one release; 'haiku' follows the newest the installed Claude Code knows.\n"},
		{"ALTER DEFAULTS ADD MODEL 'claude-mythos-1'", "Note: 'claude-mythos-1' names one release: it stays on that model when a newer one ships.\n"},
		{"ALTER PLAYBOOK p SET model = 'opus'", ""},
		{"ALTER DEFAULTS ADD MODEL 'fable' LABEL 'Fable (newest)'", ""},
	} {
		var out string
		var err error
		stderr := captureStderr(t, func() { out, err = quotedStmt(t, tc.line) })
		if err != nil {
			t.Fatalf("%s: %v", tc.line, err)
		}
		if stderr != tc.note {
			t.Errorf("%s: stderr %q, want %q", tc.line, stderr, tc.note)
		}
		if strings.Contains(out, "Note:") {
			t.Errorf("%s: the note reached stdout:\n%s", tc.line, out)
		}
	}
	if human := mustStmt(t, "SHOW PLAYBOOK p"); !strings.Contains(human, "claude-haiku-4-5-20251001 (pinned; haiku follows releases)") {
		t.Fatalf("SHOW marks a pinned picker row:\n%s", human)
	}
	if human := mustStmt(t, "SHOW DEFAULTS"); !strings.Contains(human, "claude-mythos-1 (pinned to one release)") {
		t.Fatalf("SHOW DEFAULTS marks a pinned row:\n%s", human)
	}
	// EXPLAIN's Model line reads as before, and says it last.
	captureStderr(t, func() { _, _ = quotedStmt(t, "ALTER PLAYBOOK p SET model = 'claude-fable-5-1[1m]'") })
	if ex := mustStmt(t, "EXPLAIN PLAYBOOK p"); !strings.Contains(ex, "Model: claude-fable-5-1[1m] (playbook settings); a launch's --model and /model still win; pinned; fable[1m] follows releases\n") {
		t.Fatalf("EXPLAIN's Model line:\n%s", ex)
	}
	if human := mustStmt(t, "SHOW PLAYBOOK p"); !strings.Contains(human, "claude-fable-5-1[1m] (pinned; fable[1m] follows releases)") {
		t.Fatalf("SHOW's Model row:\n%s", human)
	}
}

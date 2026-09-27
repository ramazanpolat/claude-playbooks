package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func quotedStmt(t *testing.T, line string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = runStatement([]string{line}) })
	return out, err
}

func settingsOf(t *testing.T, dir string) map[string]any {
	t.Helper()
	var m map[string]any
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	return m
}

// ADD MODEL writes only modelPicker (no stray empty keys), upserts by
// model id keeping a row's place and its other fields, keeps rows no clause
// names, and a repeat changes nothing.
func TestModelPickerRows(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "k")
	add := "ALTER PLAYBOOK k ADD MODEL 'glm-5.3' LABEL 'GLM 5.3' ADD MODEL 'glm-5.3-flash' BEHAVES AS 'claude-sonnet-5'"
	if _, err := quotedStmt(t, add); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"modelPicker": map[string]any{"options": []any{
		map[string]any{"model": "glm-5.3", "label": "GLM 5.3"},
		map[string]any{"model": "glm-5.3-flash", "behavesAs": "claude-sonnet-5"},
	}}}
	if got := settingsOf(t, root); !reflect.DeepEqual(got, want) {
		t.Fatalf("settings:\n got %v\nwant %v", got, want)
	}
	if out, err := quotedStmt(t, add); err != nil || !strings.Contains(out, "unchanged") {
		t.Fatalf("a repeat changed something: %v\n%s", err, out)
	}

	// A hand-written row and key survive; a named row keeps its place.
	hand := `{"theme": "dark", "modelPicker": {"options": [{"model": "hand", "label": "Hand", "extra": 1}, {"model": "glm-5.3", "label": "GLM 5.3"}]}}`
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(hand), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := quotedStmt(t, "ALTER PLAYBOOK k ADD MODEL 'hand' DESCRIPTION 'mine' ADD MODEL 'new'"); err != nil {
		t.Fatal(err)
	}
	opts := settingsOf(t, root)["modelPicker"].(map[string]any)["options"].([]any)
	if len(opts) != 3 || opts[0].(map[string]any)["model"] != "hand" || opts[0].(map[string]any)["extra"] != float64(1) ||
		opts[0].(map[string]any)["label"] != "Hand" || opts[0].(map[string]any)["description"] != "mine" || opts[2].(map[string]any)["model"] != "new" {
		t.Fatalf("upsert: %v", opts)
	}
	if settingsOf(t, root)["theme"] != "dark" {
		t.Fatal("a key cpb did not write was lost")
	}
	if _, err := quotedStmt(t, "ALTER PLAYBOOK k DROP MODEL 'nope'"); err == nil || !strings.Contains(err.Error(), "no row for it") {
		t.Fatalf("DROP of a missing row: %v", err)
	}
}

// ONLY / APPEND, the reads (SHOW --json, EXPLAIN), SHOW CREATE re-applying
// unchanged, DROP of the last row, UNSET MODEL PICKER.
func TestModelPickerModeAndReads(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "k")
	if _, err := quotedStmt(t, "ALTER PLAYBOOK k ADD MODEL 'glm-5.3' LABEL 'GLM 5.3' SET MODEL PICKER ONLY"); err != nil {
		t.Fatal(err)
	}
	if mp := settingsOf(t, root)["modelPicker"].(map[string]any); mp["replaceBuiltInOptions"] != true {
		t.Fatalf("ONLY: %v", mp)
	}
	var v struct {
		ModelPicker *pickerJSON `json:"model_picker"`
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW PLAYBOOK k --json")), &v); err != nil || v.ModelPicker == nil ||
		v.ModelPicker.Mode != "only" || v.ModelPicker.Options[0].Model != "glm-5.3" || *v.ModelPicker.Options[0].Label != "GLM 5.3" {
		t.Fatalf("SHOW --json: %v %+v", err, v.ModelPicker)
	}
	if out := mustStmt(t, "EXPLAIN PLAYBOOK k"); !strings.Contains(out, "Model picker: only: glm-5.3 (GLM 5.3)") {
		t.Fatalf("EXPLAIN:\n%s", out)
	}
	created := mustStmt(t, "SHOW CREATE PLAYBOOK k")
	if !strings.Contains(created, "ADD MODEL 'glm-5.3' LABEL 'GLM 5.3'") || !strings.Contains(created, "SET MODEL PICKER ONLY") {
		t.Fatalf("SHOW CREATE:\n%s", created)
	}
	f := writePlaybookFile(t, created)
	if out, err := apply(t, f); err != nil || !strings.Contains(out, " 0 created, 0 changed,") {
		t.Fatalf("SHOW CREATE did not re-apply unchanged: %v\n%s", err, out)
	}
	if _, err := quotedStmt(t, "ALTER PLAYBOOK k SET MODEL PICKER APPEND DROP MODEL 'glm-5.3'"); err != nil {
		t.Fatal(err)
	}
	if mp := settingsOf(t, root)["modelPicker"].(map[string]any); mp["replaceBuiltInOptions"] != false || mp["options"] != nil {
		t.Fatalf("APPEND and the last row dropped: %v", mp)
	}
	if _, err := quotedStmt(t, "ALTER PLAYBOOK k UNSET MODEL PICKER"); err != nil {
		t.Fatal(err)
	}
	if _, ok := settingsOf(t, root)["modelPicker"]; ok {
		t.Fatal("UNSET MODEL PICKER left the key")
	}
}

// A plain config directory takes the picker too (user scope).
func TestModelPickerDirTarget(t *testing.T) {
	sandboxDefaultRoot(t)
	cfg, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.WriteFile(filepath.Join(cfg, "settings.json"), []byte(`{"theme": "dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	recipe := writeCpb(t, dir, "picker.cpb", "ALTER PLAYBOOK ADD MODEL 'glm-5.3' LABEL 'GLM 5.3' SET MODEL PICKER APPEND;\n")
	if out, err := apply(t, recipe, "TO", cfg, "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	s := settingsOf(t, cfg)
	mp := s["modelPicker"].(map[string]any)
	if s["theme"] != "dark" || mp["replaceBuiltInOptions"] != false || mp["options"].([]any)[0].(map[string]any)["label"] != "GLM 5.3" {
		t.Fatalf("dir target: %v", s)
	}
}

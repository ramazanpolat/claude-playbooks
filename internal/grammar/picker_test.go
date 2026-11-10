package grammar

import (
	"reflect"
	"strings"
	"testing"
)

func sp(s string) *string { return &s }

func TestParseModelPicker(t *testing.T) {
	st, err := ParseLine("ALTER PLAYBOOK k ADD MODEL 'glm-5.3-flash' LABEL 'GLM 5.3 Flash' DESCRIPTION 'fast, via the router' BEHAVES AS 'claude-sonnet-5' " +
		"ADD MODEL 'glm-5.3' DROP MODEL 'old-model' SET model_picker.mode = 'only' SET model = 'glm-5.3'")
	if err != nil {
		t.Fatal(err)
	}
	want := []Clause{
		{Kind: AddModel, Arg: "glm-5.3-flash", Names: []string{"glm-5.3-flash"},
			Row: &PickerRow{Model: "glm-5.3-flash", Label: sp("GLM 5.3 Flash"), Description: sp("fast, via the router"), BehavesAs: sp("claude-sonnet-5")}},
		{Kind: AddModel, Arg: "glm-5.3", Names: []string{"glm-5.3"}, Row: &PickerRow{Model: "glm-5.3"}},
		{Kind: DropModel, Arg: "old-model", Names: []string{"old-model"}},
		{Kind: SetModelPicker, Arg: "ONLY"},
		{Kind: SetModel, Arg: "glm-5.3"},
	}
	if got := strip(st).Clauses; !reflect.DeepEqual(got, want) {
		t.Errorf("clauses\n got %+v\nwant %+v", got, want)
	}
	again, err := ParseFile(st.String())
	if err != nil {
		t.Fatalf("%s: %v", st.String(), err)
	}
	if !reflect.DeepEqual(strip(again[0]), strip(st)) {
		t.Errorf("round trip changed the statement: %s", st.String())
	}
	// RESET model_picker is the mode: rows are a collection, which ADD
	// MODEL and DROP MODEL change, in the same statement too.
	if st, err := ParseLine("ALTER PLAYBOOK k ADD MODEL 'a' RESET model_picker"); err != nil || st.Clauses[1].Kind != UnsetModelPickerMode {
		t.Errorf("ADD MODEL with RESET model_picker: %v %+v", err, st)
	}
	if st, err := ParseArgs(w("ALTER PLAYBOOK k RESET model_picker")); err != nil || st.Clauses[0].Kind != UnsetModelPickerMode {
		t.Errorf("RESET model_picker is the mode: %v %+v", err, st)
	}
	if st, err := ParseArgs(w("ALTER PLAYBOOK k RESET model")); err != nil || st.Clauses[0].Kind != UnsetModel {
		t.Errorf("RESET model: %v %+v", err, st)
	}
	if st, err := ParseLine("ALTER PLAYBOOK k SET model = 'picker'"); err != nil || st.Clauses[0].Kind != SetModel {
		t.Errorf("a quoted model id named picker: %v %+v", err, st)
	}
}

func TestModelPickerErrors(t *testing.T) {
	for line, want := range map[string]string{
		"ALTER PLAYBOOK k ADD MODEL 'a' BEHAVES 'b'":                         "BEHAVES takes AS",
		"ALTER PLAYBOOK k ADD MODEL 'a' LABEL 'x' LABEL 'y'":                 "LABEL appears twice",
		"ALTER PLAYBOOK k SET MODEL PICKER":                                  "SET model_picker.mode = 'only' | 'append'",
		"ALTER PLAYBOOK k ADD MODEL 'a' DROP MODEL 'a'":                      "model a appears twice",
		"ALTER PLAYBOOK k SET model_picker.mode = 'only' RESET model_picker": "model_picker is named twice",
		"ALTER PLAYBOOK k ADD MODEL 'a b'":                                   "needs '<id>'",
	} {
		if _, err := ParseLine(line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", line, err, want)
		}
	}
}

// ALTER DEFAULTS ADD MODEL / DROP MODEL: the rows every playbook's launch
// offers. The same clauses as a playbook's, round-tripping the same way.
func TestParseDefaultsModels(t *testing.T) {
	st, err := ParseLine("ALTER DEFAULTS ADD MODEL 'fable' LABEL 'Fable (newest)' ADD MODEL 'opus' DROP MODEL 'old' ADD ENV corp")
	if err != nil {
		t.Fatal(err)
	}
	want := []Clause{
		{Kind: AddModel, Arg: "fable", Names: []string{"fable"}, Row: &PickerRow{Model: "fable", Label: sp("Fable (newest)")}},
		{Kind: AddModel, Arg: "opus", Names: []string{"opus"}, Row: &PickerRow{Model: "opus"}},
		{Kind: DropModel, Arg: "old", Names: []string{"old"}},
		{Kind: AddEnv, Names: []string{"corp"}, Where: Last},
	}
	if got := strip(st).Clauses; !reflect.DeepEqual(got, want) {
		t.Errorf("clauses\n got %+v\nwant %+v", got, want)
	}
	again, err := ParseFile(st.String())
	if err != nil {
		t.Fatalf("%s: %v", st.String(), err)
	}
	if !reflect.DeepEqual(strip(again[0]), strip(st)) {
		t.Errorf("round trip changed the statement: %s", st.String())
	}
	if st, err := ParseArgs(w("ALTER DEFAULTS DROP MODEL fable DROP ENV corp")); err != nil || st.Clauses[0].Kind != DropModel || st.Clauses[1].Kind != DropEnv {
		t.Errorf("DROP MODEL and DROP ENV: %v %+v", err, st)
	}
	for line, want := range map[string]string{
		"ALTER DEFAULTS ADD FOO x":                    "ADD takes ENV or MODEL",
		"ALTER DEFAULTS DROP FOO x":                   "DROP takes ENV or MODEL",
		"ALTER DEFAULTS ADD MODEL 'a' DROP MODEL 'a'": "model a appears twice",
		"ALTER DEFAULTS ADD MODEL 'a' BEHAVES 'b'":    "BEHAVES takes AS",
		"ALTER DEFAULTS ADD MODEL 'a b'":              "needs '<id>'",
	} {
		if _, err := ParseLine(line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", line, want, err)
		}
	}
}

// model_picker_defaults is a key of its own: RESET model_picker stays the
// picker's mode, and the two side by side are not one key twice.
func TestModelPickerDefaultsProperty(t *testing.T) {
	st, err := ParseLine("ALTER PLAYBOOK k SET model_picker_defaults = 'excluded' RESET model_picker")
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := PropertyValue(st.Clauses, "model_picker_defaults"); !ok || v != "excluded" {
		t.Errorf("model_picker_defaults: %q %v", v, ok)
	}
	if st.Clauses[1].Kind != UnsetModelPickerMode {
		t.Errorf("RESET model_picker: %+v", st.Clauses[1])
	}
	if st, err := ParseLine("ALTER PLAYBOOK k RESET model_picker_defaults"); err != nil {
		t.Fatal(err)
	} else if v, ok := PropertyValue(st.Clauses, "model_picker_defaults"); !ok || v != "inherited" {
		t.Errorf("RESET gives the default: %q %v", v, ok)
	}
	if _, err := ParseLine("ALTER PLAYBOOK k SET model_picker_defaults = 'off'"); err == nil || !strings.Contains(err.Error(), "inherited") {
		t.Errorf("a value outside the list: %v", err)
	}
}

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
	// DELETE model_picker is the mode: rows are a collection, which ADD
	// MODEL and DROP MODEL change, in the same statement too.
	if st, err := ParseLine("ALTER PLAYBOOK k ADD MODEL 'a' DELETE model_picker"); err != nil || st.Clauses[1].Kind != UnsetModelPickerMode {
		t.Errorf("ADD MODEL with DELETE model_picker: %v %+v", err, st)
	}
	if st, err := ParseArgs(w("ALTER PLAYBOOK k DELETE model_picker")); err != nil || st.Clauses[0].Kind != UnsetModelPickerMode {
		t.Errorf("DELETE model_picker is the mode: %v %+v", err, st)
	}
	if st, err := ParseArgs(w("ALTER PLAYBOOK k DELETE model")); err != nil || st.Clauses[0].Kind != UnsetModel {
		t.Errorf("DELETE model: %v %+v", err, st)
	}
	if st, err := ParseLine("ALTER PLAYBOOK k SET model = 'picker'"); err != nil || st.Clauses[0].Kind != SetModel {
		t.Errorf("a quoted model id named picker: %v %+v", err, st)
	}
}

func TestModelPickerErrors(t *testing.T) {
	for line, want := range map[string]string{
		"ALTER PLAYBOOK k ADD MODEL 'a' BEHAVES 'b'":                          "BEHAVES takes AS",
		"ALTER PLAYBOOK k ADD MODEL 'a' LABEL 'x' LABEL 'y'":                  "LABEL appears twice",
		"ALTER PLAYBOOK k SET MODEL PICKER":                                   "SET model_picker.mode = 'only' | 'append'",
		"ALTER PLAYBOOK k ADD MODEL 'a' DROP MODEL 'a'":                       "model a appears twice",
		"ALTER PLAYBOOK k SET model_picker.mode = 'only' DELETE model_picker": "model_picker is named twice",
		"ALTER PLAYBOOK k ADD MODEL 'a b'":                                    "needs '<id>'",
	} {
		if _, err := ParseLine(line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", line, err, want)
		}
	}
}

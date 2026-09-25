package manifest

import (
	"strings"
	"testing"
)

func TestMergeEnvWithReferences(t *testing.T) {
	got := MergeEnv(
		&Env{Set: map[string]string{"A": "lit", "B": "lit"}, Unset: []string{"C"}},
		&Env{Refs: map[string]string{"A": "keychain:a", "C": "keychain:c"}},
		&Env{Set: map[string]string{"C": "own"}, Unset: []string{"B"}},
	)
	if got.Refs["A"] != "keychain:a" || got.Set["A"] != "" {
		t.Errorf("a later ref overrides a literal: %#v", got)
	}
	if got.Set["C"] != "own" || got.Refs["C"] != "" {
		t.Errorf("a later literal overrides a ref: %#v", got)
	}
	if !got.Unsets("B") {
		t.Errorf("unset: %#v", got)
	}
	blocked := MergeEnv(&Env{Refs: map[string]string{"X": "op://v/i/f"}}, &Env{Unset: []string{"X"}})
	if len(blocked.Refs) != 0 || !blocked.Unsets("X") {
		t.Errorf("an unset drops a ref: %#v", blocked)
	}
}

func TestValidateRefs(t *testing.T) {
	ok := map[string]string{"TOKEN": "keychain:pilot/x", "KEY": "op://vault/item/field"}
	if err := ValidateRefs(ok, nil, nil); err != nil {
		t.Fatal(err)
	}
	const pasted = "sk-ant-api03-SECRET"
	for name, c := range map[string]struct {
		refs  map[string]string
		set   map[string]string
		unset []string
		want  string
	}{
		"not a reference": {map[string]string{"T": pasted}, nil, nil, "not a secret reference"},
		"both set":        {map[string]string{"T": "keychain:x"}, map[string]string{"T": "v"}, nil, "both set"},
		"both unset":      {map[string]string{"T": "keychain:x"}, nil, []string{"T"}, "both unset"},
		"refused key":     {map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "keychain:x"}, nil, nil, "cannot be a secret reference"},
		"reserved key":    {map[string]string{"CLAUDE_CONFIG_DIR": "keychain:x"}, nil, nil, "managed by claude-playbook"},
	} {
		err := ValidateRefs(c.refs, c.set, c.unset)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
		if err != nil && strings.Contains(err.Error(), pasted) {
			t.Errorf("%s: the error quotes the value", name)
		}
	}
}

func TestManifestReferencesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := &Manifest{Name: "p", Env: &Env{
		Set:  map[string]string{"A": "1"},
		Refs: map[string]string{"Z_TOKEN": "keychain:z", "B_TOKEN": "op://v/i/f"},
	}}
	if err := Write(dir, in); err != nil {
		t.Fatal(err)
	}
	m, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Env.Refs["Z_TOKEN"] != "keychain:z" || m.Env.Refs["B_TOKEN"] != "op://v/i/f" || m.Env.Set["A"] != "1" {
		t.Fatalf("round trip: %#v", m.Env)
	}
}

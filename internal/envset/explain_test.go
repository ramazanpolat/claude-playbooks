package envset

import (
	"errors"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

func TestExplainNamesTheDecidingLayer(t *testing.T) {
	dir := Dir(t.TempDir())
	for _, p := range []*Set{
		{Name: "d1", Set: map[string]string{"A": "d1", "B": "d1", "C": "d1"}},
		{Name: "d2", Set: map[string]string{"B": "d2"}, Block: []string{"PROXY"}},
		{Name: "glm", Set: map[string]string{"C": "glm", "PROXY": "back"}, Block: []string{"A"}},
	} {
		if err := Write(dir, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteDefaults(dir, []string{"d1", "d2"}); err != nil {
		t.Fatal(err)
	}
	block := &manifest.Env{Sets: []string{"glm"}, Set: map[string]string{"OWN": "1"}, Block: []string{"C"}}

	got, err := Explain(dir, block)
	if err != nil {
		t.Fatal(err)
	}
	want := []Origin{
		{Key: "A", Blocked: true, Kind: LayerEnv, Set: "glm"},
		{Key: "B", Value: "d2", Kind: LayerDefaults, Set: "d2"},
		{Key: "C", Blocked: true, Kind: LayerPlaybook},
		{Key: "OWN", Value: "1", Kind: LayerPlaybook},
		{Key: "PROXY", Value: "back", Kind: LayerEnv, Set: "glm"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("origin %d: got %+v, want %+v", i, got[i], want[i])
		}
	}

	// Explain and the launch must agree on every value.
	merged, err := ExpandWithDefault(dir, block)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range got {
		if o.Blocked != merged.Blocks(o.Key) {
			t.Errorf("%s: blocked %v, launch unsets %v", o.Key, o.Blocked, merged.Blocks(o.Key))
		}
		if !o.Blocked && merged.Set[o.Key] != o.Value {
			t.Errorf("%s: explain %q, launch %q", o.Key, o.Value, merged.Set[o.Key])
		}
	}
	if len(got) != len(merged.Set)+len(merged.Block) {
		t.Errorf("explain lists %d variables, the launch changes %d", len(got), len(merged.Set)+len(merged.Block))
	}
}

func TestExplainRefusesLikeTheLaunch(t *testing.T) {
	dir := Dir(t.TempDir())
	if _, err := Explain(dir, &manifest.Env{Sets: []string{"ghost"}}); !errors.Is(err, ErrSet) {
		t.Fatalf("missing profile: %v", err)
	}
	if got, err := Explain(dir, nil); err != nil || len(got) != 0 {
		t.Fatalf("nothing configured: %+v %v", got, err)
	}
}

func TestExplainAndExpandCarryReferences(t *testing.T) {
	dir := Dir(t.TempDir())
	if err := Write(dir, &Set{Name: "r", Set: map[string]string{"URL": "http://x"}, Refs: map[string]string{"TOKEN": "keychain:r"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Read(dir, "r")
	if err != nil || p.Refs["TOKEN"] != "keychain:r" {
		t.Fatalf("profile round trip: %#v %v", p, err)
	}
	block := &manifest.Env{Sets: []string{"r"}}
	merged, err := ExpandWithDefault(dir, block)
	if err != nil || merged.Refs["TOKEN"] != "keychain:r" {
		t.Fatalf("expand: %#v %v", merged, err)
	}
	got, err := Explain(dir, block)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (Origin{Key: "TOKEN", Ref: "keychain:r", Kind: LayerEnv, Set: "r"}) {
		t.Fatalf("explain: %+v", got)
	}
}

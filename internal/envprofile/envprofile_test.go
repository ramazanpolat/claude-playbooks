package envprofile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

func TestWriteReadRoundTrip(t *testing.T) {
	dir := Dir(t.TempDir())
	in := &Profile{Name: "glm", Description: "GLM via router",
		Set: map[string]string{"B": "2", "A": "1"}, Unset: []string{"Z"}}
	if err := Write(dir, in); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "glm.toml"))
	want := "description = \"GLM via router\"\nunset = [\"Z\"]\n\n[set]\nA = \"1\"\nB = \"2\"\n"
	if string(data) != want {
		t.Fatalf("serialized:\n%s\nwant:\n%s", data, want)
	}
	if info, _ := os.Stat(filepath.Join(dir, "glm.toml")); info.Mode().Perm() != 0o600 {
		t.Fatalf("profile mode = %v, want 0600 (values may be secrets)", info.Mode().Perm())
	}
	out, err := Read(dir, "glm")
	if err != nil || out == nil || out.Name != "glm" || out.Description != "GLM via router" ||
		out.Set["A"] != "1" || out.Set["B"] != "2" || len(out.Unset) != 1 || out.Unset[0] != "Z" {
		t.Fatalf("read back: %#v err=%v", out, err)
	}
	if p, err := Read(dir, "absent"); p != nil || err != nil {
		t.Fatalf("Read(absent) = %#v, %v", p, err)
	}
}

func TestReadRejectsInvalidProfiles(t *testing.T) {
	dir := Dir(t.TempDir())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"reserved":      "[set]\nCLAUDE_CONFIG_DIR = \"/x\"\n",
		"bad key":       "unset = [\"NOT-A-NAME\"]\n",
		"set and unset": "unset = [\"A\"]\n[set]\nA = \"1\"\n",
		"bad toml":      "= [\n",
	}
	for name, body := range cases {
		if err := os.WriteFile(filepath.Join(dir, "p.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(dir, "p"); err == nil {
			t.Errorf("%s: profile accepted:\n%s", name, body)
		}
	}
	for _, name := range []string{"", ".hidden", "a/b", "a b", "../x"} {
		if _, err := Read(dir, name); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
}

func TestListSortsAndSkipsNonProfiles(t *testing.T) {
	root := t.TempDir()
	dir := Dir(root)
	if got, err := List(dir); got != nil || err != nil {
		t.Fatalf("List(missing dir) = %v, %v", got, err)
	}
	for _, name := range []string{"zeta", "alpha"} {
		if err := Write(dir, &Profile{Name: name, Set: map[string]string{"K": name}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("not a profile"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := List(dir)
	if err != nil || len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Fatalf("List = %v, %v", got, err)
	}
}

func TestExpandLayersProfilesUnderPlaybookEntries(t *testing.T) {
	dir := Dir(t.TempDir())
	if err := Write(dir, &Profile{Name: "base", Set: map[string]string{"URL": "base", "MODEL": "base", "KEEP": "base"}, Unset: []string{"TOKEN", "GONE"}}); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, &Profile{Name: "over", Set: map[string]string{"MODEL": "over", "TOKEN": "re-set"}, Unset: []string{"URL"}}); err != nil {
		t.Fatal(err)
	}
	e := &manifest.Env{Profiles: []string{"base", "over"}, Set: map[string]string{"URL": "own"}, Unset: []string{"MODEL"}}

	got, err := Expand(dir, e)
	if err != nil {
		t.Fatal(err)
	}
	// URL: base set, over unset, own set -> "own". MODEL: base, over, own unset -> unset.
	// TOKEN: base unset, over set -> "re-set". GONE: unset. KEEP: "base".
	if got.Set["URL"] != "own" || got.Set["TOKEN"] != "re-set" || got.Set["KEEP"] != "base" {
		t.Fatalf("set = %#v", got.Set)
	}
	if _, still := got.Set["MODEL"]; still || !got.Unsets("MODEL") || !got.Unsets("GONE") || got.Unsets("URL") || got.Unsets("TOKEN") {
		t.Fatalf("unset = %#v set = %#v", got.Unset, got.Set)
	}
	if len(got.Profiles) != 0 {
		t.Fatalf("profiles leaked into the flattened block: %v", got.Profiles)
	}

	// Without profiles the block is returned untouched, nil included.
	plain := &manifest.Env{Set: map[string]string{"A": "1"}}
	if got, err := Expand(dir, plain); err != nil || got != plain {
		t.Fatalf("Expand(no profiles) = %#v, %v", got, err)
	}
	if got, err := Expand(dir, nil); err != nil || got != nil {
		t.Fatalf("Expand(nil) = %#v, %v", got, err)
	}
}

func TestExpandMissingProfileIsTyped(t *testing.T) {
	dir := Dir(t.TempDir())
	_, err := Expand(dir, &manifest.Env{Profiles: []string{"nope"}})
	var missing *MissingError
	if !errors.As(err, &missing) || missing.Name != "nope" {
		t.Fatalf("err = %v, want *MissingError for nope", err)
	}
}

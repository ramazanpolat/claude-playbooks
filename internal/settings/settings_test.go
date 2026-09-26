package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEditKeepsOrderAndOtherKeys(t *testing.T) {
	dir := t.TempDir()
	orig := "{\n    \"permissions\": {\"allow\": [\"Bash(ls:*)\"]},\n    \"enabledPlugins\": {\"b@m\": false, \"a@m\": true},\n    \"statusLine\": {\"type\": \"command\", \"command\": \"x <y> & z\"}\n}\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	plugins, err := f.Root.Object("enabledPlugins")
	if err != nil {
		t.Fatal(err)
	}
	if err := plugins.Set("c@m", true); err != nil {
		t.Fatal(err)
	}
	plugins.Delete("a@m")
	f.Root.SetObject("enabledPlugins", plugins)
	if err := f.Root.Set("agent", "kommander"); err != nil {
		t.Fatal(err)
	}
	if err := f.Write(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(f.Path)
	want := "{\n    \"permissions\": {\n        \"allow\": [\n            \"Bash(ls:*)\"\n        ]\n    },\n" +
		"    \"enabledPlugins\": {\n        \"b@m\": false,\n        \"c@m\": true\n    },\n" +
		"    \"statusLine\": {\n        \"type\": \"command\",\n        \"command\": \"x <y> & z\"\n    },\n" +
		"    \"agent\": \"kommander\"\n}\n"
	if string(got) != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if info, _ := os.Stat(f.Path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600 kept", info.Mode().Perm())
	}
	if err := f.Restore(); err != nil {
		t.Fatal(err)
	}
	if back, _ := os.ReadFile(f.Path); string(back) != orig {
		t.Errorf("restore gave\n%s", back)
	}
}

func TestMissingFileAndEmptyObjectRemoved(t *testing.T) {
	dir := t.TempDir()
	f, err := Load(dir)
	if err != nil || f.Exists {
		t.Fatalf("missing file: %v exists=%v", err, f.Exists)
	}
	sub, _ := f.Root.Object("extraKnownMarketplaces")
	f.Root.SetObject("extraKnownMarketplaces", sub) // empty: stays absent
	if f.Root.Has("extraKnownMarketplaces") {
		t.Error("an empty object was added")
	}
	if err := f.Restore(); err != nil { // no file before: none after
		t.Fatal(err)
	}
	if _, err := ParseObject([]byte(`[1]`)); err == nil {
		t.Error("an array parsed as an object")
	}
}

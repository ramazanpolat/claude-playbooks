package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadRejectsEscapingSourcePaths(t *testing.T) {
	for _, field := range []string{"subdir"} {
		t.Run(field, func(t *testing.T) {
			dir := t.TempDir()
			content := "version = \"0.1.0\"\n[source]\n" + field + " = \"../outside\"\n"
			if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := Read(dir); err == nil {
				t.Fatalf("expected %s traversal to be rejected", field)
			}
		})
	}
}

func TestReadAllowsDotDotPrefixInOrdinaryName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("subdir = \"..config\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err != nil {
		t.Fatalf("safe path was rejected: %v", err)
	}
}

func TestResolveSubdirRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(root, "config")); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveSubdir(root, "subdir", "config")
	if err == nil || !strings.Contains(err.Error(), "resolves outside") {
		t.Fatalf("expected symlink escape error, got %v", err)
	}
}

func TestWriteValidatesSourcePaths(t *testing.T) {
	err := Write(t.TempDir(), &Manifest{Source: &Source{Subdir: "/tmp/outside"}})
	if err == nil {
		t.Fatal("expected Write to validate source.subdir")
	}
}

func TestReadRejectsEscapingPreservePath(t *testing.T) {
	dir := t.TempDir()
	content := "version = \"0.1.0\"\n[update]\npreserve = [\"../outside\"]\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err == nil {
		t.Fatal("expected update.preserve traversal to be rejected")
	}
}

func TestPreserveRoundTrips(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, &Manifest{Update: &Update{Preserve: []string{"settings.json", "local/notes.md"}}}); err != nil {
		t.Fatal(err)
	}
	m, err := Read(dir)
	if err != nil || m == nil || m.Update == nil {
		t.Fatalf("m=%#v err=%v", m, err)
	}
	if len(m.Update.Preserve) != 2 || m.Update.Preserve[0] != "settings.json" || m.Update.Preserve[1] != "local/notes.md" {
		t.Fatalf("preserve=%#v", m.Update.Preserve)
	}
}

func TestEnvRoundTrips(t *testing.T) {
	dir := t.TempDir()
	in := &Manifest{
		Name: "pb",
		Env: &Env{
			Set:   map[string]string{"ANTHROPIC_BASE_URL": "http://proxy:1/v1", "A_FLAG": "x=y"},
			Unset: []string{"CLAUDE_CODE_OAUTH_TOKEN"},
		},
	}
	if err := Write(dir, in); err != nil {
		t.Fatal(err)
	}
	out, err := Read(dir)
	if err != nil || out == nil || out.Env == nil {
		t.Fatalf("read back: m=%#v err=%v", out, err)
	}
	if out.Env.Set["ANTHROPIC_BASE_URL"] != "http://proxy:1/v1" || out.Env.Set["A_FLAG"] != "x=y" {
		t.Fatalf("set did not round-trip: %#v", out.Env.Set)
	}
	if !out.Env.Unsets("CLAUDE_CODE_OAUTH_TOKEN") {
		t.Fatalf("unset did not round-trip: %#v", out.Env.Unset)
	}
	// Serialization order is deterministic: [env] header, unset, then the
	// [env.set] table with sorted keys.
	data, _ := os.ReadFile(filepath.Join(dir, FileName))
	want := "[env]\nunset = [\"CLAUDE_CODE_OAUTH_TOKEN\"]\n\n[env.set]\nANTHROPIC_BASE_URL = \"http://proxy:1/v1\"\nA_FLAG = \"x=y\"\n"
	if !strings.HasSuffix(string(data), want) {
		t.Fatalf("serialized manifest:\n%s\nwant suffix:\n%s", data, want)
	}
}

func TestEnvEmptyBlockIsNotWritten(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, &Manifest{Name: "pb", Env: &Env{}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, FileName))
	if strings.Contains(string(data), "[env]") {
		t.Fatalf("empty env block was serialized:\n%s", data)
	}
}

func TestReadRejectsInvalidEnv(t *testing.T) {
	cases := map[string]string{
		"reserved set":   "[env.set]\nCLAUDE_CONFIG_DIR = \"/x\"\n",
		"reserved unset": "[env]\nunset = [\"CLAUDE_CONFIG_DIR\"]\n",
		"bad name":       "[env]\nunset = [\"NOT-A-NAME\"]\n",
		"bad set name":   "[env.set]\n\"1BAD\" = \"v\"\n",
		"set and unset":  "[env]\nunset = [\"FOO\"]\n[env.set]\nFOO = \"v\"\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, FileName), []byte("name = \"pb\"\n"+body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Read(dir); err == nil {
				t.Fatalf("manifest accepted:\n%s", body)
			}
		})
	}
}

func TestNearestWalksToInstallRoot(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "playbook")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, FileName), []byte("name = \"pb\"\nsubdir = \"playbook\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Nearest(sub)
	if err != nil || m == nil || m.Name != "pb" {
		t.Fatalf("Nearest(sub) = %#v, %v", m, err)
	}
	if m, err := Nearest(t.TempDir()); err != nil || m != nil {
		t.Fatalf("Nearest(no manifest) = %#v, %v; want nil, nil", m, err)
	}
}

// Values under [env.set] may be tokens: a manifest carrying any is private.
func TestWriteKeepsEnvValuesPrivate(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, &Manifest{Name: "pb"}); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(filepath.Join(dir, FileName)); info.Mode().Perm() != 0o644 {
		t.Fatalf("plain manifest mode = %v, want 0644", info.Mode().Perm())
	}
	// Adding a value to an existing 0644 file tightens it.
	if err := Write(dir, &Manifest{Name: "pb", Env: &Env{Set: map[string]string{"K": "secret"}}}); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(filepath.Join(dir, FileName)); info.Mode().Perm() != 0o600 {
		t.Fatalf("manifest with env values mode = %v, want 0600", info.Mode().Perm())
	}
	// Clearing the values never loosens a file the pilot may have made private.
	if err := Write(dir, &Manifest{Name: "pb", Env: &Env{Unset: []string{"K"}}}); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(filepath.Join(dir, FileName)); info.Mode().Perm() != 0o600 {
		t.Fatalf("rewrite loosened the manifest to %v", info.Mode().Perm())
	}
}

// A broken manifest in a subdir must not hide the install root's: the
// nearest VALID one governs, and the error is still reported.
func TestNearestWalksPastBrokenManifest(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "playbook")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, FileName), []byte("name = \"pb\"\nisolate_auth = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, FileName), []byte("= [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Nearest(sub)
	if m == nil || !m.IsolateAuth {
		t.Fatalf("Nearest stopped at the broken manifest: m=%#v", m)
	}
	if err == nil {
		t.Fatal("the broken manifest went unreported")
	}
}

// A secret must never be readable at a looser mode than its final one, not
// even mid-write: the file is replaced by rename, so an existing 0644 file
// is never truncated and rewritten in place, and no temp file lingers.
func TestWritePrivateNeverExposesContent(t *testing.T) {
	dir := t.TempDir()
	at := filepath.Join(dir, FileName)
	if err := os.WriteFile(at, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(at)
	if err := WritePrivate(at, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(at)
	if os.SameFile(before, after) {
		t.Fatal("the existing public file was rewritten in place instead of replaced")
	}
	if after.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", after.Mode().Perm())
	}
	if got, _ := os.ReadFile(at); string(got) != "secret\n" {
		t.Fatalf("content = %q", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

// A deliberately restrictive mode that is not fully private (0640) is kept
// exactly by a rewrite without values, as an in-place write would have.
func TestWritePreservesPartialModes(t *testing.T) {
	dir := t.TempDir()
	at := filepath.Join(dir, FileName)
	if err := os.WriteFile(at, []byte("name = \"pb\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, &Manifest{Name: "pb", Alias: "p"}); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(at); info.Mode().Perm() != 0o640 {
		t.Fatalf("rewrite changed 0640 to %v", info.Mode().Perm())
	}
	if err := Write(dir, &Manifest{Name: "pb", Env: &Env{Set: map[string]string{"K": "v"}}}); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(at); info.Mode().Perm() != 0o600 {
		t.Fatalf("values did not tighten 0640 to 0600: %v", info.Mode().Perm())
	}
}

// A broken manifest's error must not echo file content: since [env.set] may
// hold credentials, and this error reaches the terminal from every command
// that discovers playbooks, only the line number is reported.
func TestReadDoesNotEchoBrokenManifestContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("name = \"pb\"\n[env.set]\nKEY = sk-ant-SECRETVALUE-unquoted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Read(dir)
	if err == nil {
		t.Fatal("broken manifest accepted")
	}
	if strings.Contains(err.Error(), "SECRETVALUE") {
		t.Fatalf("error echoes manifest content: %v", err)
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("error lacks the line number: %v", err)
	}
}

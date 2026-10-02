package cmd

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// TestSbxCallLogGolden pins everything an sbx launch does, whole: every sbx
// call in order, stderr, the error and where the playbook's credential store
// points afterwards. The other sandbox tests check prefixes and fragments;
// this one fails on any added, dropped or reordered call, so a change to the
// backend seam that is meant to leave sbx alone can prove it did. Paths are
// normalized, and the attach's -e flags sorted: the launch layers the
// environment through maps, so their order varies from run to run and
// means nothing to sbx. CPB_UPDATE_GOLDEN=1 rewrites testdata/sbx-golden/.
func TestSbxCallLogGolden(t *testing.T) {
	type scenario struct {
		name  string
		setup func(t *testing.T, root, work string) (playbook string, args []string)
	}
	scenarios := []scenario{
		{"create-routed", func(t *testing.T, root, work string) (string, []string) {
			if err := stmtErr(t, "CREATE ENV router SET ANTHROPIC_BASE_URL=http://router.local:9/v1 ANTHROPIC_AUTH_TOKEN=real-token ANTHROPIC_API_KEY=real-key MODEL=glm AS PLAINTEXT"); err != nil {
				t.Fatal(err)
			}
			extra := filepath.Join(filepath.Dir(work), "extra")
			if err := os.MkdirAll(extra, 0o755); err != nil {
				t.Fatal(err)
			}
			writePlaybook(t, root, "box", &manifest.Manifest{IsolatedLogin: true, Env: &manifest.Env{Sets: []string{"router"}},
				Sandbox: &manifest.Sandbox{Mounts: []string{extra + ":ro"}, AllowNet: []string{"api.example.com"}, ClaudeVersion: "2.1.263"}})
			return "box", []string{"--env", "EXTRA=1", "-p", "it's"}
		}},
		{"host-service", func(t *testing.T, root, work string) (string, []string) {
			if err := stmtErr(t, "CREATE ENV local SET ANTHROPIC_BASE_URL=http://localhost:8080/v1 ANTHROPIC_AUTH_TOKEN=lt AS PLAINTEXT"); err != nil {
				t.Fatal(err)
			}
			writePlaybook(t, root, "onhost", &manifest.Manifest{IsolatedLogin: true, Env: &manifest.Env{Sets: []string{"local"}},
				Sandbox: &manifest.Sandbox{AllowNet: []string{"host.docker.internal", "other.example"}}})
			return "onhost", nil
		}},
		{"reuse-rotate", func(t *testing.T, root, work string) (string, []string) {
			if err := stmtErr(t, "CREATE ENV rot SET ANTHROPIC_API_KEY=new-key AS PLAINTEXT"); err != nil {
				t.Fatal(err)
			}
			writePlaybook(t, root, "rot", &manifest.Manifest{IsolatedLogin: true, Env: &manifest.Env{Sets: []string{"rot"}}})
			t.Setenv("SBX_STUB_LS", "cpb-rot")
			t.Setenv("SBX_STUB_LSJSON", `{"sandboxes":[{"name":"cpb-rot","workspaces":["`+canon(t, work)+`","`+canon(t, filepath.Join(root, "rot"))+`"]}]}`)
			t.Setenv("SBX_STUB_SECRETS", "cpb-rot api.anthropic.com ANTHROPIC_API_KEY cpb-rot-ANTHROPIC_API_KEY old-***")
			return "rot", nil
		}},
		{"revoke", func(t *testing.T, root, work string) (string, []string) {
			writePlaybook(t, root, "revoke", &manifest.Manifest{IsolatedLogin: true, Env: &manifest.Env{Set: map[string]string{"ANTHROPIC_BASE_URL": "http://router.local:9/v1"}}})
			t.Setenv("SBX_STUB_LS", "cpb-revoke")
			t.Setenv("SBX_STUB_LSJSON", `{"sandboxes":[{"name":"cpb-revoke","workspaces":["`+canon(t, work)+`","`+canon(t, filepath.Join(root, "revoke"))+`"]}]}`)
			t.Setenv("SBX_STUB_SECRETS", "cpb-revoke router.local ANTHROPIC_AUTH_TOKEN cpb-revoke-ANTHROPIC_AUTH_TOKEN old-***\ncpb-revoke router.local OTHER cpb-revoke-OTHER x")
			return "revoke", nil
		}},
		{"fail-closed-shared-login", func(t *testing.T, root, work string) (string, []string) {
			// #149: a failed registration refuses the launch before the
			// attach, and the shared login link the launch re-pointed at
			// the sandbox is restored on the way out.
			home := os.Getenv("HOME")
			if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r","expiresAt":9999999999999}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := stmtErr(t, "CREATE ENV canary SET ANTHROPIC_API_KEY=cpbcanaryvalue AS PLAINTEXT"); err != nil {
				t.Fatal(err)
			}
			writePlaybook(t, root, "failreg", &manifest.Manifest{Env: &manifest.Env{Sets: []string{"canary"}}})
			t.Setenv("SBX_STUB_FAIL", "secret")
			return "failreg", nil
		}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			root := sandboxRoot(t, "pbs")
			work := filepath.Join(t.TempDir(), "work")
			if err := os.MkdirAll(work, 0o755); err != nil {
				t.Fatal(err)
			}
			playbook, args := sc.setup(t, root, work)
			log := stubSbx(t, strings.Fields(os.Getenv("SBX_STUB_LS"))...)
			var runErr error
			stderr := captureStderr(t, func() {
				runErr = runRun(nil, append([]string{"--sandbox", "--workdir", work, playbook}, args...))
			})
			var b strings.Builder
			b.WriteString("== calls\n")
			if data, err := os.ReadFile(log); err == nil {
				for _, line := range strings.SplitAfter(string(data), "\n") {
					b.WriteString(sortAttachEnv(line))
				}
			}
			b.WriteString("== stderr\n" + stderr)
			b.WriteString("== error\n")
			if runErr != nil {
				b.WriteString(runErr.Error() + "\n")
			}
			b.WriteString("== store\n")
			if target, err := os.Readlink(filepath.Join(root, playbook, ".credentials.json")); err == nil {
				b.WriteString(target + "\n")
			}
			got := normalizeGolden(b.String(), map[string]string{
				canon(t, work):               "<WORK>",
				canon(t, filepath.Dir(work)): "<TMP>",
				canon(t, root):               "<ROOT>",
				canon(t, os.Getenv("HOME")):  "<HOME>",
				filepath.Dir(work):           "<TMP>",
				root:                         "<ROOT>",
				os.Getenv("HOME"):            "<HOME>",
			})
			path := filepath.Join("testdata", "sbx-golden", sc.name+".txt")
			if os.Getenv("CPB_UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (CPB_UPDATE_GOLDEN=1 writes it)", err)
			}
			if got != string(want) {
				t.Fatalf("sbx launch differs from %s\n--- want\n%s\n--- got\n%s", path, want, got)
			}
		})
	}
}

// normalizeGolden replaces each path with its token, longest path first so
// a directory never shadows one below it.
func normalizeGolden(s string, paths map[string]string) string {
	keys := make([]string, 0, len(paths))
	for k := range paths {
		if k != "" {
			keys = append(keys, k)
		}
	}
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if len(keys[j]) > len(keys[i]) {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	for _, k := range keys {
		s = strings.ReplaceAll(s, k, paths[k])
	}
	return s
}

// sortAttachEnv sorts the -e K=V pairs of an attach call (`exec -i [-t]
// -e ... NAME bash -lc CMD`) and returns every other line unchanged. The
// values in these scenarios hold no spaces, so a space separates tokens.
func sortAttachEnv(line string) string {
	if !strings.HasPrefix(line, "exec -i ") {
		return line
	}
	f := strings.Split(line, " ")
	i := 2
	if i < len(f) && f[i] == "-t" {
		i++
	}
	start := i
	var pairs []string
	for i+1 < len(f) && f[i] == "-e" {
		pairs = append(pairs, f[i+1])
		i += 2
	}
	sort.Strings(pairs)
	out := append([]string{}, f[:start]...)
	for _, p := range pairs {
		out = append(out, "-e", p)
	}
	return strings.Join(append(out, f[i:]...), " ")
}

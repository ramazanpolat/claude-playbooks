//go:build !windows

// Package e2e exercises claude-playbook as a real subprocess.
//
// The unit tests in internal/auth verify PrepareLaunchEnv's return value. That
// is not the same claim as "the child claude process actually receives this
// environment": between the two sit exec.Command, c.Env assignment, and
// exec.LookPath, none of which the unit tests cross. These tests build the real
// binary, put a stub `claude` on its PATH that records its own environment, and
// assert on what that stub received.
//
// Nothing here needs a valid credential: proving a token is NOT propagated
// requires no working token. Keeping that true takes more than not supplying
// one. On darwin the no-token path shells out to `security find-generic-password`
// BEFORE consulting $HOME, so redirecting HOME does not isolate the run -- the
// stub `security` in shimDir does, and TestSecurityLookupIsIntercepted asserts
// it was reached. This matters because exercising a shared subscription grant
// can rotate its refresh token and strand every other holder of it.
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	tokenEnv       = "CLAUDE_CODE_OAUTH_TOKEN"
	tokenFileEnv   = "CLAUDE_PLAYBOOKS_OAUTH_TOKEN_FILE"
	isolateEnv     = "CLAUDE_PLAYBOOKS_ISOLATE_AUTH"
	dumpEnv        = "CPB_TEST_ENVDUMP"
	securityLogEnv = "CPB_TEST_SECURITY_LOG"
)

// binPath builds claude-playbook once per test binary run and returns its path.
var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cpb-e2e-bin")
	if err != nil {
		panic(err)
	}

	binPath = filepath.Join(dir, "claude-playbook")
	// The module root is the parent of this package.
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		// Without a binary every test below is vacuous, so fail loudly rather
		// than skipping into a green run that proves nothing.
		panic("building claude-playbook for e2e: " + err.Error() + "\n" + string(out))
	}

	code := m.Run()
	// os.Exit does not run deferred functions, so a `defer os.RemoveAll(dir)`
	// here would silently leak a ~13MB binary per run into the system temp
	// directory. Clean up explicitly on every exit path instead.
	os.RemoveAll(dir)
	os.Exit(code)
}

// shimDir writes the stub executables prepended to the binary's PATH.
//
//   - `claude`   records its environment to $CPB_TEST_ENVDUMP. This is what every
//     assertion in this file reads.
//   - `security` is stubbed because EnsureGlobalCredentials shells out to
//     `security find-generic-password -s "Claude Code-credentials"` on darwin,
//     and does so BEFORE consulting $HOME. Redirecting HOME therefore does not
//     isolate the test: without this stub the no-token cases read the
//     developer's real Claude Keychain item and materialize it into the
//     temporary home. Exiting non-zero models "no such Keychain item", which
//     makes the darwin path deterministic and identical to every other OS.
func shimDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	stubs := map[string]string{
		"claude": "#!/bin/sh\nenv > \"$" + dumpEnv + "\"\nexit 0\n",
		// Records each interception so a test can prove the stub is the binary
		// actually reached -- a stub silently bypassed by a PATH change would
		// otherwise re-expose the real Keychain while every test still passed.
		"security": "#!/bin/sh\necho \"$*\" >> \"$" + securityLogEnv + "\"\nexit 1\n",
	}
	for name, script := range stubs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// playbook creates a playbook directory, optionally with isolate_auth set.
func playbook(t *testing.T, root, name string, isolated bool) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# "+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	man := "name = \"" + name + "\"\n"
	if isolated {
		man += "isolate_auth = true\n"
	}
	if err := os.WriteFile(filepath.Join(dir, ".playbook"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

type launch struct {
	// extra environment for the claude-playbook process, "K=V" form.
	env []string
	// args after the global flags, e.g. {"run", "shared"}.
	args []string
	// seedHome populates the temporary HOME before the binary runs. The plan
	// descriptors are read from the global credential store, which lives under
	// HOME; without a hook the redirected HOME is always empty and every
	// injection case would be indistinguishable from "nothing found".
	seedHome func(t *testing.T, home string)
}

// childEnv runs claude-playbook and returns the environment its child received.
func childEnv(t *testing.T, playbooksDir string, l launch) map[string]string {
	t.Helper()
	work := t.TempDir()
	dump := filepath.Join(work, "envdump")

	if l.seedHome != nil {
		l.seedHome(t, work)
	}

	args := append([]string{
		"--playbooks-dir", playbooksDir,
	}, l.args...)

	cmd := exec.Command(binPath, args...)
	// A curated environment, not os.Environ(): an ambient CLAUDE_CODE_OAUTH_TOKEN
	// on the developer's machine would otherwise silently invalidate the
	// no-token cases.
	cmd.Env = append([]string{
		"PATH=" + shimDir(t) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + work,
		dumpEnv + "=" + dump,
		securityLogEnv + "=" + filepath.Join(work, "security.log"),
	}, l.env...)

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("claude-playbook %v: %v\n%s", args, err, out)
	}

	data, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("stub claude was never executed (no env dump): %v", err)
	}
	got := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			got[k] = v
		}
	}
	return got
}

// tokenFile writes a token file and returns the env entry pointing at it.
func tokenFile(t *testing.T, value string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "oauth-token")
	if err := os.WriteFile(p, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return tokenFileEnv + "=" + p
}

// A non-isolated playbook is the whole point of the feature: the token must
// reach the child, or the repeated-/login problem it exists to solve remains.
func TestNonIsolatedReceivesToken(t *testing.T) {
	root := t.TempDir()
	playbook(t, root, "shared", false)

	t.Run("from file", func(t *testing.T) {
		env := childEnv(t, root, launch{
			env:  []string{tokenFile(t, "sk-ant-oat01-FROMFILE")},
			args: []string{"run", "shared"},
		})
		if got := env[tokenEnv]; got != "sk-ant-oat01-FROMFILE" {
			t.Fatalf("child token = %q, want the file's token", got)
		}
	})

	t.Run("already exported wins over the file", func(t *testing.T) {
		env := childEnv(t, root, launch{
			env: []string{
				tokenEnv + "=sk-ant-oat01-FROMENV",
				tokenFile(t, "sk-ant-oat01-FROMFILE"),
			},
			args: []string{"run", "shared"},
		})
		if got := env[tokenEnv]; got != "sk-ant-oat01-FROMENV" {
			t.Fatalf("child token = %q, want the exported token", got)
		}
	})
}

// The isolate_auth contract (SPEC-v4.md) exists so two accounts can run side by
// side. A leaked global token silently defeats it: both playbooks authenticate
// as the same account while appearing isolated.
//
// Every subtest here fails against the pre-fix implementation.
func TestIsolatedPlaybookNeverReceivesToken(t *testing.T) {
	cases := []struct {
		name     string
		manifest bool   // isolation declared in .playbook
		extra    string // isolation declared by environment
		token    []string
	}{
		{
			name:     "manifest isolation, token in file",
			manifest: true,
			token:    nil, // filled below
		},
		{
			name:     "manifest isolation, token exported",
			manifest: true,
		},
		{
			name:  "environment isolation, token exported",
			extra: isolateEnv + "=true",
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			name := "pb"
			playbook(t, root, name, tc.manifest)

			env := []string{}
			if i == 0 {
				env = append(env, tokenFile(t, "sk-ant-oat01-FROMFILE"))
			} else {
				env = append(env, tokenEnv+"=sk-ant-oat01-INHERITED")
			}
			if tc.extra != "" {
				env = append(env, tc.extra)
			}

			got := childEnv(t, root, launch{env: env, args: []string{"run", name}})
			if v, present := got[tokenEnv]; present {
				t.Fatalf("isolated playbook leaked the global token to its child: %s=%s", tokenEnv, v)
			}
		})
	}
}

// Not injecting is not the same as not inheriting. TokenActive returns an empty
// inject value when the token is already exported, relying on the child
// inheriting it -- for an isolated playbook that inheritance IS the leak, so the
// variable must be actively removed. Covered above for `run`; `start` takes a
// separate code path and regressed independently.
func TestStartHonorsIsolation(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "startcfg")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, ".playbook"),
		[]byte("name = \"startcfg\"\nisolate_auth = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := childEnv(t, root, launch{
		env:  []string{tokenEnv + "=sk-ant-oat01-INHERITED"},
		args: []string{"start", cfg},
	})
	if v, present := got[tokenEnv]; present {
		t.Fatalf("start leaked the global token to an isolated config dir: %s=%s", tokenEnv, v)
	}
}

// The suite's own isolation is a testable property, not a comment. On darwin the
// no-token path shells out to `security find-generic-password` BEFORE consulting
// $HOME, so a stub that is ever bypassed -- by a PATH change, a refactor, an
// absolute path in the callee -- would silently start reading the developer's
// real Claude Keychain item while every other test kept passing.
//
// This asserts the interception actually happened rather than assuming it: the
// stub logs each call, and on darwin at least one must be recorded.
func TestSecurityLookupIsIntercepted(t *testing.T) {
	root := t.TempDir()
	playbook(t, root, "shared", false)

	// The no-token path is the one that reaches EnsureGlobalCredentials.
	got := childEnv(t, root, launch{
		env:  []string{tokenFileEnv + "=" + filepath.Join(t.TempDir(), "absent")},
		args: []string{"run", "shared"},
	})

	logPath := got[securityLogEnv]
	if logPath == "" {
		t.Fatalf("%s was not propagated to the child; the stub cannot be verified", securityLogEnv)
	}
	data, err := os.ReadFile(logPath)
	if runtime.GOOS != "darwin" {
		// Only darwin consults the Keychain; elsewhere no call is the correct
		// outcome and an empty log proves nothing is wrong.
		if err == nil && len(data) > 0 {
			t.Fatalf("unexpected security lookup on %s: %s", runtime.GOOS, data)
		}
		return
	}
	if err != nil {
		t.Fatalf("no security lookup was recorded on darwin: the stub was bypassed "+
			"and the real Keychain may have been read: %v", err)
	}
	if !strings.Contains(string(data), "find-generic-password") {
		t.Fatalf("security stub log does not show the expected lookup: %q", data)
	}
}

// The legacy path must be untouched: without a token there is nothing to inject,
// and the variable must not appear at all. This is also the control for the
// tests above -- a guard that always fires would pass those and fail nothing.
func TestNoTokenLeavesVariableUnset(t *testing.T) {
	root := t.TempDir()
	playbook(t, root, "shared", false)

	got := childEnv(t, root, launch{
		env:  []string{tokenFileEnv + "=" + filepath.Join(t.TempDir(), "absent")},
		args: []string{"run", "shared"},
	})
	if v, present := got[tokenEnv]; present {
		t.Fatalf("no token configured, yet child received %s=%s", tokenEnv, v)
	}
}

// CLAUDE_CONFIG_DIR is what makes a playbook a playbook; if the token work ever
// disturbed it, isolation would be moot.
func TestConfigDirBoundToPlaybook(t *testing.T) {
	root := t.TempDir()
	dir := playbook(t, root, "shared", false)

	got := childEnv(t, root, launch{args: []string{"run", "shared"}})
	if got["CLAUDE_CONFIG_DIR"] != dir {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q, want %q", got["CLAUDE_CONFIG_DIR"], dir)
	}
}

// The environment is only half the isolate_auth contract; the other half is that
// a shared credentials symlink is detached. Skipping SyncCredentials broke both
// at once, so both are asserted.
func TestIsolatedDetachesSharedCredentialsSymlink(t *testing.T) {
	root := t.TempDir()
	dir := playbook(t, root, "iso", true)

	global := filepath.Join(t.TempDir(), "global-creds.json")
	if err := os.WriteFile(global, []byte(`{"claudeAiOauth":{"accessToken":"GLOBAL"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".credentials.json")
	if err := os.Symlink(global, link); err != nil {
		t.Fatal(err)
	}

	childEnv(t, root, launch{
		env:  []string{tokenEnv + "=sk-ant-oat01-INHERITED"},
		args: []string{"run", "iso"},
	})

	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("isolated playbook kept its shared-credentials symlink (lstat err = %v)", err)
	}
	// The global file itself must survive -- detaching is not deleting.
	if _, err := os.Stat(global); err != nil {
		t.Fatalf("global credentials were destroyed by detaching: %v", err)
	}
}

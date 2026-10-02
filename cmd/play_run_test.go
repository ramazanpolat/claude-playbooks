package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/play"
)

// playRunFlags sets slice 2's flags for one call, and resets them after.
func playRunFlags(t *testing.T, yes bool, endpoints, secrets, envs []string) {
	t.Helper()
	playYes, playTrustEndpoint, playTrustSecret, playEnvSets = yes, endpoints, secrets, envs
	t.Cleanup(func() { playYes, playTrustEndpoint, playTrustSecret, playEnvSets = false, nil, nil, nil })
}

// stubLog reads what the stub claude recorded: its arguments and env.
func stubLog(t *testing.T, log string) (args string, env map[string]string) {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		return "", nil
	}
	env = map[string]string{}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, "ARGS ") {
			args = strings.TrimPrefix(l, "ARGS ")
			continue
		}
		if k, v, ok := strings.Cut(l, "="); ok {
			env[k] = v
		}
	}
	return args, env
}

func playStores(t *testing.T) []string {
	t.Helper()
	dirs, _ := filepath.Glob(filepath.Join(os.TempDir(), "cpb-play-*"))
	return dirs
}

const plainRecipe = "-- title: Plain\n-- description: A model.\n\nALTER PLAYBOOK SET MODEL 'claude-opus-5-5';\n"

func TestPlayRun(t *testing.T) {
	noSandboxHere(t)
	resetCommandTestState(t)
	aliasTestHome(t)
	t.Setenv("TMPDIR", t.TempDir()) // the throwaway stores land here, and nowhere else
	log := stubClaude(t)
	dir := t.TempDir()
	plain := writeRecipe(t, dir, "plain.cpb", plainRecipe)
	router := writeRecipe(t, dir, "router.cpb", routerRecipe)
	playFlags(t, false, false, false, "")

	// Without a terminal and without --yes: refused, nothing launched,
	// nothing left.
	playRunFlags(t, false, nil, nil, nil)
	var err error
	captureStdout(t, func() { err = runPlay(playCmd, []string{plain}) })
	if err == nil || !strings.Contains(err.Error(), "confirm with --yes") {
		t.Fatalf("no --yes: %v", err)
	}
	if args, _ := stubLog(t, log); args != "" || len(playStores(t)) != 0 {
		t.Fatalf("no --yes launched (%q) or left a store (%v)", args, playStores(t))
	}

	// --yes: the session runs in the throwaway playbook, claude's own
	// arguments after --, and everything is gone afterwards.
	playRunFlags(t, true, nil, nil, nil)
	cmdArgs := []string{plain, "--", "-p", "hi"}
	playCmd.Flags().Parse(cmdArgs)
	var out string
	stderr := captureStderr(t, func() { out = captureStdout(t, func() { err = runPlay(playCmd, playCmd.Flags().Args()) }) })
	args, env := stubLog(t, log)
	if err != nil || args != "-p hi" || !strings.Contains(env["CLAUDE_CONFIG_DIR"], "cpb-play-") || !strings.Contains(env["CLAUDE_CONFIG_DIR"], "/play-plain-") {
		t.Fatalf("--yes: %v, args %q, config %q\n%s", err, args, env["CLAUDE_CONFIG_DIR"], out)
	}
	if !strings.Contains(out, "No sandbox available here (sbx, or OpenShell on Linux): this agent will run on your machine, as you.") || strings.Contains(stderr, "Resume this playbook's session") {
		t.Fatalf("the preview, or a resume line for a removed playbook:\n%s\n%s", out, stderr)
	}
	if _, err := os.Stat(env["CLAUDE_CONFIG_DIR"]); !os.IsNotExist(err) || len(playStores(t)) != 0 {
		t.Fatalf("left behind: %v %v", err, playStores(t))
	}
	if out := mustStmt(t, "SHOW PLAYBOOKS --json"); strings.Contains(out, "play-plain-") {
		t.Fatalf("a playbook in the user's store: %s", out)
	}

	// A moved endpoint: --yes alone refuses and names the flag.
	os.Remove(log)
	playCmd.Flags().Parse([]string{router})
	captureStdout(t, func() { err = runPlay(playCmd, []string{router}) })
	if err == nil || !strings.Contains(err.Error(), "--trust-endpoint router.example.net") {
		t.Fatalf("--yes alone on a moved endpoint: %v", err)
	}
	if args, _ := stubLog(t, log); args != "" {
		t.Fatal("launched without the endpoint confirmed")
	}

	// Confirmed: the key a user attaches with --env follows; the shell's
	// own credentials do not.
	t.Setenv("ANTHROPIC_API_KEY", "shell-key-must-not-follow")
	t.Setenv("MY_SECRET_TOKEN", "shell-secret-must-not-follow")
	mustStmt(t, "CREATE ENV routerkey SET ANTHROPIC_AUTH_TOKEN=router-token AS PLAINTEXT")
	playRunFlags(t, true, []string{"router.example.net"}, nil, []string{"routerkey"})
	captureStdout(t, func() { err = runPlay(playCmd, []string{router}) })
	_, env = stubLog(t, log)
	if err != nil || env["ANTHROPIC_BASE_URL"] != "https://router.example.net/v1" || env["ANTHROPIC_AUTH_TOKEN"] != "router-token" {
		t.Fatalf("endpoint confirmed: %v, env %v", err, env)
	}
	for _, k := range []string{"ANTHROPIC_API_KEY", "MY_SECRET_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s followed the recipe to another host", k)
		}
	}
	if len(playStores(t)) != 0 {
		t.Fatalf("left behind: %v", playStores(t))
	}
}

// On a terminal: the yes, then each typed confirmation, exactly.
func TestPlayConfirmInteractive(t *testing.T) {
	res := play.Check([]byte("ALTER PLAYBOOK\n  SET VAR ANTHROPIC_BASE_URL=https://router.example.net/v1 HTTPS_PROXY=http://p.example:8080\n  SET VAR GH FROM 'keychain:gh';\n"))
	playRunFlags(t, false, nil, nil, nil)
	defer func() { promptIn = os.Stdin }()
	for input, wantErr := range map[string]string{
		"y\nrouter.example.net\np.example\nkeychain:gh\n": "",
		"n\n":                    "not confirmed",
		"y\nrouter.example.ne\n": "router.example.net was not confirmed",
		"y\nrouter.example.net\np.example\nkeychain:other\n": "keychain:gh was not confirmed",
	} {
		promptIn = strings.NewReader(input)
		var err error
		captureStdout(t, func() { err = playConfirm(res, true) })
		if (wantErr == "" && err != nil) || (wantErr != "" && (err == nil || !strings.Contains(err.Error(), wantErr))) {
			t.Errorf("%q: %v, want %q", input, err, wantErr)
		}
	}
	// --yes answers the yes, never the typed ones.
	playRunFlags(t, true, nil, nil, nil)
	promptIn = strings.NewReader("router.example.net\np.example\nkeychain:gh\n")
	var err error
	captureStdout(t, func() { err = playConfirm(res, true) })
	if err != nil {
		t.Fatalf("--yes on a terminal still asks the typed ones: %v", err)
	}
	// Without a terminal: each typed one needs its flag.
	playRunFlags(t, true, []string{"router.example.net", "p.example"}, nil, nil)
	if err := playConfirm(res, false); err == nil || !strings.Contains(err.Error(), "--trust-secret keychain:gh") {
		t.Fatalf("a secret without --trust-secret: %v", err)
	}
	playRunFlags(t, true, []string{"router.example.net", "p.example"}, []string{"keychain:gh"}, nil)
	if err := playConfirm(res, false); err != nil {
		t.Fatalf("all trusted: %v", err)
	}
}

// A throwaway store a killed cpb left behind is swept by the next play:
// only when its cpb is gone and it is old; never one whose cpb is alive.
func TestSweepStalePlays(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	dead := exec.Command("sh", "-c", "exit 0")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	mk := func(name string, pid int, age time.Duration) string {
		d := filepath.Join(os.TempDir(), name)
		os.MkdirAll(d, 0o700)
		m := filepath.Join(d, playMarker)
		os.WriteFile(m, []byte("pid="+strconv.Itoa(pid)+"\n"), 0o600)
		old := time.Now().Add(-age)
		os.Chtimes(m, old, old)
		return d
	}
	stale := mk("cpb-play-stale", dead.Process.Pid, 25*time.Hour)
	alive := mk("cpb-play-alive", os.Getpid(), 25*time.Hour)
	fresh := mk("cpb-play-fresh", dead.Process.Pid, time.Hour)
	// cpb is gone but its session is not: kept (a session can outlive a
	// killed cpb).
	childAlive := filepath.Join(os.TempDir(), "cpb-play-child-alive")
	os.MkdirAll(childAlive, 0o700)
	cm := filepath.Join(childAlive, playMarker)
	os.WriteFile(cm, []byte("pid="+strconv.Itoa(dead.Process.Pid)+" child="+strconv.Itoa(os.Getpid())+"\n"), 0o600)
	oldTime := time.Now().Add(-25 * time.Hour)
	os.Chtimes(cm, oldTime, oldTime)
	unmarked := filepath.Join(os.TempDir(), "cpb-play-unmarked")
	os.MkdirAll(unmarked, 0o700)
	sweepStalePlays()
	for d, gone := range map[string]bool{stale: true, alive: false, fresh: false, unmarked: false, childAlive: false} {
		if _, err := os.Stat(d); os.IsNotExist(err) != gone {
			t.Errorf("%s: gone %v, want %v", filepath.Base(d), os.IsNotExist(err), gone)
		}
	}
}

// ^C (or TERM, HUP) at a prompt cancels the play: the prompt returns, and
// nothing runs.
func TestPlayConfirmCancelled(t *testing.T) {
	res := play.Check([]byte("ALTER PLAYBOOK SET VAR ANTHROPIC_BASE_URL=https://router.example.net/v1;\n"))
	playRunFlags(t, false, nil, nil, nil)
	r, w, _ := os.Pipe() // a prompt that would wait for ever
	defer w.Close()
	promptIn = r
	cancel := make(chan struct{})
	promptCancel = cancel
	defer func() { promptIn, promptCancel = os.Stdin, nil }()
	go func() { time.Sleep(50 * time.Millisecond); close(cancel) }()
	var err error
	captureStdout(t, func() { err = playConfirm(res, true) })
	if err != errPlayCancelled {
		t.Fatalf("a cancelled prompt: %v", err)
	}
}

// noSandboxHere makes no backend available, for a play on this machine.
func noSandboxHere(t *testing.T) {
	t.Helper()
	saved := playSandboxAvailable
	playSandboxAvailable = func(string) error { return os.ErrNotExist }
	t.Cleanup(func() { playSandboxAvailable = saved })
}

func playSandboxFlags(t *testing.T, flag string, off bool) {
	t.Helper()
	playSandboxFlag, playNoSandbox = flag, off
	t.Cleanup(func() { playSandboxFlag, playNoSandbox = "", false })
}

// Where a play runs: a sandbox by default where a backend is available;
// --no-sandbox said plainly; create-with: SANDBOX refused with none; a
// recipe with secret references refused sandboxed (a sandboxed launch
// cannot resolve them yet) rather than quietly moved to the host. With no
// backend at all it runs on the host like any recipe, the preview says so,
// and each reference is still typed to confirm it.
func TestChoosePlaySandbox(t *testing.T) {
	plain := play.Check([]byte(plainRecipe))
	wants := play.Check([]byte("-- create-with: SANDBOX\n\nALTER PLAYBOOK SET MODEL 'm';\n"))
	refs := play.Check([]byte("ALTER PLAYBOOK SET VAR GH FROM 'keychain:gh';\n"))
	saved := playSandboxAvailable
	defer func() { playSandboxAvailable = saved }()
	avail := map[string]bool{}
	playSandboxAvailable = func(k string) error {
		if avail[k] {
			return nil
		}
		return os.ErrNotExist
	}
	for _, c := range []struct {
		name      string
		avail     []string
		flag      string
		off       bool
		res       *play.Result
		backend   string
		note, err string
	}{
		{"sbx first", []string{"sbx", "openshell"}, "", false, plain, "sbx", "Sandboxed (sbx)", ""},
		{"openshell when no sbx", []string{"openshell"}, "", false, plain, "openshell", "Sandboxed (openshell)", ""},
		{"picked", []string{"sbx", "openshell"}, "openshell", false, plain, "openshell", "Sandboxed (openshell)", ""},
		{"picked, missing", []string{"sbx"}, "openshell", false, plain, "", "", "--sandbox=openshell"},
		{"none", nil, "", false, plain, "", "No sandbox available here", ""},
		{"--no-sandbox", []string{"sbx"}, "", true, plain, "", "Sandbox off (--no-sandbox)", ""},
		{"both flags", []string{"sbx"}, "sbx", true, plain, "", "", "together"},
		{"wants, none", nil, "", false, wants, "", "", "create-with: SANDBOX"},
		{"wants, --no-sandbox", nil, "", true, wants, "", "although the recipe asks for one", ""},
		{"refs, sandboxed", []string{"sbx"}, "", false, refs, "", "", "keychain:gh"},
		{"refs, --no-sandbox", []string{"sbx"}, "", true, refs, "", "Sandbox off", ""},
		{"refs, none here", nil, "", false, refs, "", "No sandbox available here", ""},
	} {
		avail = map[string]bool{}
		for _, k := range c.avail {
			avail[k] = true
		}
		playSandboxFlags(t, c.flag, c.off)
		sb, err := choosePlaySandbox(c.res)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: %v, want an error with %q", c.name, err, c.err)
			}
			continue
		}
		if err != nil || sb.Backend != c.backend || !strings.Contains(sb.Note, c.note) {
			t.Errorf("%s: %+v %v, want %q %q", c.name, sb, err, c.backend, c.note)
		}
	}
}

// A sandboxed play: the sandbox is created only after every confirmation,
// the session attaches to it, and it is removed with the store. A recipe
// that moves the endpoint and is not confirmed never reaches sbx at all:
// the proxy would inject a key for that host (root, 2026-10-02).
func TestPlayRunSandboxed(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	t.Setenv("TMPDIR", t.TempDir())
	log := stubSbx(t)
	dir := t.TempDir()
	plain := writeRecipe(t, dir, "plain.cpb", plainRecipe)
	router := writeRecipe(t, dir, "router.cpb", routerRecipe)
	playFlags(t, false, false, false, "")
	playSandboxFlags(t, "", false)

	// Unconfirmed endpoint: refused, and sbx never ran.
	playRunFlags(t, true, nil, nil, nil)
	var err error
	captureStdout(t, func() { err = runPlay(playCmd, []string{router}) })
	if err == nil || !strings.Contains(err.Error(), "--trust-endpoint router.example.net") {
		t.Fatalf("unconfirmed endpoint: %v", err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("sbx ran before the endpoint was confirmed: %q", sbxCalls(t, log))
	}

	// A plain recipe, sandboxed by default: create, attach, remove.
	out := captureStdout(t, func() { err = runPlay(playCmd, []string{plain}) })
	if err != nil || !strings.Contains(out, "Sandboxed (sbx)") {
		t.Fatalf("sandboxed: %v\n%s", err, out)
	}
	calls := strings.Join(sbxCalls(t, log), "\n")
	create, attach, rm := strings.Index(calls, "create --name cpb-play-plain-"), strings.Index(calls, "exec -i"), strings.Index(calls, "rm -f cpb-play-plain-")
	if create < 0 || attach < create || rm < attach {
		t.Fatalf("create, attach, remove, in order: %q", calls)
	}
	if len(playStores(t)) != 0 {
		t.Fatalf("left behind: %v", playStores(t))
	}
}

package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

var runCmd = &cobra.Command{
	Use:                "run <name> [claude-flags...]",
	Short:              "Run Claude Code with a playbook",
	DisableFlagParsing: true,
	ValidArgsFunction:  autocompletePlaybookNames,
	RunE:               runRun,
}

// errConfigDirOverrideSandbox refuses a sandboxed launch that also carries a
// caller-supplied config directory. A sandbox mounts the config directory, and
// a directory whose playbook content is reached through symlinks (the shape a
// caller provisioning its own necessarily produces) dangles inside, because
// the backend mounts directories: the same failure a shared login's symlinked
// credentials have. Half-supporting that is worse than saying so.
func errConfigDirOverrideSandbox() error {
	return fmt.Errorf("%s and a sandboxed launch together are not supported: a sandbox mounts the config directory, and content reached through symlinks dangles inside it. Launch on the host, or unset %s",
		config.ConfigDirOverrideEnv, config.ConfigDirOverrideEnv)
}

// refuseUnreadableManifest refuses a launch of dir while a manifest that
// would govern it, or one on the way up, cannot be read. It may ask for an
// isolated login or a sandbox, and launching as if it said nothing would
// hand over the shared login, or run on the host.
func refuseUnreadableManifest(dir string) error {
	if _, _, err := manifest.NearestPath(dir); err != nil {
		return fmt.Errorf("%w. cpb does not launch over a manifest it cannot read: it may ask for an isolated login or a sandbox", err)
	}
	return nil
}

// onLaunch, when set, is told the session's process once it starts: cpb
// play forwards a SIGTERM or SIGHUP aimed at cpb to it, and names it in its
// sweep marker. Start then Wait is what Run does.
var onLaunch func(*os.Process)

// runAttached runs a session's command, telling onLaunch its process: a
// host launch, or a sandbox backend's attach.
func runAttached(c *exec.Cmd) error { return runRecorded(c, nil, nil) }

// runRecorded is runAttached for a host launch: rec, when given, is written
// for the process once it starts and removed when it ends, so SHOW SESSIONS
// can tell the session's account (launch_record.go). A sandbox's attach
// records nothing: its sessions are not on the host.
func runRecorded(c *exec.Cmd, rec *launchRecord, watch *failoverWatch) error {
	if err := c.Start(); err != nil {
		return err
	}
	// Without a watcher to see the next claude's session file, the launch's
	// start ends the catch of a switch's Ctrl-C.
	if watch == nil {
		releaseHandover()
	}
	// A token run stays live for as long as this session does, even if
	// cpb is killed first (internal/auth/own_login.go).
	auth.SessionStarted(c.Process.Pid)
	if rec != nil {
		defer rec.record(launchRecordsRoot(), c.Process.Pid)()
	}
	if onLaunch != nil {
		onLaunch(c.Process)
	}
	if watch == nil {
		return c.Wait()
	}
	// Account failover (failover.go): the watcher may end this claude to
	// resume its session on another account; then the launch reports the
	// switch instead of claude's exit.
	done := make(chan struct{})
	result := make(chan *failoverSwitch, 1)
	go func() { result <- watch.watch(done, c.Process) }()
	err := c.Wait()
	close(done)
	if sw := <-result; sw != nil {
		return sw
	}
	return err
}

// failoverRelaunch is what runRunOnce returns when its session moved to
// another account: the cpb run command line that resumes it there. runRun
// runs it after the launch that ended has returned, its clean-up done.
type failoverRelaunch struct {
	args []string
	sw   *failoverSwitch
	// back resumes the session on the account it came from, for when the
	// launch on the new one is refused before claude starts.
	back []string
}

func (r *failoverRelaunch) Error() string { return r.sw.Error() }

// runRun launches a playbook, and launches it again on another account
// each time its session moves there (account failover). The resume runs
// from the session's own folder. A launch on the new account that never
// ran the session (refused, or its secret helper failing before claude
// starts: the transcript is as it was) resumes the session on the account
// it came from instead; when that does not run it either, cpb says nothing
// runs the session and how to resume it by hand.
func runRun(cmd *cobra.Command, args []string) error {
	// A Ctrl-C between two claudes of a switch is caught (handoverSigs in
	// failover.go); cpb run stops catching it when it returns, whatever
	// happened.
	defer releaseHandover()
	err := runRunOnce(cmd, args)
	for {
		var rl *failoverRelaunch
		if !errors.As(err, &rl) {
			return err
		}
		sw := rl.sw
		fmt.Fprintf(os.Stderr, "cpb: account %s hit its %s limit (resets %s); resuming this session on %s\n",
			sw.From, windowWords(sw.Window), sw.resetsAt.Local().Format("Mon 2 Jan 15:04"), sw.To)
		pinPlaybooksDir()
		if sw.folder != "" {
			_ = os.Chdir(sw.folder)
		}
		failoverHandover, failoverBack = sw, false
		handoverClaudeStarted.Store(false)
		err = runRunOnce(cmd, rl.args)
		var again *failoverRelaunch
		if !errors.As(err, &again) && sw.notRun(err) {
			sw.Outcome = "failed"
			fmt.Fprint(os.Stderr, relaunchFailedLine(sw, err))
			failoverBack = true
			handoverClaudeStarted.Store(false)
			err = runRunOnce(cmd, rl.back)
			// A way back whose claude ran and then ended with an error
			// reported as any launch does; one that never ran is stranded.
			if !errors.As(err, &again) && sw.notRun(err) && !handoverClaudeStarted.Load() {
				sw.stranded = true
				printFailoverSummary()
				line, filled := resumeByHand(sw.folder, rl.back)
				how := "to resume it by hand:"
				if filled {
					how = "to resume it by hand, replace each <...> with its value and remove the leading #, then run:"
				}
				fmt.Fprintf(os.Stderr, "cpb: could not resume it on %s either (%s); %s\n  %s\n", sw.From, printable(firstLine(err.Error())), how, line)
			}
		}
		failoverHandover, failoverBack = nil, false
	}
}

// relaunchFailedLine is what cpb says when the launch on the new account
// did not run the session, its error shown with control characters as '?'.
func relaunchFailedLine(sw *failoverSwitch, err error) string {
	return fmt.Sprintf("cpb: could not resume this session on %s (%s); resuming it on %s instead\n", sw.To, printable(firstLine(err.Error())), sw.From)
}

// pinPlaybooksDir makes a relative playbooks root absolute before a
// relaunch changes folder, so the relaunch reads the same root.
func pinPlaybooksDir() {
	if v := os.Getenv(config.PlaybooksDirEnv); v != "" && !filepath.IsAbs(v) {
		_ = os.Setenv(config.PlaybooksDirEnv, absPath(v))
	}
	if root := config.ResolvePlaybooksDir(); !filepath.IsAbs(root) {
		config.PlaybooksDir = absPath(root)
	}
}

// notShown stands for a value the by-hand line does not show.
const notShown = "<a value with control characters, not shown>"

// mustHide reports whether s holds a character a terminal does not show as
// written, other than a newline or a tab (which single quotes keep).
func mustHide(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return hidden(r) && r != '\n' && r != '\t' }) >= 0
}

// resumeByHand is the command line that resumes a session with args from
// folder, quoted for any POSIX shell, and whether a value in it is not
// shown: an --env value and an inline --settings JSON (either may hold a
// credential), and a value with a character a terminal does not show as
// written. A flag keeps its name; its value becomes a placeholder. A line
// with a placeholder starts with "# " and stays one line (a value holding
// a newline is then not shown either), so pasted as it is it runs nothing.
func resumeByHand(folder string, args []string) (string, bool) {
	line, filled := byHandLine(folder, args, false)
	if filled {
		// A marked line stays one line: the "# " covers only its first, so
		// a value's newline would carry the rest to the shell.
		line, _ = byHandLine(folder, args, true)
		line = "# " + line
	}
	return line, filled
}

// byHandLine is resumeByHand's line, unmarked, and whether a value in it
// is not shown; with oneLine, a value holding a newline is not shown
// either.
func byHandLine(folder string, args []string, oneLine bool) (string, bool) {
	filled := false
	hide := func(s string) bool { return mustHide(s) || oneLine && strings.Contains(s, "\n") }
	word := func(s string) string {
		if hide(s) {
			filled = true
			return shellWord(notShown)
		}
		return shellWord(s)
	}
	isJSON := func(v string) bool { return strings.HasPrefix(strings.TrimSpace(v), "{") }
	words := []string{"cpb", "run"}
	if config.PlaybooksDir != "" || os.Getenv(config.PlaybooksDirEnv) != "" {
		words = append(words, "--playbooks-dir", word(config.ResolvePlaybooksDir()))
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, inline := strings.Cut(a, "=")
		switch {
		case inline && name == "--env":
			k, _, _ := strings.Cut(value, "=")
			if hide(k) {
				k = notShown
			}
			words = append(words, shellWord("--env="+k+"=<your value>"))
			filled = true
		case inline && name == "--settings" && isJSON(value):
			words = append(words, shellWord("--settings=<your settings JSON>"))
			filled = true
		case a == "--settings" && i+1 < len(args) && isJSON(args[i+1]):
			words = append(words, a, shellWord("<your settings JSON>"))
			filled = true
			i++
		case inline && strings.HasPrefix(name, "--") && hide(value):
			words = append(words, shellWord(name+"="+notShown))
			filled = true
		default:
			words = append(words, word(a))
		}
	}
	line := strings.Join(words, " ")
	if folder != "" {
		line = "cd " + word(folder) + " && " + line
	}
	return line, filled
}

// shellWord is s as one word for any POSIX shell: as it is when it is
// plain, else in single quotes, where a newline or a tab stands as itself.
// A value with any other character a terminal does not show as written is
// not shown (resumeByHand marks the line).
func shellWord(s string) string {
	plain := s != "" && s[0] != '=' && s[0] != '~' && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@%+,", r))
	}) < 0
	if plain {
		return s
	}
	if mustHide(s) {
		return "'" + notShown + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fileSize is a file's size, -1 when it cannot be read.
func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return fi.Size()
}

func runRunOnce(cmd *cobra.Command, args []string) error {
	original := args
	rest, err := takePlaybooksDirArg(args)
	if err != nil {
		return err
	}
	// Launch flags may precede the name (`run --env K=V name`) or follow it
	// directly (`name --env K=V ...`, which is what a launcher passes). The
	// --sandbox family is scanned in the same leading runs.
	var sopts sandboxOpts
	var failoverFlag, noFailoverFlag bool
	failoverFlags := map[string]*bool{"--failover": &failoverFlag, "--no-failover": &noFailoverFlag}
	rest, tokens, err := takeRunFlags(rest, &sopts, failoverFlags)
	if err != nil {
		return err
	}
	// A --help at the NAME position prints usage, whether or not launch
	// flags preceded it; after the name it belongs to claude.
	if restRequestsHelp(rest) {
		fmt.Println("Usage: cpb run " + runFlagsUsage + " <name> [claude-flags...]")
		fmt.Println()
		fmt.Println("Runs Claude Code with the named playbook.")
		fmt.Println("Launch flags add one-off environment layers on top of the playbook's [env]")
		fmt.Println("block, in order, for this launch only; they go before the name or right after it.")
		fmt.Println("  --account NAME       run on this account (one the playbook may use)")
		fmt.Println("  --env-set NAME   layer an existing env set")
		fmt.Println("  --env KEY=VALUE      set one variable")
		fmt.Println("  --block KEY          remove one variable (CLAUDE_CODE_OAUTH_TOKEN: use the stored login)")
		fmt.Println("  --env-file PATH      layer a dotenv-style file of KEY=VALUE lines")
		fmt.Println("  --failover           at a usage limit, resume the session on the next account of failover_accounts")
		fmt.Println("  --no-failover        not this launch, whatever the playbook or DEFAULTS say")
		fmt.Println("Sandbox flags (experimental) run the playbook inside a sandbox (backend sbx, Docker Sandboxes).")
		fmt.Println("Experimental: the sandbox works and is supported in v4, but its flags and sandbox.* keys may change or move to a separate runner without the usual notice.")
		fmt.Println("  --sandbox[=BACKEND]  launch in the playbook's sandbox cpb-<name> (created on first use)")
		fmt.Println("  --no-sandbox         launch on the host although the manifest says [sandbox] always = true")
		fmt.Println("  --sandbox-host U@H   run the sandboxed launch on that machine over ssh (cpb and the playbook installed there)")
		fmt.Println("  --sandbox-fresh      remove and recreate that sandbox first")
		fmt.Println("  --clone              at creation, work on a private clone of the working directory's repo")
		fmt.Println("  --workdir PATH       working directory to mount and enter (default: current directory)")
		fmt.Println("  --mount PATH[:ro]    extra host path to mount (repeatable)")
		fmt.Println("Everything else after the name is forwarded directly to claude.")
		return nil
	}

	if len(rest) == 0 {
		return fmt.Errorf("playbook name required\nUsage: cpb run " + runFlagsUsage + " <name> [claude-flags...]")
	}
	name := rest[0]
	claudeArgs, more, err := takeRunFlags(rest[1:], &sopts, failoverFlags)
	if err != nil {
		return err
	}
	tokens = append(tokens, more...)
	if failoverFlag && noFailoverFlag {
		return fmt.Errorf("--failover and --no-failover together: pick one")
	}

	// A caller-supplied config directory, resolved before any sandbox
	// decision so the refusal below happens whatever shape the sandbox
	// request takes. Absent: every launch behaves exactly as before.
	overrideDir, override, err := config.ResolveConfigDirOverride()
	if err != nil {
		return err
	}

	// An explicit --sandbox-host (before or after the name) runs the whole
	// launch on that host: the playbook need not be registered here, and
	// no launch flag is evaluated here (env files stay unread).
	if sopts.host != "" {
		if sopts.disabled {
			return fmt.Errorf("--sandbox-host and --no-sandbox together: pick one")
		}
		if failoverFlag {
			return errFailoverSandbox
		}
		if override {
			return errConfigDirOverrideSandbox()
		}
		return forwardToSandboxHost(sopts.host, "run", original, &sopts, tokens, nil, name, claudeArgs)
	}
	playbooksDirResolved := config.ResolvePlaybooksDir()

	pb, err := playbook.Find(playbooksDirResolved, name)
	if err != nil {
		return err
	}
	if pb == nil {
		return fmt.Errorf("unknown playbook %q. `cpb SHOW PLAYBOOKS` lists them", name)
	}

	// Before the sandbox decision: a manifest on the way up may ask for
	// one, or for an isolated login.
	guardDir := pb.Path
	if override {
		guardDir = overrideDir
	}
	if err := refuseUnreadableManifest(guardDir); err != nil {
		return err
	}

	var sbm *manifest.Sandbox
	if pb.Manifest != nil {
		sbm = pb.Manifest.Sandbox
	}
	sandboxed, backend, err := resolveSandbox(sbm, &sopts, fmt.Sprintf("playbook %q", pb.Name))
	if err != nil {
		return err
	}
	if sandboxed {
		if override {
			return errConfigDirOverrideSandbox()
		}
		if failoverFlag {
			return errFailoverSandbox
		}
		resumeNote(claudeArgs)
		if host := sandboxHost(sbm, &sopts); host != "" {
			return forwardToSandboxHost(host, "run", original, &sopts, tokens, nil, name, claudeArgs)
		}
		// Only now is the launch known to be local: evaluate the launch
		// flags (env files are read here and nowhere earlier).
		layers, err := launchLayers(tokens)
		if err != nil {
			return err
		}
		// The registry's spelling of the config directory, made absolute
		// so a relative --playbooks-dir cannot leak a relative
		// CLAUDE_CONFIG_DIR (or ANTHROPIC_CONFIG_DIR) into a sandbox whose
		// working directory is elsewhere.
		configPath, err := filepath.Abs(pb.Path)
		if err != nil {
			return err
		}
		// The account, as for a host launch: rendered inside the sandbox.
		acct, err := planSandboxAccount(configPath, pb.Name, layers, sbm, sopts, claudeArgs)
		if err != nil {
			return err
		}
		layers = acct.layers
		if eff, perr := auth.EffectiveBlock(pb.Path, layers); perr == nil {
			if err := refuseRefsInSandbox(eff, fmt.Sprintf("playbook %q", pb.Name), acct.rendered.Headers); err != nil {
				return err
			}
		}
		rootPath := pb.RootPath
		if rootPath == "" {
			rootPath = pb.Path
		}
		if rootPath, err = filepath.Abs(rootPath); err != nil {
			return err
		}
		if err := acct.prepare(); err != nil {
			return err
		}
		_, err = runSandboxed(sandboxTarget{
			label: fmt.Sprintf("playbook %q", pb.Name), name: sandboxName(pb.Name),
			configPath: configPath, rootPath: rootPath, manifest: sbm, backend: backend,
			noLoginAccount: acct.noLoginAccount(),
			noCredential:   acct.noCredential(),
			account:        acct.sel.Name,
		}, layers, acct.claudeArgs(claudeArgs), sopts)
		return err
	}

	// The pilot's input is validated before the machine is inspected.
	//
	// launchLayers is what rejects a --env that is not KEY=VALUE, a reserved
	// key, an unreadable or malformed --env-file, and an unknown profile. With
	// the claude lookup first, all of those reported "'claude' command not
	// found" instead: a pilot who mistyped a flag was sent to install an agent
	// they may already have. Only the token scan above survived, so of six ways
	// to get the flags wrong, one named itself.
	//
	// Order is the whole fix, and it is safe in both directions: launchLayers
	// only validates and reads (an --env-file), while everything that MUTATES
	// -- credential quarantine and sync in PrepareLaunchEnv below -- still
	// happens after the lookup, so a machine without an agent is never touched.
	// It also stays after the remote-forward decision, which is what keeps a
	// --sandbox-host launch from reading local env files at all.
	layers, err := launchLayers(tokens)
	if err != nil {
		return err
	}

	// The config directory this launch binds: the playbook's install
	// directory, or the one the caller supplied. Everything downstream --
	// the authentication decision, credential sync, quarantine, the
	// governing manifest lookup -- then operates on it uniformly, with no
	// special case for where it came from.
	configDir := pb.Path
	if override {
		configDir = overrideDir
	}

	// The sixth way to get the flags wrong: a profile that does not resolve.
	// Its refusal lives inside PrepareLaunchEnv, which cannot move above the
	// lookup because it mutates credentials -- so resolve the block here,
	// purely, and let the input name itself like the other five. EffectiveBlock
	// is the same resolution PrepareLaunchEnv performs, reading only; doing it
	// twice costs a few file reads and cannot diverge, being one function.
	if _, perr := auth.EffectiveBlock(configDir, layers); errors.Is(perr, envset.ErrSet) {
		return perr
	}
	// The account: where requests go and how they authenticate, derived
	// from it as the launch's last layer. Anything else that would route or
	// authenticate the launch, and a login it cannot keep out, refuses here,
	// before anything is mutated.
	wd, _ := os.Getwd()
	// The --account this launch names: the commands cpb suggests keep it.
	namedAccount := accountFlag(layers)
	acct, err := planAccount(configDir, pb.Name, layers, wd, claudeArgs)
	if err != nil {
		return err
	}
	if err := liveLoginRefusal(pb.Name, configDir, acct); err != nil {
		return err
	}
	layers = acct.layers
	eff, perr := auth.EffectiveBlock(configDir, layers)
	if perr != nil {
		return perr
	}
	// Secret references exec the launch through the helper; with none
	// configured the launch is refused here, before anything is mutated.
	plan, err := planLaunch(eff)
	if err != nil {
		return err
	}
	if err := plan.withHeaders(acct.rendered.Headers); err != nil {
		return err
	}
	// A token over an isolated playbook's own login: refused on macOS,
	// before anything is mutated (internal/auth/own_login.go).
	if err := auth.OwnLoginRefusal(configDir, eff, pb.Name); err != nil {
		return err
	}

	// A --resume or --continue of a session still live elsewhere is refused
	// before anything is mutated (resume.go).
	sd := playbookSessionDir(pb)
	if override {
		sd = sessionDir{label: configDir, path: configDir}
	}
	sd.account = namedAccount
	if err := guardResume(sd, claudeArgs); err != nil {
		return err
	}
	// Account failover: decided before anything is changed, so a refusal
	// (a setting that cannot be read, a flag a resume would not keep) costs
	// nothing.
	watch, err := failoverWatchFor(failoverWatchInput{
		pb: pb, configDir: configDir, tokens: tokens, claudeArgs: claudeArgs, wd: wd, from: acct.sel.Name,
		flagOn: failoverFlag, flagOff: noFailoverFlag, skip: override || playSessionRunning,
	})
	if err != nil {
		return err
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("'claude' command not found. Install Claude Code first: https://claude.ai/download")
	}
	if err := acct.prepare(); err != nil {
		return err
	}
	launchEnv, syncErr := auth.PrepareLaunchEnvFor(configDir, layers, acct.noLoginAccount())
	if acct.noCredential() {
		launchEnv = auth.WithNoCredential(launchEnv)
	}
	// A token run may have set the playbook's own login aside: it comes
	// back when the launch returns, whatever the outcome.
	defer auth.EndLaunch(configDir)
	if errors.Is(syncErr, envset.ErrSet) || errors.Is(syncErr, auth.ErrOwnLogin) {
		// Missing, unreadable, or invalid profile: launching with a silently
		// dropped layer could send traffic to the wrong endpoint with the
		// wrong credentials -- refuse, do not warn. A token launch over an
		// isolated playbook's own login is refused the same way.
		return syncErr
	}
	if syncErr != nil {
		// Neutral wording: PrepareLaunchEnv may have been syncing credentials,
		// account metadata, or detaching for an isolated playbook. Naming
		// credentials specifically sent pilots after credential files and symlinks
		// for failures in paths where credential syncing was deliberately skipped.
		fmt.Fprintf(os.Stderr, "Warning: failed to prepare authentication state: %v\n", syncErr)
	}

	rec := newLaunchRecord(pb.Name, configDir, acct.sel)
	rec.Labels = launchLabelsOf(tokens)
	if !override {
		acct.statusline = statuslineStepFor(config.ResolvePlaybooksDir(), configDir, wd, rec.LaunchID, claudeArgs).wrapped
	}
	c := plan.command(claudePath, acct.claudeArgs(claudeArgs), withPlaybooksRoot(launchEnv))
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	// The exit line names the command that resumes this session in this
	// playbook; a caller-supplied config dir is not the playbook's, so
	// that launch gets none.
	exitLine := !override && !playSessionRunning && wantsResumeLine(claudeArgs)
	var before map[string]time.Time
	if exitLine {
		before = transcriptStamps(sd.path)
	}
	if watch != nil {
		rec.Switches = activeEpisode.switchesCopy()
	}
	err = runRecorded(c, rec, watch)
	var sw *failoverSwitch
	if errors.As(err, &sw) {
		activeEpisode.mu.Lock()
		activeEpisode.switches = append(activeEpisode.switches, sw)
		activeEpisode.mu.Unlock()
		var switches []string
		if failoverFlag {
			switches = append(switches, "--failover")
		}
		if sopts.disabled {
			switches = append(switches, "--no-sandbox")
		}
		return &failoverRelaunch{args: relaunchArgs(switches, tokens, pb.Name, claudeArgs, sw), sw: sw, back: relaunchArgs(switches, tokens, pb.Name, claudeArgs,
			&failoverSwitch{From: sw.To, To: sw.From, sessionID: sw.sessionID, model: sw.model, folder: sw.folder, noPrompt: true})}
	}
	err = preserveExitCode(err)
	if err != nil {
		fmt.Fprint(os.Stderr, unresolvedAccountLine(acct, plan))
	}
	if h := failoverHandover; h != nil && h.notRun(err) && !(failoverBack && handoverClaudeStarted.Load()) {
		// The launch never ran the session: runRun goes on (back to the
		// account it came from, or says nothing runs it) and reports.
		return err
	}
	printFailoverSummary()
	if exitLine {
		printResumeLine(sd, before)
	}
	return err
}

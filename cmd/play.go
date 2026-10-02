package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/play"
)

// cpb play <ref> (v3.28.0): try someone else's playbook. This slice fetches
// and checks the recipe (--check) and plans it against a throwaway store
// (--dry-run, --json); running it arrives with the next slice. Design:
// task claude-playbooks-cli, design-cpb-play-2026-10-01-21_58.md.
var playCmd = &cobra.Command{
	Use:   "play <ref> | play --update <name>",
	Short: "Try someone else's playbook: preview it, confirm, run it in a throwaway playbook",
	Long: `play fetches a recipe once, checks it, and shows exactly what it would do.

<ref> is a template name (reviewer), an https URL, github:<owner>/<repo>/<path>.cpb@<ref>,
or a local file (./x.cpb). A played recipe changes nothing on your machine but
the playbook play makes: no env sets, no DEFAULTS, no plaintext secrets, and
never your pilot profile.

It runs in a sandbox where one is available (sbx, or OpenShell on Linux), and
says so when none is; --no-sandbox runs it on this machine, as you. A recipe
whose header asks for a sandbox (-- create-with: SANDBOX) is refused where none
is available. A recipe that reads a secret reference cannot run sandboxed
yet (a sandboxed session cannot resolve references): where a sandbox is
available it is refused unless you pass --no-sandbox.

--keep keeps it instead, as a playbook in your store (--as names it), with a
launcher and a [play] record of where it came from; no session runs. Your
DEFAULTS apply to it like to any playbook, and the preview names them.
cpb play --update <name> fetches the recorded ref again: the same bytes
change nothing; others show the diff and the full preview again.`,
	Args: func(cmd *cobra.Command, args []string) error {
		if playUpdate != "" {
			switch {
			case len(args) > 0:
				return errors.New("--update <name> takes no <ref>: it fetches the one the playbook recorded")
			case playKeep || playAs != "" || playCheck:
				return errors.New("--update cannot be combined with --keep, --as or --check")
			case cmd.Flags().Changed("sandbox") || playNoSandbox:
				return errors.New("--update keeps the playbook's sandbox setting: ALTER it, or keep the recipe again")
			}
			return nil
		}
		if playAs != "" && !playKeep {
			return errors.New("--as names a kept playbook: add --keep")
		}
		if playKeep && playCheck {
			return errors.New("--check runs nothing to keep: drop --keep, or use --keep --dry-run")
		}
		if playKeep && cmd.ArgsLenAtDash() >= 0 {
			return errors.New("--keep runs no session, so there are no claude arguments: launch the kept playbook with them")
		}
		// <ref>, then claude's own arguments after --.
		at := cmd.ArgsLenAtDash()
		if len(args) == 0 || at == 0 {
			return errors.New("play what? cpb play <ref> [-- <claude arguments>]")
		}
		if (at < 0 && len(args) > 1) || at > 1 {
			return errors.New("one <ref>; claude's own arguments go after --")
		}
		return nil
	},
	RunE: runPlay,
}

var (
	playCheck  bool
	playDryRun bool
	playJSONF  bool
	playSHA256 string
)

func init() {
	playCmd.Flags().BoolVar(&playCheck, "check", false, "fetch and check the recipe only: what it may not hold, and its risks")
	playCmd.Flags().BoolVar(&playDryRun, "dry-run", false, "show the plan against a throwaway playbook, and run nothing")
	playCmd.Flags().BoolVar(&playJSONF, "json", false, "with --dry-run or --check: the plan as JSON")
	playCmd.Flags().StringVar(&playSHA256, "sha256", "", "refuse any recipe whose sha256 is not this")
	playCmd.Flags().BoolVar(&playYes, "yes", false, "answer the yes, for scripts; never confirms an endpoint, a proxy, TLS or a secret")
	playCmd.Flags().StringArrayVar(&playTrustEndpoint, "trust-endpoint", nil, "without a terminal: confirm a model endpoint or proxy host (or TLS); repeatable")
	playCmd.Flags().StringArrayVar(&playTrustSecret, "trust-secret", nil, "without a terminal: confirm a secret reference; repeatable")
	playCmd.Flags().StringArrayVar(&playEnvSets, "env", nil, "attach one of your env sets to the played playbook (a key for a moved endpoint); repeatable")
	playCmd.Flags().StringVar(&playSandboxFlag, "sandbox", "", "run sandboxed (the default where a backend is available); =sbx or =openshell picks one")
	playCmd.Flags().Lookup("sandbox").NoOptDefVal = "auto"
	playCmd.Flags().BoolVar(&playNoSandbox, "no-sandbox", false, "run on this machine, as you; the preview says so")
	playCmd.Flags().BoolVar(&playKeep, "keep", false, "keep it as a playbook in your store, with a [play] record, instead of running it")
	playCmd.Flags().StringVar(&playAs, "as", "", "with --keep: the kept playbook's name (default: the recipe's)")
	playCmd.Flags().StringVar(&playUpdate, "update", "", "fetch a kept playbook's recorded recipe again, and update it after the preview")
	rootCmd.AddCommand(playCmd)
}

// playJSON is the "play" block a played recipe's --json report adds to the
// APPLY --dry-run --json object (a new top-level field: additive).
type playJSON struct {
	Ref      string         `json:"ref"`
	Kind     string         `json:"kind"`
	URL      string         `json:"url,omitempty"`
	Path     string         `json:"path,omitempty"`
	SHA256   string         `json:"sha256"`
	Bytes    int            `json:"bytes"`
	Pinned   bool           `json:"pinned"`
	Note     string         `json:"note,omitempty"`
	Header   playHeaderJSON `json:"header"`
	Playbook string         `json:"playbook"`
	Endpoint string         `json:"endpoint"`
	Refused  []play.Refusal `json:"refused"`
	Risks    []play.Risk    `json:"risks"`
	// Sandbox: where it would run (slice 3); null in --check.
	Sandbox *playSandbox `json:"sandbox"`
	// Keep: --keep's plan, against the user's own store (slice 4).
	Keep bool `json:"keep,omitempty"`
	// Update: --update's, against the kept playbook (slice 4).
	Update *playUpdateJSON `json:"update,omitempty"`
}

type playHeaderJSON struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Needs       string `json:"needs"`
	CreateWith  string `json:"create_with"`
	MinCPB      string `json:"min_cpb"`
}

// fetchRecipe resolves ref and reads it once.
func fetchRecipe(ref string) (*play.Source, *play.Recipe, error) {
	src, err := play.Resolve(ref, Version)
	if err != nil {
		return nil, nil, err
	}
	var rec *play.Recipe
	if src.Kind == play.KindLocal {
		rec, err = play.ReadLocal(src.Path)
	} else {
		rec, err = play.Fetch(context.Background(), play.NewClient(), src.URL, "cpb/"+Version+" (play)")
	}
	if err != nil {
		return src, nil, err
	}
	if playSHA256 != "" && !strings.EqualFold(playSHA256, rec.SHA256) {
		return src, nil, fmt.Errorf("sha256 is %s, not %s: refused before anything was shown or written", rec.SHA256, strings.ToLower(playSHA256))
	}
	return src, rec, nil
}

// checkRecipe checks a fetched recipe, the header's min-cpb included.
func checkRecipe(rec *play.Recipe) *play.Result {
	res := play.Check(rec.Bytes)
	for _, p := range res.Header.Problems {
		res.Refused = append(res.Refused, play.Refusal{Line: 0, What: "the header", Reason: p})
	}
	if res.Header.TooOld(Version) {
		res.Refused = append(res.Refused, play.Refusal{Line: 0, What: "the header",
			Reason: fmt.Sprintf("this recipe needs cpb %s or later; this is %s (cpb update)", res.Header.MinCPB, Version)})
	}
	return res
}

func runPlay(cmd *cobra.Command, args []string) error {
	if playJSONF && !playDryRun && !playCheck {
		return errors.New("--json needs --dry-run or --check")
	}
	if playUpdate != "" {
		return playUpdateRun(playUpdate)
	}
	ref := args[0]
	var claudeArgs []string
	if at := cmd.ArgsLenAtDash(); at >= 0 {
		claudeArgs = args[at:]
	}
	if playCheck {
		if info, err := os.Stat(ref); err == nil && info.IsDir() {
			return playCheckDir(ref)
		}
	}
	src, rec, err := fetchRecipe(ref)
	if err != nil {
		return err
	}
	res := checkRecipe(rec)
	block := playBlock(src, rec, res, "")
	if playCheck {
		if playJSONF {
			return printPlayCheckJSON(block)
		}
		printPlayCheck(os.Stdout, src, rec, res)
		if len(res.Refused) > 0 {
			return &commandExitError{code: 1}
		}
		return nil
	}
	if playKeep {
		return playKeepRun(src, rec, res, block)
	}
	if playDryRun {
		return playDryRunPlan(src, rec, res, block)
	}
	return playRun(src, rec, res, claudeArgs)
}

func playBlock(src *play.Source, rec *play.Recipe, res *play.Result, playbookName string) *playJSON {
	h := res.Header
	return &playJSON{
		Ref: src.Ref, Kind: src.Kind, URL: rec.URL, Path: rec.Path, SHA256: rec.SHA256, Bytes: len(rec.Bytes),
		Pinned: src.Pinned, Note: src.Note, Playbook: playbookName, Endpoint: res.Endpoint,
		Header:  playHeaderJSON{Title: h.Title, Description: h.Description, Needs: h.Needs, CreateWith: h.CreateWith, MinCPB: h.MinCPB},
		Refused: res.Refused, Risks: res.Risks,
	}
}

// printPlayCheck is the human report of --check, and the head of a preview.
func printPlayCheck(w *os.File, src *play.Source, rec *play.Recipe, res *play.Result) {
	from := rec.URL
	if from == "" {
		from = rec.Path
	}
	fmt.Fprintf(w, "Recipe:  %s\n", src.Ref)
	fmt.Fprintf(w, "From:    %s\n", from)
	fmt.Fprintf(w, "sha256:  %s (%d bytes)\n", rec.SHA256, len(rec.Bytes))
	if src.Note != "" {
		fmt.Fprintf(w, "Note:    %s\n", src.Note)
	}
	h := res.Header
	for _, kv := range [][2]string{{"Title", h.Title}, {"About", h.Description}, {"Needs", h.Needs}, {"Wants", h.CreateWith}} {
		if kv[1] != "" {
			fmt.Fprintf(w, "%-8s %s\n", kv[0]+":", kv[1])
		}
	}
	if len(res.Refused) > 0 {
		fmt.Fprintf(w, "\nRefused, so nothing would run (%d):\n", len(res.Refused))
		for _, f := range res.Refused {
			fmt.Fprintf(w, "  %s  %s: %s\n", playLine(f.Line), f.What, f.Reason)
		}
	}
	if len(res.Risks) == 0 {
		fmt.Fprintln(w, "\nNothing to look at: no program, plugin, remote server, wide permission or secret.")
		return
	}
	fmt.Fprintf(w, "\n%d thing(s) to look at:\n", len(res.Risks))
	for _, r := range res.Risks {
		mark := "!"
		if r.Confirm != "" {
			mark = "!!"
		}
		fmt.Fprintf(w, "  %-2s %s  %s: %s\n", mark, playLine(r.Line), r.Clause, r.Detail)
		if r.Confirm != "" {
			fmt.Fprintf(w, "       → to run it, you will type: %s\n", r.Confirm)
		}
	}
	if res.Endpoint != "" {
		fmt.Fprintf(w, "\nRequests would go to %s, not Anthropic: the played playbook gets its own login, and your\n"+
			"credential variables are blocked. Attach a key for that host yourself, by name.\n", res.Endpoint)
	}
}

func playLine(n int) string {
	if n == 0 {
		return "      "
	}
	return fmt.Sprintf("line %-2d", n)
}

func printPlayCheckJSON(block *playJSON) error {
	rep := &applyReport{Files: []string{}, Warnings: []applyWarning{}, Statements: []applyStmtJSON{}, Play: block}
	code := 0
	if len(block.Refused) > 0 {
		code = 1
		rep.Error = &applyErrorJSON{Message: fmt.Sprintf("%d refusal(s): a played recipe may not hold them", len(block.Refused))}
	}
	return printApplyReport(rep, code)
}

// playCredentialVars are the credentials a played playbook blocks when its
// endpoint is not Anthropic's, beside every credential-looking variable the
// shell exports: none of them may follow a recipe to another host.
var playCredentialVars = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN",
	"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE",
	"GOOGLE_APPLICATION_CREDENTIALS",
}

// playBlockedVars is the BLOCK VAR list for a recipe whose endpoint moves:
// the fixed list, and every credential-looking variable the environment
// carries, by name only.
func playBlockedVars() []string {
	seen := map[string]bool{}
	var out []string
	add := func(k string) {
		if !seen[k] && manifest.ValidateEnvKey(k) == nil {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, k := range playCredentialVars {
		add(k)
	}
	var shell []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if manifest.LooksLikeSecretKey(k) {
			shell = append(shell, k)
		}
	}
	sort.Strings(shell)
	for _, k := range shell {
		add(k)
	}
	return out
}

// playSetup is the statement play writes before the recipe: the throwaway
// playbook itself, never with the pilot profile, and, when the endpoint
// moves, with a login of its own and the credentials blocked.
func playSetup(name string, res *play.Result) string { return playSetupKeeping(name, res, nil) }

// playSetupKeeping is playSetup with keep's keys left out of the credential
// BLOCK: the ones an --env set the user attached provides.
func playSetupKeeping(name string, res *play.Result, keep map[string]bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE PLAYBOOK IF NOT EXISTS %s NO ALIAS NO PILOT PROFILE", name)
	if res.Endpoint != "" {
		b.WriteString(" ISOLATED LOGIN")
	}
	b.WriteString(";\n")
	if res.Endpoint != "" {
		var blocked []string
		for _, k := range playBlockedVars() {
			if !keep[k] {
				blocked = append(blocked, k)
			}
		}
		if len(blocked) > 0 {
			fmt.Fprintf(&b, "ALTER PLAYBOOK %s BLOCK VAR %s;\n", name, strings.Join(blocked, " "))
		}
	}
	return b.String()
}

// withThrowawayStore runs fn with a fresh, empty playbooks store, so the
// user's DEFAULTS and env sets cannot layer into a played recipe. Only the
// secret helper setting is copied, so references can be checked.
func withThrowawayStore(fn func(dir string) error) error {
	userStore := config.ResolvePlaybooksDir()
	tmp, err := os.MkdirTemp("", "cpb-play-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := os.Chmod(tmp, 0o700); err != nil {
		return err
	}
	store := filepath.Join(tmp, "store")
	if err := os.MkdirAll(envprofile.Dir(store), 0o700); err != nil {
		return err
	}
	if h, err := envprofile.SecretHelper(envprofile.Dir(userStore)); err == nil && h != nil {
		if err := envprofile.SetSecretHelper(envprofile.Dir(store), h.Command); err != nil {
			return err
		}
	}
	saved := config.PlaybooksDir
	config.PlaybooksDir = store
	defer func() { config.PlaybooksDir = saved }()
	return fn(tmp)
}

// playDryRunPlan plans the recipe against a throwaway playbook: the
// preview's head, then APPLY's own dry run of the exact bytes fetched.
func playDryRunPlan(src *play.Source, rec *play.Recipe, res *play.Result, block *playJSON) error {
	name := playName(src)
	block.Playbook = name
	sb := playSandboxDecision(res)
	block.Sandbox, block.Refused, block.Risks = sb, res.Refused, res.Risks
	if len(res.Refused) > 0 {
		if playJSONF {
			return printPlayCheckJSON(block)
		}
		printPlayCheck(os.Stdout, src, rec, res)
		return &commandExitError{code: 1}
	}
	userStore := config.ResolvePlaybooksDir()
	return withThrowawayStore(func(dir string) error {
		keep, err := copyEnvSets(userStore, config.ResolvePlaybooksDir())
		if err != nil {
			return err
		}
		setup, recipe, err := writePlayFiles(dir, src, rec, playSetupFor(name, res, playEnvSets, keep))
		if err != nil {
			return err
		}
		st := &grammar.Stmt{Verb: grammar.Apply, Files: []string{setup, recipe}, Target: name, DryRun: true, Yes: true, JSON: playJSONF}
		if playJSONF {
			return runPlayApplyJSON(st, block)
		}
		printPlayCheck(os.Stdout, src, rec, res)
		fmt.Println("\n" + sb.Note)
		fmt.Println("\nThe plan, against a throwaway playbook (nothing is written):")
		return applyRun(st, nil)
	})
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strings.Repeat("0", 2*n)
	}
	return hex.EncodeToString(b)
}

// runPlayApplyJSON is runApplyJSON with the play block added.
func runPlayApplyJSON(st *grammar.Stmt, block *playJSON) error {
	rep := &applyReport{Files: []string{}, Warnings: []applyWarning{}, Statements: []applyStmtJSON{}, Play: block}
	stdout := os.Stdout
	os.Stdout = os.Stderr
	err := applyRun(st, rep)
	os.Stdout = stdout
	code := 0
	if err != nil {
		code = 2
		var f *applyFailure
		if errors.As(err, &f) {
			code = f.code
		}
		if !(len(rep.Statements) > 0 && rep.Statements[len(rep.Statements)-1].Verdict == verdictRefused) {
			rep.Error = &applyErrorJSON{Message: err.Error()}
		}
	}
	return printApplyReport(rep, code)
}

// playCheckDir checks a template directory, as the website's CI runs it:
// every <name>.cpb, and index.txt listing exactly those names, sorted.
func playCheckDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	failed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".cpb") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".cpb")
		names = append(names, name)
		if _, err := play.Resolve(name, Version); err != nil {
			fmt.Printf("FAIL %s: not a template name ([a-z0-9][a-z0-9-]*)\n", e.Name())
			failed++
			continue
		}
		rec, err := play.ReadLocal(filepath.Join(dir, e.Name()))
		if err != nil {
			fmt.Printf("FAIL %s: %v\n", e.Name(), err)
			failed++
			continue
		}
		res := checkRecipe(rec)
		if res.Header.Title == "" || res.Header.Description == "" {
			res.Refused = append(res.Refused, play.Refusal{What: "the header", Reason: "a template needs -- title: and -- description:"})
		}
		for _, k := range res.Header.Unknown {
			res.Refused = append(res.Refused, play.Refusal{What: "the header", Reason: "unknown key " + k + ": the keys are title, description, needs, create-with, min-cpb"})
		}
		if len(res.Refused) > 0 {
			failed++
			for _, f := range res.Refused {
				fmt.Printf("FAIL %s %s %s: %s\n", e.Name(), strings.TrimSpace(playLine(f.Line)), f.What, f.Reason)
			}
			continue
		}
		fmt.Printf("ok   %s (%d risk(s))\n", e.Name(), len(res.Risks))
	}
	sort.Strings(names)
	idx, err := os.ReadFile(filepath.Join(dir, "index.txt"))
	switch {
	case err != nil:
		fmt.Printf("FAIL index.txt: %v\n", err)
		failed++
	default:
		listed := strings.Fields(string(idx))
		if strings.Join(listed, "\n") != strings.Join(names, "\n") {
			fmt.Printf("FAIL index.txt: it lists %v; the templates here, sorted, are %v\n", listed, names)
			failed++
		} else {
			fmt.Printf("ok   index.txt (%d template(s))\n", len(names))
		}
	}
	if failed > 0 {
		return &commandExitError{code: 1}
	}
	return nil
}

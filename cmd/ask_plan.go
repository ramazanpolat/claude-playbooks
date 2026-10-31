package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

// askPlan is the model's answer, then cpb's verdict on each step. The
// model writes answer, steps[].args, steps[].why and notes; everything else
// is cpb's own, decided from the parsed command and local state.
type askPlan struct {
	Answer string    `json:"answer"`
	Steps  []askStep `json:"steps"`
	Notes  []string  `json:"notes"`
}

type askStep struct {
	Args []string `json:"args"`
	Why  string   `json:"why"`

	Command   string     `json:"command"`          // as shown and run, after cpb
	Status    string     `json:"status"`           // ok | placeholder | rejected
	Reason    string     `json:"reason,omitempty"` // why it is not ok
	Guards    []askGuard `json:"guards,omitempty"` // what to know before running it
	RouteAway bool       `json:"route_away,omitempty"`

	// launch and writes say what the step is, for the plan-wide check:
	// a launch judged here, and a step that changes the registry.
	launch   bool
	writes   bool
	CPBNotes []string `json:"cpb_notes,omitempty"`
}

// askGuard is one thing a step does that its reader must know before
// running it: it removes, forgets or replaces something, touches a
// credential or the login, or fetches code.
type askGuard struct {
	Kind string `json:"kind"` // destructive | credential | fetch
	What string `json:"what"`
}

const (
	stepOK          = "ok"
	stepPlaceholder = "placeholder"
	stepRejected    = "rejected"

	guardDestructive = "destructive"
	guardCredential  = "credential"
	guardFetch       = "fetch"
)

// askVerbs are the commands a plan may hold besides statements.
var askVerbs = map[string]bool{"run": true, "start": true, "play": true, "update": true, "auth": true, "tui": true}

var placeholderRe = regexp.MustCompile(`<[^<>\s][^<>]*>`)

// checkAskPlan decides every step of p against the registry at root.
func checkAskPlan(p *askPlan, root string) {
	if p.Notes == nil {
		p.Notes = []string{}
	}
	if p.Steps == nil {
		p.Steps = []askStep{}
	}
	// A step is judged against the registry as it is: once an earlier step
	// changes it, a later launch may route elsewhere, and is shown as if it
	// routed away.
	changed := false
	for i := range p.Steps {
		st := &p.Steps[i]
		checkAskStep(st, root)
		if changed && st.Status == stepOK && st.launch && !st.RouteAway {
			st.RouteAway = true
			st.CPBNotes = append(st.CPBNotes, askChangedNote)
		}
		if st.Status == stepOK && st.writes {
			changed = true
		}
	}
}

func checkAskStep(s *askStep, root string) {
	args := append([]string(nil), s.Args...)
	if len(args) > 0 && args[0] == "cpb" {
		args = args[1:]
	}
	s.Args = args
	s.Command = askCommandLine(args)
	s.Status = stepOK
	reject := func(format string, a ...any) {
		s.Status, s.Reason, s.Guards = stepRejected, fmt.Sprintf(format, a...), nil
	}
	if len(args) == 0 {
		reject("an empty step")
		return
	}
	if strings.HasPrefix(args[0], "-") {
		reject("a step starts with a cpb command or statement, not a flag")
		return
	}
	var holes []string
	for _, a := range args {
		holes = append(holes, placeholderRe.FindAllString(a, -1)...)
	}
	if len(holes) > 0 {
		s.Status, s.Reason = stepPlaceholder, "fill in "+strings.Join(holes, ", ")+" first"
		return
	}
	if grammar.IsStatement(args) {
		if err := checkAskStatement(s, args); err != nil {
			reject("%v", err)
		}
		return
	}
	verb := args[0]
	if !askVerbs[verb] {
		reject("%q is not a command cpb ask proposes", verb)
		return
	}
	switch verb {
	case "run", "start":
		if err := checkAskLaunch(s, args, root); err != nil {
			reject("%v", err)
		}
	case "play":
		ref := firstOperand(args[1:])
		if ref == "" {
			reject("play what? cpb play <ref>")
			return
		}
		s.Guards = append(s.Guards, askGuard{guardFetch, "fetches the recipe " + ref + " and runs what it says"})
		s.writes = true
	case "update":
		name := firstOperand(args[1:])
		if name == "" {
			reject("update which playbook? cpb update <name>")
			return
		}
		if pb, err := playbook.Find(root, name); err != nil || pb == nil {
			reject("unknown playbook %q", name)
			return
		}
		s.Guards = append(s.Guards, askGuard{guardFetch, "fetches playbook " + name + "'s source again and installs what changed"})
		s.writes = true
	case "auth":
		if len(args) < 2 || args[1] != "status" {
			reject("only auth status is proposed")
		}
	}
}

// firstOperand is the first argument that is not a flag.
func firstOperand(args []string) string {
	for _, a := range args {
		if a == "--" {
			return ""
		}
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

// checkAskStatement parses a statement step as runStatement would, and
// records what in it to know before running it.
func checkAskStatement(s *askStep, args []string) error {
	if _, _, ok := describeArgs(args); ok {
		return nil
	}
	if _, _, _, ok := selectArgs(args); ok {
		return nil
	}
	var st *grammar.Stmt
	var err error
	if strings.ContainsAny(args[0], " \t\r\n") {
		if len(args) > 1 {
			return fmt.Errorf("a quoted statement is the whole command line")
		}
		st, err = grammar.ParseLine(args[0])
	} else {
		st, err = grammar.ParseArgs(args)
	}
	if err != nil {
		return err
	}
	s.writes = st.Write() || (st.Verb == grammar.Apply && !st.DryRun)
	s.Guards = append(s.Guards, statementGuards(st)...)
	return nil
}

// statementGuards is what a statement does that its reader must know: it
// removes, forgets or replaces something, touches a credential or the
// login, or fetches code.
func statementGuards(st *grammar.Stmt) []askGuard {
	var g []askGuard
	add := func(kind, format string, a ...any) { g = append(g, askGuard{kind, fmt.Sprintf(format, a...)}) }
	switch st.Verb {
	case grammar.Drop:
		switch st.Object {
		case grammar.Playbook:
			add(guardDestructive, "removes playbook %s: its directory, sessions and launcher", st.Name)
		case grammar.Env:
			add(guardDestructive, "removes env set %s", st.Name)
		}
	case grammar.Apply:
		if !st.DryRun {
			add(guardDestructive, "applies every statement in %s", strings.Join(st.Files, ", "))
		}
	case grammar.Create:
		if st.Object == grammar.Env && st.OrReplace {
			add(guardDestructive, "replaces env set %s's whole content", st.Name)
		}
	}
	var branch, subdir string
	for _, c := range st.Clauses {
		switch c.Kind {
		case grammar.Branch:
			branch = c.Arg
		case grammar.Subdir:
			subdir = c.Arg
		}
	}
	for _, c := range st.Clauses {
		g = append(g, clauseGuards(st, c, branch, subdir)...)
	}
	return g
}

func clauseGuards(st *grammar.Stmt, c grammar.Clause, branch, subdir string) []askGuard {
	var g []askGuard
	add := func(kind, format string, a ...any) { g = append(g, askGuard{kind, fmt.Sprintf(format, a...)}) }
	credKey := func(k string) bool { return k == auth.OAuthTokenEnv || manifest.LooksLikeSecretKey(k) }
	subject := strings.Join(append(append(append([]string(nil), c.Names...), c.Keys...), nonEmpty(c.Arg)...), " ")
	switch c.Kind {
	case grammar.From:
		src := c.Arg
		if branch != "" {
			src += " BRANCH " + branch
		}
		if subdir != "" {
			src += " SUBDIR " + subdir
		}
		add(guardFetch, "installs playbook %s from %s", st.Name, src)
	case grammar.SetVar:
		for _, v := range c.Vars {
			if credKey(v.Key) {
				if c.Plaintext {
					add(guardCredential, "stores %s as plaintext", v.Key)
				} else {
					add(guardCredential, "sets %s", v.Key)
				}
			}
		}
	case grammar.SetRef:
		for _, v := range c.Vars {
			add(guardCredential, "sets %s from the reference %s", v.Key, v.Ref)
		}
	case grammar.BlockVar:
		for _, k := range c.Keys {
			kind := guardDestructive
			if credKey(k) {
				kind = guardCredential
			}
			add(kind, "removes %s from every launch", k)
		}
	case grammar.UnsetVar:
		for _, k := range c.Keys {
			kind := guardDestructive
			if credKey(k) {
				kind = guardCredential
			}
			add(kind, "forgets %s", k)
		}
	case grammar.DropEnv:
		add(guardDestructive, "detaches env set %s", subject)
	case grammar.RenameTo:
		add(guardDestructive, "renames %s to %s", st.Name, c.Arg)
	case grammar.NoLauncher:
		add(guardDestructive, "removes %s's launcher", st.Name)
	case grammar.DefaultLauncher:
		add(guardDestructive, "resets %s's launcher to its name", st.Name)
	case grammar.SetProperties:
		for _, v := range c.Settings {
			if v.Key == "login" {
				add(guardCredential, "sets login = %s", v.Value)
			}
		}
	case grammar.ResetProperties, grammar.UnsetSandboxKeys:
		for _, v := range c.Settings {
			kind := guardDestructive
			if v.Key == "login" {
				kind = guardCredential
			}
			add(kind, "resets %s", v.Key)
		}
	case grammar.SetHelper, grammar.UnsetHelper:
		add(guardCredential, "changes the secret helper")
	case grammar.AddMarketplace:
		add(guardFetch, "adds marketplace %s from %s", strings.Join(c.Names, " "), c.Arg)
	case grammar.AddPlugin:
		add(guardFetch, "installs plugin %s", subject)
	case grammar.AddSkill:
		src := ""
		if c.Skill != nil {
			src = c.Skill.From
		}
		add(guardFetch, "adds skill %s from %s", subject, src)
	case grammar.AddMCP:
		what := "connects to"
		target := ""
		if c.MCP != nil {
			target = c.MCP.URL
			if c.MCP.Command != "" {
				what, target = "runs", strings.Join(append([]string{c.MCP.Command}, c.MCP.Args...), " ")
			}
			for _, v := range append(append([]grammar.Var(nil), c.MCP.Env...), c.MCP.Headers...) {
				if v.Ref != "" {
					add(guardCredential, "passes %s from the reference %s", v.Key, v.Ref)
				}
			}
		}
		add(guardFetch, "adds MCP server %s, which %s %s", subject, what, target)
	case grammar.DropMarketplace, grammar.DropPlugin, grammar.DropMCP, grammar.DropSkill, grammar.DropModel, grammar.UnsetTool:
		add(guardDestructive, "%s %s", strings.ToLower(string(c.Kind)), subject)
	case grammar.UnsetAgent, grammar.UnsetModel, grammar.UnsetStatusline, grammar.UnsetStatuslineRefresh, grammar.UnsetModelPickerMode:
		add(guardDestructive, "%s", strings.ToLower(string(c.Kind)))
	case grammar.SetIfUnset:
		for _, sub := range c.Group {
			g = append(g, clauseGuards(st, sub, branch, subdir)...)
		}
	}
	return g
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// checkAskLaunch checks a run or start step: its flags as the launch reads
// them (nothing evaluated, no file read), the playbook or directory, and
// where its requests would go.
func checkAskLaunch(s *askStep, args []string, root string) error {
	verb := args[0]
	// A step names no playbooks root or launcher directory of its own: the
	// launch would take it out of the arguments before reading its flags,
	// and it would be judged against another registry than this one.
	for _, a := range args[1:] {
		if a == "--" {
			break
		}
		if f, _, _ := strings.Cut(a, "="); f == "--playbooks-dir" || f == "--launcher-dir" {
			return fmt.Errorf("a step names no playbooks root or launcher directory of its own (%s)", f)
		}
	}
	var sopts sandboxOpts
	var deleteAfter bool
	extra := map[string]*bool{}
	if verb == "start" {
		extra["--delete"] = &deleteAfter
	}
	rest, tokens, err := takeRunFlags(args[1:], &sopts, extra)
	if err != nil {
		return err
	}
	if len(rest) == 0 || rest[0] == "--" {
		if verb == "run" {
			return fmt.Errorf("run which playbook? cpb run <name>")
		}
		return fmt.Errorf("start needs a directory: cpb start <dir>")
	}
	target := rest[0]
	claudeArgs, more, err := takeRunFlags(rest[1:], &sopts, extra)
	if err != nil {
		return err
	}
	tokens = append(tokens, more...)

	// The config directory the launch binds: the playbook's (or the
	// caller's CPB_CONFIG_DIR) for run, the directory for start.
	var configDir string
	if verb == "run" {
		pb, err := playbook.Find(root, target)
		if err != nil {
			return err
		}
		if pb == nil {
			return fmt.Errorf("unknown playbook %q", target)
		}
		configDir = pb.Path
		if dir, override, err := config.ResolveConfigDirOverride(); err == nil && override {
			configDir = dir
		}
	} else if configDir, err = filepath.Abs(target); err != nil {
		return err
	}

	envFile := false
	for _, t := range tokens {
		switch t.flag {
		case "--env":
			k, _, _ := strings.Cut(t.value, "=")
			if k == auth.OAuthTokenEnv || manifest.LooksLikeSecretKey(k) {
				s.Guards = append(s.Guards, askGuard{guardCredential, "sets " + k + " for this launch"})
			}
		case "--block":
			if t.value == auth.OAuthTokenEnv || manifest.LooksLikeSecretKey(t.value) {
				s.Guards = append(s.Guards, askGuard{guardCredential, "removes " + t.value + " for this launch"})
			}
		case "--env-file":
			envFile = true
			s.Guards = append(s.Guards, askGuard{guardCredential, "reads variables from " + t.value})
		}
	}
	// Settings passed through to claude are checked as the launch checks
	// them. A settings file is not read here: the check sees empty inline
	// settings in its place, and the launch is judged as away.
	settingsFile := false
	checkArgs := append([]string(nil), claudeArgs...)
	for i := 0; i < len(checkArgs); i++ {
		var value string
		inline := strings.HasPrefix(checkArgs[i], "--settings=")
		switch {
		case checkArgs[i] == "--settings" && i+1 < len(checkArgs):
			i++
			value = checkArgs[i]
		case inline:
			value = strings.TrimPrefix(checkArgs[i], "--settings=")
		default:
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(value), "{") {
			continue
		}
		settingsFile = true
		s.Guards = append(s.Guards, askGuard{guardCredential, "reads settings from " + value})
		if inline {
			checkArgs[i] = "--settings={}"
		} else {
			checkArgs[i] = "{}"
		}
	}
	if sopts.fresh {
		s.Guards = append(s.Guards, askGuard{guardDestructive, "removes the sandbox and creates it again"})
	}
	if deleteAfter {
		what := "deletes " + target + " when the session ends"
		if fi, err := os.Stat(target); err == nil && fi.IsDir() {
			what = "deletes " + target + " when the session ends: it exists now, and everything in it goes"
		}
		s.Guards = append(s.Guards, askGuard{guardDestructive, what})
	}
	if sopts.host != "" {
		return nil // runs on that host, with its registry: nothing to judge here
	}
	s.launch = true
	if err := refuseUnreadableManifest(configDir); err != nil {
		return err
	}
	// The route as the launch resolves it: DEFAULTS and the governing
	// manifest, then the launch flags in order on top, then the caller's
	// environment. An env file is not read here: judged as away. A
	// settings file is not read either, and the launch is judged as away
	// after the checks that do not need it.
	away := envFile
	if !envFile {
		layers, err := launchLayers(tokens)
		if err != nil {
			return err
		}
		// The launch as cpb would plan it: a launch cpb would refuse (an
		// account it may not use, a login it cannot keep out, a route split
		// across layers) is rejected here with cpb's own reason.
		name := ""
		if verb == "run" {
			name = target
		}
		wd, _ := os.Getwd()
		if _, err := planAccount(configDir, name, layers, wd, checkArgs); err != nil {
			return fmt.Errorf("cpb would refuse this launch: %v", err)
		}
		route, err := askLaunchRoute(configDir, layers, os.Environ())
		if err != nil {
			return err
		}
		away = settingsFile || route != "anthropic"
	}
	if away {
		s.RouteAway = true
		s.CPBNotes = append(s.CPBNotes, askRouteAwayNote)
	}
	if verb == "start" && !sopts.enabled {
		s.CPBNotes = append(s.CPBNotes, "The new directory shares this machine's login: a /login inside it replaces the machine's login.")
	}
	return nil
}

const askRouteAwayNote = "This launch sends its requests away from Anthropic, with the CLAUDE.md files of the directory it starts in and of every directory above it (from below home, ~/CLAUDE.md and ~/.claude/CLAUDE.md too). Run it from a directory outside home, as shown, so none of yours go along."

const askChangedNote = "A step before this one changes what this launch uses, so cpb ask cannot tell where it will send its requests: run it from a directory outside home, as shown."

// askHomeMemory lists the CLAUDE.md files a session started in dir would
// take from dir and the directories above it, spelled with ~: what a
// route-away launch from there would send along.
func askHomeMemory(dir string) []string {
	var out []string
	for d := dir; ; d = filepath.Dir(d) {
		for _, f := range []string{"CLAUDE.md", "CLAUDE.local.md", filepath.Join(".claude", "CLAUDE.md")} {
			if fi, err := os.Stat(filepath.Join(d, f)); err == nil && !fi.IsDir() {
				out = append(out, tildePath(filepath.Join(d, f)))
			}
		}
		if filepath.Dir(d) == d {
			return out
		}
	}
}

var bareArg = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,-]+$`)

// askCommandLine spells args as one shell line after cpb.
func askCommandLine(args []string) string {
	words := []string{"cpb"}
	for _, a := range args {
		if bareArg.MatchString(a) {
			words = append(words, a)
		} else {
			words = append(words, shell.QuoteArg(a))
		}
	}
	return strings.Join(words, " ")
}

// printAskPlan shows the plan. A step that routes away from Anthropic is
// shown run from /tmp, outside home; cwd is where the user asked from, to
// name the CLAUDE.md files a launch from there would send.
func printAskPlan(w io.Writer, p *askPlan, cwd string) {
	if a := strings.TrimSpace(p.Answer); a != "" {
		fmt.Fprintln(w, a)
	}
	for _, s := range p.Steps {
		fmt.Fprintln(w)
		line := s.Command
		if s.RouteAway && s.Status == stepOK {
			line = "(cd /tmp && " + line + ")"
		}
		switch s.Status {
		case stepRejected:
			fmt.Fprintf(w, "  %s\n      not proposed: %s\n", line, s.Reason)
			continue
		case stepPlaceholder:
			fmt.Fprintf(w, "  %s\n      %s\n", line, s.Reason)
		default:
			fmt.Fprintf(w, "  %s\n", line)
		}
		if why := strings.TrimSpace(s.Why); why != "" {
			fmt.Fprintf(w, "      %s\n", why)
		}
		for _, g := range s.Guards {
			fmt.Fprintf(w, "      ! %s\n", g.What)
		}
		for _, n := range s.CPBNotes {
			fmt.Fprintf(w, "      note: %s\n", n)
		}
		if s.RouteAway && s.Status == stepOK {
			if files := askHomeMemory(cwd); len(files) > 0 {
				fmt.Fprintf(w, "      note: from here (%s) it would have sent %s.\n", tildePath(cwd), strings.Join(files, ", "))
			}
		}
	}
	if len(p.Notes) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Notes:")
		for _, n := range p.Notes {
			fmt.Fprintf(w, "  - %s\n", n)
		}
	}
}

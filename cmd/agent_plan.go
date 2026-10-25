package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

// agentPlan is the model's answer, then cpb's verdict on each step. The
// model writes answer, steps[].args, steps[].why and notes; everything else
// is cpb's own, decided from the parsed command and local state.
type agentPlan struct {
	Answer string      `json:"answer"`
	Steps  []agentStep `json:"steps"`
	Notes  []string    `json:"notes"`
}

type agentStep struct {
	Args []string `json:"args"`
	Why  string   `json:"why"`

	Command   string       `json:"command"`          // as shown and run, after cpb
	Status    string       `json:"status"`           // ok | placeholder | rejected
	Reason    string       `json:"reason,omitempty"` // why it is not ok
	Guards    []agentGuard `json:"guards,omitempty"` // why it always asks first
	RouteAway bool         `json:"route_away,omitempty"`
	CPBNotes  []string     `json:"cpb_notes,omitempty"`
}

// agentGuard is one reason a step always asks before it runs.
type agentGuard struct {
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

// agentVerbs are the commands a plan may hold besides statements.
var agentVerbs = map[string]bool{"run": true, "start": true, "play": true, "update": true, "auth": true, "tui": true}

var placeholderRe = regexp.MustCompile(`<[^<>\s][^<>]*>`)

// checkAgentPlan decides every step of p against the registry at root.
func checkAgentPlan(p *agentPlan, root string) {
	if p.Notes == nil {
		p.Notes = []string{}
	}
	if p.Steps == nil {
		p.Steps = []agentStep{}
	}
	for i := range p.Steps {
		checkAgentStep(&p.Steps[i], root)
	}
}

func checkAgentStep(s *agentStep, root string) {
	args := append([]string(nil), s.Args...)
	if len(args) > 0 && args[0] == "cpb" {
		args = args[1:]
	}
	s.Args = args
	s.Command = agentCommandLine(args)
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
		if err := checkAgentStatement(s, args); err != nil {
			reject("%v", err)
		}
		return
	}
	verb := args[0]
	if !agentVerbs[verb] {
		reject("%q is not a command cpb agent proposes", verb)
		return
	}
	switch verb {
	case "run", "start":
		if err := checkAgentLaunch(s, args, root); err != nil {
			reject("%v", err)
		}
	case "play":
		ref := firstOperand(args[1:])
		if ref == "" {
			reject("play what? cpb play <ref>")
			return
		}
		s.Guards = append(s.Guards, agentGuard{guardFetch, "fetches the recipe " + ref + " and runs what it says"})
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
		s.Guards = append(s.Guards, agentGuard{guardFetch, "fetches playbook " + name + "'s source again and installs what changed"})
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

// checkAgentStatement parses a statement step as runStatement would, and
// records what in it always asks first.
func checkAgentStatement(s *agentStep, args []string) error {
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
	s.Guards = append(s.Guards, statementGuards(st)...)
	return nil
}

// statementGuards is what a statement does that always asks first: it
// removes, forgets or replaces something, touches a credential or the
// login, or fetches code.
func statementGuards(st *grammar.Stmt) []agentGuard {
	var g []agentGuard
	add := func(kind, format string, a ...any) { g = append(g, agentGuard{kind, fmt.Sprintf(format, a...)}) }
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

func clauseGuards(st *grammar.Stmt, c grammar.Clause, branch, subdir string) []agentGuard {
	var g []agentGuard
	add := func(kind, format string, a ...any) { g = append(g, agentGuard{kind, fmt.Sprintf(format, a...)}) }
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

// checkAgentLaunch checks a run or start step: its flags as the launch reads
// them (nothing evaluated, no file read), the playbook or directory, and
// where its requests would go.
func checkAgentLaunch(s *agentStep, args []string, root string) error {
	verb := args[0]
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
	_, more, err := takeRunFlags(rest[1:], &sopts, extra)
	if err != nil {
		return err
	}
	tokens = append(tokens, more...)

	env := &manifest.Env{}
	if verb == "run" {
		pb, err := playbook.Find(root, target)
		if err != nil {
			return err
		}
		if pb == nil {
			return fmt.Errorf("unknown playbook %q", target)
		}
		if pb.Manifest != nil && pb.Manifest.Env != nil {
			e := pb.Manifest.Env
			env = &manifest.Env{Sets: append([]string(nil), e.Sets...), Set: map[string]string{}, Refs: e.Refs, Block: append([]string(nil), e.Block...)}
			for k, v := range e.Set {
				env.Set[k] = v
			}
		}
	}
	envFile := false
	for _, t := range tokens {
		switch t.flag {
		case "--env-set":
			env.Sets = append(env.Sets, t.value)
		case "--env":
			k, v, _ := strings.Cut(t.value, "=")
			if env.Set == nil {
				env.Set = map[string]string{}
			}
			env.Set[k] = v
			if k == auth.OAuthTokenEnv || manifest.LooksLikeSecretKey(k) {
				s.Guards = append(s.Guards, agentGuard{guardCredential, "sets " + k + " for this launch"})
			}
		case "--block":
			env.Block = append(env.Block, t.value)
			if t.value == auth.OAuthTokenEnv || manifest.LooksLikeSecretKey(t.value) {
				s.Guards = append(s.Guards, agentGuard{guardCredential, "removes " + t.value + " for this launch"})
			}
		case "--env-file":
			envFile = true
			s.Guards = append(s.Guards, agentGuard{guardCredential, "reads variables from " + t.value})
		}
	}
	if sopts.fresh {
		s.Guards = append(s.Guards, agentGuard{guardDestructive, "removes the sandbox and creates it again"})
	}
	if deleteAfter {
		what := "deletes " + target + " when the session ends"
		if fi, err := os.Stat(target); err == nil && fi.IsDir() {
			what = "deletes " + target + " when the session ends: it exists now, and everything in it goes"
		}
		s.Guards = append(s.Guards, agentGuard{guardDestructive, what})
	}
	if sopts.host != "" {
		return nil // runs on that host, with its registry: nothing to judge here
	}
	origins, err := envset.Explain(envset.Dir(root), env)
	if err != nil {
		return err
	}
	origins = append(origins, routeInherited(origins, os.Environ())...)
	if envFile || routeEgress(origins) != "anthropic" {
		s.RouteAway = true
		s.CPBNotes = append(s.CPBNotes, "This launch sends its requests away from Anthropic, with the CLAUDE.md files of the directory it starts in and of every directory above it (from below home, ~/CLAUDE.md and ~/.claude/CLAUDE.md too). cpb agent runs it from a new directory outside home, so none of yours go along.")
	}
	if verb == "start" && !sopts.enabled {
		s.CPBNotes = append(s.CPBNotes, "The new directory shares this machine's login: a /login inside it replaces the machine's login.")
	}
	return nil
}

// agentHomeMemory lists the CLAUDE.md files a session started in dir would
// take from dir and the directories above it, spelled with ~: what a
// route-away launch from there would send along.
func agentHomeMemory(dir string) []string {
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

// agentCommandLine spells args as one shell line after cpb.
func agentCommandLine(args []string) string {
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

// printAgentPlan shows the plan. neutral is the directory a route-away step
// runs from: "/tmp" when only shown, the real one when about to run.
func printAgentPlan(w io.Writer, p *agentPlan, cwd, neutral string) {
	if a := strings.TrimSpace(p.Answer); a != "" {
		fmt.Fprintln(w, a)
	}
	for _, s := range p.Steps {
		fmt.Fprintln(w)
		line := s.Command
		if s.RouteAway && s.Status == stepOK {
			line = "(cd " + shellWord(neutral) + " && " + line + ")"
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
			if files := agentHomeMemory(cwd); len(files) > 0 {
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

func shellWord(s string) string {
	if bareArg.MatchString(s) {
		return s
	}
	return shell.QuoteArg(s)
}

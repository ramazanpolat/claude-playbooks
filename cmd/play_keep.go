package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/play"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// cpb play --keep (slice 4): a played recipe kept as a playbook in the
// user's own store, with a [play] record, and updated only when asked
// (cpb update <name>).

var (
	playKeep bool
	playAs   string
)

// playRecipeFile is where a kept playbook holds the exact bytes it was
// built from: an update diffs against them and undoes what they added.
const playRecipeFile = ".play/recipe.cpb"

// playKeepName is the kept playbook's name: --as, or the template's or
// file's own name.
func playKeepName(src *play.Source) string {
	if playAs != "" {
		return playAs
	}
	return src.Name
}

// keepSandbox decides whether a kept playbook is created SANDBOX: the
// recipe's create-with: SANDBOX unless --no-sandbox, or --sandbox. Nothing
// runs now, so no backend has to be here yet; a kept playbook with secret
// references cannot be sandboxed (a sandboxed launch cannot resolve them).
func keepSandbox(res *play.Result) (sandboxed bool, backend string, err error) {
	if playNoSandbox && playSandboxFlag != "" {
		return false, "", errors.New("--sandbox and --no-sandbox together: pick one")
	}
	if playSandboxFlag != "" && playSandboxFlag != "auto" && !manifest.KnownSandboxBackend(playSandboxFlag) {
		return false, "", fmt.Errorf("--sandbox=%s: not a sandbox backend (available: %s)", playSandboxFlag, strings.Join(manifest.SandboxBackends, ", "))
	}
	sandboxed = playSandboxFlag != "" || (res.Header.WantsSandbox() && !playNoSandbox)
	if sandboxed {
		if refs := playRefs(res); len(refs) > 0 {
			return false, "", fmt.Errorf("the recipe uses secret references (%s), which a sandboxed launch cannot resolve yet: keep it with --no-sandbox", strings.Join(refs, ", "))
		}
	}
	if playSandboxFlag != "auto" {
		backend = playSandboxFlag
	}
	return sandboxed, backend, nil
}

func playRefs(res *play.Result) []string {
	var refs []string
	for _, r := range res.Risks {
		if r.Code == play.RiskUsesSecret {
			refs = append(refs, r.Confirm)
		}
	}
	return refs
}

func keepSandboxNote(sandboxed bool, res *play.Result) string {
	switch {
	case sandboxed:
		return "Sandboxed: every launch of it runs in a sandbox (CREATE PLAYBOOK … SANDBOX)."
	case res.Header.WantsSandbox():
		return "Not sandboxed (--no-sandbox), although the recipe asks for one: it will run on your machine, as you."
	}
	return "Not sandboxed: it will run on your machine, as you, unless you launch it with --sandbox."
}

// defaultsKeys is the user's DEFAULTS env sets and the keys they carry: a
// kept playbook lives in the user's store, so they layer into it.
func defaultsKeys(store string) ([]string, map[string]bool, error) {
	dir := envprofile.Dir(store)
	names, err := envprofile.Defaults(dir)
	if err != nil {
		return nil, nil, err
	}
	keys := map[string]bool{}
	for _, n := range names {
		p, err := envprofile.Read(dir, n)
		if err != nil || p == nil {
			continue
		}
		e := p.Env()
		for k := range e.Set {
			keys[k] = true
		}
		for k := range e.Refs {
			keys[k] = true
		}
	}
	return names, keys, nil
}

// envSetKeys checks the --env sets exist in the user's store and returns
// the keys they set, which the credential BLOCK leaves alone.
func envSetKeys(store string, sets []string) (map[string]bool, error) {
	keys := map[string]bool{}
	for _, name := range sets {
		p, err := envprofile.Read(envprofile.Dir(store), name)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, fmt.Errorf("--env %s: no such env set (SHOW ENVS)", name)
		}
		e := p.Env()
		for k := range e.Set {
			keys[k] = true
		}
		for k := range e.Refs {
			keys[k] = true
		}
	}
	return keys, nil
}

// keepBlocked is the BLOCK VAR list of a kept playbook whose endpoint
// moves: play's credential list, and every key the DEFAULTS sets would
// layer in (they do not follow the recipe to another host), minus the keys
// of the --env sets the user attached and the ones the recipe sets itself.
func keepBlocked(defaults, keep, own map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	add := func(k string) {
		if !seen[k] && !keep[k] && !own[k] && manifest.ValidateEnvKey(k) == nil {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, k := range playBlockedVars() {
		add(k)
	}
	var dk []string
	for k := range defaults {
		dk = append(dk, k)
	}
	sort.Strings(dk)
	for _, k := range dk {
		add(k)
	}
	return out
}

// keepSetup is what --keep writes before the recipe: the playbook, with a
// launcher; a login of its own and the credentials blocked when the
// endpoint moves; and the --env sets.
func keepSetup(name string, res *play.Result, sandboxed bool, blocked []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE PLAYBOOK %s", name)
	if res.Endpoint != "" {
		b.WriteString(" ISOLATED LOGIN")
	}
	if sandboxed {
		b.WriteString(" SANDBOX")
	}
	b.WriteString(";\n")
	if res.Endpoint != "" && len(blocked) > 0 {
		fmt.Fprintf(&b, "ALTER PLAYBOOK %s BLOCK VAR %s;\n", name, strings.Join(blocked, " "))
	}
	if len(playEnvSets) > 0 {
		fmt.Fprintf(&b, "ALTER PLAYBOOK %s USE ENV %s;\n", name, strings.Join(playEnvSets, " "))
	}
	return b.String()
}

// printDefaultsNote says which DEFAULTS sets layer into a kept playbook,
// and that they do not follow a moved endpoint.
func printDefaultsNote(names []string, res *play.Result) {
	if len(names) == 0 {
		return
	}
	if res.Endpoint == "" {
		fmt.Printf("\nYour DEFAULTS apply to it, as to every playbook in your store: %s.\n", strings.Join(names, ", "))
		return
	}
	fmt.Printf("\nYour DEFAULTS (%s) will NOT follow it to %s: their keys are blocked in this playbook.\n"+
		"Attach a set it may use with --env <set>.\n", strings.Join(names, ", "), res.Endpoint)
}

// playStage writes statement files into a private temp directory and
// returns their paths and a clean-up.
func playStage(texts ...[2]string) ([]string, func(), error) {
	dir, err := os.MkdirTemp("", "cpb-play-stage-")
	if err != nil {
		return nil, func() {}, err
	}
	done := func() { os.RemoveAll(dir) }
	var files []string
	for _, t := range texts {
		if t[1] == "" {
			continue
		}
		p := filepath.Join(dir, t[0])
		if err := os.WriteFile(p, []byte(t[1]), 0o600); err != nil {
			done()
			return nil, func() {}, err
		}
		files = append(files, p)
	}
	return files, done, nil
}

// writePlayRecord stores the exact bytes and the [play] record in a kept
// playbook.
func writePlayRecord(pbDir string, src *play.Source, rec *play.Recipe, backend string) error {
	if err := os.MkdirAll(filepath.Join(pbDir, filepath.Dir(playRecipeFile)), 0o700); err != nil {
		return err
	}
	if err := manifest.WritePrivate(filepath.Join(pbDir, playRecipeFile), rec.Bytes, 0o600); err != nil {
		return err
	}
	m, err := manifest.Read(pbDir)
	if err != nil {
		return err
	}
	if m == nil {
		m = &manifest.Manifest{Name: filepath.Base(pbDir)}
	}
	ref := src.Ref
	if src.Kind == play.KindLocal {
		ref = src.Path // absolute: an update resolves it from anywhere
	}
	m.Play = &manifest.Play{Ref: ref, URL: rec.URL, SHA256: rec.SHA256, PlayedAt: time.Now().UTC().Format(time.RFC3339)}
	if backend != "" {
		if m.Sandbox == nil {
			m.Sandbox = &manifest.Sandbox{}
		}
		m.Sandbox.Backend = backend
	}
	return manifest.Write(pbDir, m)
}

// playKeepRun: the preview against the user's own store, the
// confirmations, then the playbook built and recorded. No session.
func playKeepRun(src *play.Source, rec *play.Recipe, res *play.Result, block *playJSON) error {
	name := playKeepName(src)
	if block != nil {
		block.Playbook = name
	}
	if len(res.Refused) > 0 {
		if block != nil && playJSONF {
			return printPlayCheckJSON(block)
		}
		printPlayCheck(os.Stdout, src, rec, res)
		return &commandExitError{code: 1}
	}
	sandboxed, backend, err := keepSandbox(res)
	if err != nil {
		return fmt.Errorf("%v; nothing was written", err)
	}
	store := config.ResolvePlaybooksDir()
	if pb, err := playbook.Find(store, name); err != nil {
		return err
	} else if pb != nil {
		hint := "keep it under another name with --as <name>"
		if pb.Manifest != nil && pb.Manifest.Play != nil {
			hint += ", or update it with cpb update " + name
		}
		return fmt.Errorf("a playbook named %s exists: %s; nothing was written", name, hint)
	}
	defNames, defKeys, err := defaultsKeys(store)
	if err != nil {
		return err
	}
	keep, err := envSetKeys(store, playEnvSets)
	if err != nil {
		return err
	}
	files, done, err := playStage([2]string{"_play-setup.cpb", keepSetup(name, res, sandboxed, keepBlocked(defKeys, keep, recipeKeys(rec.Bytes)))}, [2]string{src.Name + ".cpb", string(rec.Bytes)})
	if err != nil {
		return err
	}
	defer done()
	plan := &grammar.Stmt{Verb: grammar.Apply, Files: files, Target: name, DryRun: true, Yes: true, JSON: playJSONF}
	if playDryRun && playJSONF {
		sb := &playSandbox{Note: keepSandboxNote(sandboxed, res)}
		if sandboxed {
			sb.Backend = backend
			if sb.Backend == "" {
				sb.Backend = "default"
			}
		}
		block.Sandbox, block.Keep = sb, true
		return runPlayApplyJSON(plan, block)
	}
	printPlayCheck(os.Stdout, src, rec, res)
	fmt.Printf("\nWhat it would do: a playbook %s in your store, with a launcher %s:\n", name, name)
	if err := applyRun(plan, nil); err != nil {
		return err
	}
	printDefaultsNote(defNames, res)
	fmt.Println("\n" + keepSandboxNote(sandboxed, res))
	if playDryRun {
		return nil
	}
	if err := playConfirmAsk(res, isTerminal(os.Stdin) && isTerminal(os.Stdout), fmt.Sprintf("\nKeep this playbook as %s? [y/N] ", name)); err != nil {
		return err
	}
	if err := applyRun(&grammar.Stmt{Verb: grammar.Apply, Files: files, Target: name, Yes: true}, nil); err != nil {
		dropHalfKept(name)
		return err
	}
	pb, err := playbook.Find(store, name)
	if err != nil || pb == nil {
		return fmt.Errorf("kept %s, but cannot find it to record where it came from: %v", name, err)
	}
	if err := writePlayRecord(pb.RootPath, src, rec, backend); err != nil {
		return fmt.Errorf("kept %s, but could not record where it came from: %v", name, err)
	}
	fmt.Printf("\nKept as %s: run it with `%s` (or cpb run %s). Update it with cpb update %s.\n", name, name, name, name)
	return nil
}

// dropHalfKept removes a playbook --keep created when applying the recipe
// failed part-way: it did not exist before, so nothing of the user's goes.
func dropHalfKept(name string) {
	files, done, err := playStage([2]string{"_play-drop.cpb", fmt.Sprintf("DROP PLAYBOOK IF EXISTS %s;\n", name)})
	if err != nil {
		return
	}
	defer done()
	stdout := os.Stdout
	os.Stdout = os.Stderr
	if err := applyRun(&grammar.Stmt{Verb: grammar.Apply, Files: files, Yes: true}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %s was left part-built: cpb DROP PLAYBOOK %s --yes removes it (%v)\n", name, name, err)
	}
	os.Stdout = stdout
}

// undoItem is one thing a recipe clause sets: key names it, so an update
// can tell what the new recipe no longer sets; sig is the clause as
// written, so it can tell what changed; undo removes it.
type undoItem struct {
	key, sig string
	undo     grammar.Clause
}

// clauseUndo lists what a clause sets. Clauses with nothing to undo
// return none.
func clauseUndo(c grammar.Clause) []undoItem {
	type u = undoItem
	one := func(c grammar.Clause) string {
		return (&grammar.Stmt{Verb: grammar.Alter, Object: grammar.Playbook, Recipe: true, Clauses: []grammar.Clause{c}}).String()
	}
	switch c.Kind {
	case grammar.SetVar:
		var out []u
		for _, v := range c.Vars {
			cc := c
			cc.Vars = []grammar.Var{v}
			out = append(out, u{"var:" + v.Key, one(cc), grammar.Clause{Kind: grammar.UnsetVar, Keys: []string{v.Key}}})
		}
		return out
	case grammar.SetRef:
		v := c.Vars[0]
		return []u{{"var:" + v.Key, one(c), grammar.Clause{Kind: grammar.UnsetVar, Keys: []string{v.Key}}}}
	case grammar.BlockVar:
		var out []u
		for _, k := range c.Keys {
			out = append(out, u{"var:" + k, "BLOCK " + k, grammar.Clause{Kind: grammar.UnsetVar, Keys: []string{k}}})
		}
		return out
	case grammar.AllowTool, grammar.DenyTool:
		var out []u
		for _, r := range c.Names {
			out = append(out, u{"tool:" + r, string(c.Kind) + " " + r, grammar.Clause{Kind: grammar.UnsetTool, Names: []string{r}}})
		}
		return out
	case grammar.AddMarketplace:
		return []u{{"marketplace:" + c.Names[0], one(c), grammar.Clause{Kind: grammar.DropMarketplace, Names: c.Names[:1]}}}
	case grammar.AddPlugin:
		return []u{{"plugin:" + c.Names[0], one(c), grammar.Clause{Kind: grammar.DropPlugin, Names: c.Names[:1]}}}
	case grammar.AddMCP:
		return []u{{"mcp:" + c.Names[0], one(c), grammar.Clause{Kind: grammar.DropMCP, Names: c.Names[:1]}}}
	case grammar.AddSkill:
		return []u{{"skill:" + c.Names[0], one(c), grammar.Clause{Kind: grammar.DropSkill, Names: c.Names[:1]}}}
	case grammar.AddModel:
		return []u{{"model:" + c.Row.Model, one(c), grammar.Clause{Kind: grammar.DropModel, Names: []string{c.Row.Model}}}}
	case grammar.SetAgent:
		return []u{{"agent", one(c), grammar.Clause{Kind: grammar.UnsetAgent}}}
	case grammar.SetModel:
		return []u{{"setmodel", one(c), grammar.Clause{Kind: grammar.UnsetModel}}}
	case grammar.SetModelPicker:
		return []u{{"picker", one(c), grammar.Clause{Kind: grammar.UnsetModelPicker}}}
	case grammar.SetStatusline:
		return []u{{"statusline", one(c), grammar.Clause{Kind: grammar.UnsetStatusline}}}
	case grammar.SetStatuslineRefresh:
		return []u{{"refresh", one(c), grammar.Clause{Kind: grammar.UnsetStatuslineRefresh}}}
	}
	// SET ISOLATED LOGIN and NO ALIAS: a login is never unset by an update,
	// and the launcher is play's.
	return nil
}

// undoFor is the statement that removes what the old recipe set and the
// new one no longer sets the same way, in an order that drops plugins
// before their marketplace. nil when there is nothing to undo.
func undoFor(name string, oldStmts, newStmts []*grammar.Stmt) *grammar.Stmt {
	sigs := map[string]string{}
	for _, s := range newStmts {
		for _, c := range s.Clauses {
			for _, x := range clauseUndo(c) {
				sigs[x.key] = x.sig
			}
		}
	}
	var undo []grammar.Clause
	var marketplaces []grammar.Clause
	seen := map[string]bool{}
	for _, s := range oldStmts {
		for _, c := range s.Clauses {
			for _, x := range clauseUndo(c) {
				if seen[x.key] || sigs[x.key] == x.sig {
					continue
				}
				seen[x.key] = true
				if x.undo.Kind == grammar.DropMarketplace {
					marketplaces = append(marketplaces, x.undo)
				} else {
					undo = append(undo, x.undo)
				}
			}
		}
	}
	undo = append(undo, marketplaces...)
	if len(undo) == 0 {
		return nil
	}
	return &grammar.Stmt{Verb: grammar.Alter, Object: grammar.Playbook, Name: name, Clauses: undo}
}

// recipeLines is a recipe's statement lines: no header, comments or blanks.
func recipeLines(b []byte) []string {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimRight(l, "\r \t")
		if t := strings.TrimSpace(l); t == "" || strings.HasPrefix(t, "--") {
			continue
		}
		out = append(out, l)
	}
	return out
}

// lineDiff is a minimal line diff: "- " for a line only the old recipe
// has, "+ " for one only the new has, in order.
func lineDiff(a, b []string) []string {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			i, j = i+1, j+1
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			out = append(out, "- "+a[i])
			i++
		default:
			out = append(out, "+ "+b[j])
			j++
		}
	}
	return out
}

// playUpdateRun fetches a kept playbook's recorded ref again. The same
// bytes: unchanged, nothing runs. Others: the diff, the full preview and
// the confirmations, then what the old recipe set and the new one does not
// is undone and the new bytes applied.
func playUpdateRun(name string) error {
	store := config.ResolvePlaybooksDir()
	pb, err := playbook.Find(store, name)
	if err != nil {
		return err
	}
	if pb == nil {
		return fmt.Errorf("no playbook named %s (SHOW PLAYBOOKS)", name)
	}
	if pb.Manifest == nil || pb.Manifest.Play == nil {
		return fmt.Errorf("%s was not kept by cpb play (its .playbook has no [play] record): nothing to update", name)
	}
	recd := pb.Manifest.Play
	src, rec, err := fetchRecipe(recd.Ref)
	if err != nil {
		return err
	}
	res := checkRecipe(rec)
	block := playBlock(src, rec, res, name)
	if rec.SHA256 == recd.SHA256 {
		if playDryRun && playJSONF {
			block.Update = &playUpdateJSON{FromSHA256: recd.SHA256, Unchanged: true}
			return printPlayCheckJSON(block)
		}
		fmt.Printf("%s is unchanged: %s still has sha256 %s.\n", name, recd.Ref, rec.SHA256)
		return nil
	}
	block.Update = &playUpdateJSON{FromSHA256: recd.SHA256, TagMoved: src.Pinned && recd.URL != "" && rec.URL == recd.URL}
	if len(res.Refused) > 0 {
		if playJSONF {
			return printPlayCheckJSON(block)
		}
		printPlayCheck(os.Stdout, src, rec, res)
		return &commandExitError{code: 1}
	}
	old, err := os.ReadFile(filepath.Join(pb.RootPath, playRecipeFile))
	if err != nil {
		return fmt.Errorf("%s: the recipe it was built from is missing (%s): drop it and keep the recipe again", name, playRecipeFile)
	}
	oldStmts, err := grammar.ParseFile(string(old))
	if err != nil {
		return fmt.Errorf("%s: the recipe it was built from (%s) does not parse: %v", name, playRecipeFile, err)
	}
	newStmts, err := grammar.ParseFile(string(rec.Bytes))
	if err != nil {
		return err
	}
	if pb.Manifest.Sandbox != nil && pb.Manifest.Sandbox.Always {
		if refs := playRefs(res); len(refs) > 0 {
			return fmt.Errorf("%s is sandboxed, and the new recipe uses secret references (%s), which a sandboxed launch cannot resolve yet; nothing was written", name, strings.Join(refs, ", "))
		}
	}
	defNames, defKeys, err := defaultsKeys(store)
	if err != nil {
		return err
	}
	keep, err := envSetKeys(store, playEnvSets)
	if err != nil {
		return err
	}
	attached := map[string]bool{}
	if pb.Manifest.Env != nil {
		for _, p := range pb.Manifest.Env.Sets {
			attached[p] = true
			if k, err := envSetKeys(store, []string{p}); err == nil {
				for key := range k {
					keep[key] = true
				}
			}
		}
	}
	var setup strings.Builder
	if res.Endpoint != "" {
		fmt.Fprintf(&setup, "ALTER PLAYBOOK %s SET ISOLATED LOGIN", name)
		if blocked := keepBlocked(defKeys, keep, recipeKeys(rec.Bytes)); len(blocked) > 0 {
			fmt.Fprintf(&setup, " BLOCK VAR %s", strings.Join(blocked, " "))
		}
		setup.WriteString(";\n")
	}
	for _, e := range playEnvSets {
		if !attached[e] {
			fmt.Fprintf(&setup, "ALTER PLAYBOOK %s ADD ENV %s;\n", name, e)
		}
	}
	undoText := ""
	if u := undoFor(name, oldStmts, newStmts); u != nil {
		undoText = u.Pretty() + ";\n"
	}
	// The recipe's own name-less statements target the playbook.
	files, done, err := playStage([2]string{"_play-undo.cpb", undoText}, [2]string{"_play-setup.cpb", setup.String()}, [2]string{src.Name + ".cpb", string(rec.Bytes)})
	if err != nil {
		return err
	}
	defer done()
	plan := &grammar.Stmt{Verb: grammar.Apply, Files: files, Target: name, DryRun: true, Yes: true, JSON: playJSONF}
	if playDryRun && playJSONF {
		return runPlayApplyJSON(plan, block)
	}
	printPlayCheck(os.Stdout, src, rec, res)
	fmt.Printf("\nChanges from sha256 %s to %s:\n", shortSHA(recd.SHA256), shortSHA(rec.SHA256))
	for _, l := range lineDiff(recipeLines(old), recipeLines(rec.Bytes)) {
		fmt.Println("  " + l)
	}
	if block.Update.TagMoved {
		fmt.Printf("\nNote: %s is pinned, and it now serves different bytes: the tag moved.\n", recd.Ref)
	}
	fmt.Printf("\nWhat the update would do to %s:\n", name)
	if err := applyRun(plan, nil); err != nil {
		return err
	}
	printDefaultsNote(defNames, res)
	if playDryRun {
		return nil
	}
	if err := playConfirmAsk(res, isTerminal(os.Stdin) && isTerminal(os.Stdout), fmt.Sprintf("\nUpdate %s to these bytes? [y/N] ", name)); err != nil {
		return err
	}
	if err := applyRun(&grammar.Stmt{Verb: grammar.Apply, Files: files, Target: name, Yes: true}, nil); err != nil {
		return err
	}
	if err := writePlayRecord(pb.RootPath, src, rec, ""); err != nil {
		return fmt.Errorf("updated %s, but could not record it: %v", name, err)
	}
	fmt.Printf("\nUpdated %s to sha256 %s.\n", name, shortSHA(rec.SHA256))
	return nil
}

// playUpdateJSON is the play block's "update" in cpb update <name> --dry-run --json.
type playUpdateJSON struct {
	FromSHA256 string `json:"from_sha256"`
	Unchanged  bool   `json:"unchanged"`
	TagMoved   bool   `json:"tag_moved"`
}

// recipeKeys is every variable a recipe sets, references or blocks itself.
func recipeKeys(b []byte) map[string]bool {
	keys := map[string]bool{}
	stmts, err := grammar.ParseFile(string(b))
	if err != nil {
		return keys
	}
	for _, s := range stmts {
		for _, c := range s.Clauses {
			for _, x := range clauseUndo(c) {
				if k, ok := strings.CutPrefix(x.key, "var:"); ok {
					keys[k] = true
				}
			}
		}
	}
	return keys
}

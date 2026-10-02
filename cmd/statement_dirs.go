package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/settings"
)

// A recipe applied TO a plain Claude Code config directory, such as
// ~/.claude (docs/reference/cli-grammar.md, "TO a plain config directory").
// Nothing of cpb runs at that directory's launches, so only Claude Code's
// own configuration applies: plugins, the agent, MCP servers without
// references, tool permissions, the status line, the model, skills, and
// plain values in settings.json's env. Before the first write in a run,
// settings.json (and .claude.json before an MCP change) is backed up beside
// itself.

// dirRefusals names why each refused clause cannot apply to a directory.
var dirRefusals = map[grammar.Kind]string{
	grammar.SetRef:   "a secret reference is resolved by cpb's launcher, which never runs for this directory",
	grammar.BlockVar: "removing a variable at launch is the launcher's job",
	grammar.UseEnv:   "env sets are layered by the launcher",
	grammar.AddEnv:   "env sets are layered by the launcher",
	grammar.DropEnv:  "env sets are layered by the launcher",
	grammar.RenameTo: "the directory is not in the registry",
	grammar.Alias:    "the directory has no launcher",
	grammar.NoAlias:  "the directory has no launcher",

	grammar.SetIsolatedLogin:   "isolate_auth is recorded in a playbook's manifest, which the directory does not have",
	grammar.UnsetIsolatedLogin: "isolate_auth is recorded in a playbook's manifest, which the directory does not have",
}

// validateDirClauses refuses, with its reason, a clause that cannot apply
// to a plain directory. APPLY calls it for every such statement before
// anything is written.
func validateDirClauses(st *grammar.Stmt) error {
	dir := st.Dir
	for _, c := range st.Clauses {
		if why, refused := dirRefusals[c.Kind]; refused {
			return fmt.Errorf("%s cannot apply to %s: %s", c.Kind, dir, why)
		}
		if c.Kind == grammar.AddMCP {
			for _, v := range append(append([]grammar.Var(nil), c.MCP.Env...), c.MCP.Headers...) {
				if v.Ref != "" {
					return fmt.Errorf("ADD MCP SERVER %s cannot apply to %s: its %s takes a secret reference, which only cpb's launcher resolves", c.Names[0], dir, v.Key)
				}
			}
		}
	}
	return nil
}

func dirStatement(r *stmtRun, st *grammar.Stmt) error {
	if err := validateDirClauses(st); err != nil {
		return err
	}
	dir := st.Dir
	key := dirMark + dir
	unlock, err := r.lockRegistry()
	if err != nil {
		return err
	}
	defer unlock()

	// Plan everything before writing anything.
	var steps []pluginStep
	var lines []string
	if plansPluginCommands(st.Clauses) {
		w := r.dryWorld(key)
		if w == nil {
			if w, err = readPluginWorld(dir); err != nil {
				return err
			}
			r.keepWorld(key, w)
		}
		var pl []string
		if steps, pl, err = planPlugins(w, st.Clauses); err != nil {
			return err
		}
		if err := checkClaudeForPlugins(steps); err != nil {
			return err
		}
		lines = append(lines, pl...)
	}
	mcpChange := false
	if mcpClauses(st.Clauses) {
		state, err := r.mcpState(key, dir)
		if err != nil {
			return err
		}
		mp, err := planMCP(state, nil, st.Clauses)
		if err != nil {
			return err
		}
		steps = append(steps, mp.steps...)
		lines = append(lines, mp.lines...)
		mcpChange = len(mp.steps) > 0
	}
	sf, err := settings.Load(dir)
	if err != nil {
		return err
	}
	if r.dry != nil {
		if raw, ok := r.dry.settings[key]; ok {
			if sf.Root, err = settings.ParseObject(raw); err != nil {
				return err
			}
		}
	}
	r.warnMarketplaceRef(st.Clauses)
	slKey, _ := filepath.Abs(dir)
	slp, err := r.planSLHistory(slKey, dir, st.Clauses)
	if err != nil {
		return err
	}
	setLines, setChange, err := applySettingsWithHistory(sf, st.Clauses, slp, true)
	if err != nil {
		return err
	}
	envLines, envChange, err := applyDirEnv(sf, st.Clauses)
	if err != nil {
		return err
	}

	var skills *skillPlan
	recs, err := dirSkillRecords(dir)
	if err != nil {
		return err
	}
	if r.dry != nil {
		if rec, ok := r.dry.skillRecords[key]; ok {
			recs = rec
		}
	}
	before := cloneSkills(recs)
	after := before
	if skillClauses(st.Clauses) {
		if skills, err = planSkills(dir, before, r.skillKnown(key), st.Clauses); err != nil {
			return err
		}
		lines = append(lines, skills.lines...)
		after = cloneSkills(before)
		for n, rec := range skills.records {
			if rec == nil {
				delete(after, n)
				continue
			}
			if after == nil {
				after = map[string]*manifest.SkillRecord{}
			}
			c := *rec
			after[n] = &c
		}
	}
	skillChange := skills != nil && (len(skills.ops) > 0 || !reflect.DeepEqual(before, after))
	if skills != nil {
		// Skill operations run in clause order with the commands.
		steps = append(steps, skills.steps()...)
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].clause < steps[j].clause })
	settingsChange := setChange || envChange
	if len(steps) == 0 && !settingsChange && !skillChange {
		r.outcome = outUnchanged
		r.say(stmtHead(st)+" unchanged", lines)
		return nil
	}
	r.outcome = outChanged

	backups := dirBackupPlan(r, dir, settingsChange || len(steps) > 0, mcpChange)
	if r.dryRun {
		var what []string
		for _, b := range backups {
			what = append(what, b.what())
			r.actions = append(r.actions, planAction{Type: "backup", Path: b.src, To: b.dst})
		}
		noRefs := func(string) string { return "" } // a plain directory takes no reference
		for _, s := range steps {
			what = append(what, s.command())
			r.actions = append(r.actions, stepActions(dir, s, noRefs)...)
		}
		if settingsChange {
			what = append(what, "write "+filepath.Join(dir, settings.FileName))
			r.actions = append(r.actions, planAction{Type: "write", Path: filepath.Join(dir, settings.FileName)})
		}
		if skills != nil {
			r.recordSkills(key, after, skills)
		}
		if settingsChange && r.dry != nil {
			r.dry.settings[key], _ = sf.Root.MarshalJSON()
			if slp.changed {
				_ = r.commitSLHistory(slp.key, slp.after)
			}
		}
		r.note = "would run: " + strings.Join(what, "; ")
		return nil
	}

	for _, b := range backups {
		if err := b.run(); err != nil {
			return fmt.Errorf("cannot back up %s, nothing was changed: %w", b.src, err)
		}
		lines = append(lines, b.done())
	}
	var cur map[string]*manifest.SkillRecord
	if skills != nil {
		// Each operation is recorded as soon as it is done; one whose record
		// cannot be written is taken away again.
		cur = cloneSkills(before)
		if cur == nil {
			cur = map[string]*manifest.SkillRecord{}
		}
		write := func(recs map[string]*manifest.SkillRecord) error { return writeDirSkillRecords(dir, recs) }
		for i := range skills.ops {
			op := &skills.ops[i]
			op.record = func() error { return recordSkillOp(op, cur, write) }
		}
	}
	ran, err := runPluginSteps(dir, steps)
	lines = append(lines, ran...)
	if err == nil && settingsChange {
		// Loaded again: the commands above may have rewritten it.
		if sf, err = settings.Load(dir); err == nil {
			if _, _, err = applySettingsWithHistory(sf, st.Clauses, slp, false); err == nil {
				if _, _, err = applyDirEnv(sf, st.Clauses); err == nil {
					if err = sf.Write(); err == nil && slp.changed {
						if herr := r.commitSLHistory(slp.key, slp.after); herr != nil {
							err = fmt.Errorf("the status line is set, but its history could not be recorded: %w", herr)
						}
					}
				}
			}
		}
		if err == nil {
			lines = append(append(lines, setLines...), envLines...)
		}
	}
	if err == nil && skills != nil && !reflect.DeepEqual(cloneSkills(cur), after) {
		// Records that change without a file operation (a DROP of a skill
		// already gone).
		err = writeDirSkillRecords(dir, after)
	}
	if err != nil {
		if len(lines) > 0 {
			r.say("Partly altered "+dir, lines)
		}
		return err
	}
	r.say("Altered "+dir, lines)
	return nil
}

// applyDirEnv applies SET VAR / UNSET VAR to the env map of a plain
// directory's settings.json (Claude Code's own per-install variables).
func applyDirEnv(f *settings.File, clauses []grammar.Clause) ([]string, bool, error) {
	var lines []string
	changed := false
	env, err := f.Root.Object("env")
	if err != nil {
		return nil, false, err
	}
	for _, c := range clauses {
		switch c.Kind {
		case grammar.SetVar:
			for _, v := range c.Vars {
				var cur string
				if ok, _ := env.Get(v.Key, &cur); ok && cur == v.Value {
					continue
				}
				if err := env.Set(v.Key, v.Value); err != nil {
					return nil, false, err
				}
				changed = true
				lines = append(lines, "set       "+v.Key)
			}
		case grammar.UnsetVar:
			for _, k := range c.Keys {
				if env.Delete(k) {
					changed = true
					lines = append(lines, "unset     "+k)
				}
			}
		}
	}
	if changed {
		f.Root.SetObject("env", env)
	}
	return lines, changed, nil
}

// dirBackup is one backup a run makes before its first write to a file.
type dirBackup struct {
	src, dst string
}

func (b dirBackup) what() string { return "back up " + b.src }
func (b dirBackup) done() string {
	return "backed up " + filepath.Base(b.src) + " to " + filepath.Base(b.dst)
}
func (b dirBackup) run() error {
	data, err := os.ReadFile(b.src)
	if err != nil {
		return err
	}
	info, err := os.Stat(b.src)
	if err != nil {
		return err
	}
	return os.WriteFile(b.dst, data, info.Mode().Perm())
}

// dirBackupPlan lists the backups due before this statement writes: each
// file once per run, and only a file that exists (the clause creates one
// that does not). A file is marked when first planned, in a dry run too, so
// a later statement of the run neither backs it up again nor backs up the
// file an earlier statement created.
func dirBackupPlan(r *stmtRun, dir string, settingsWrite, mcpWrite bool) []dirBackup {
	stamp := time.Now().Format("2006-01-02-15_04_05")
	var out []dirBackup
	add := func(name string) {
		src := filepath.Join(dir, name)
		if r.backedUp[src] {
			return
		}
		r.markBackedUp(src)
		if _, err := os.Stat(src); err != nil {
			return
		}
		out = append(out, dirBackup{src: src, dst: src + ".cpb-backup-" + stamp})
	}
	if settingsWrite {
		add(settings.FileName)
	}
	if mcpWrite {
		add(claudeJSON)
	}
	return out
}

func (r *stmtRun) markBackedUp(src string) {
	if r.backedUp == nil {
		r.backedUp = map[string]bool{}
	}
	r.backedUp[src] = true
}

func (r *stmtRun) dryWorld(key string) *pluginWorld {
	if r.dry == nil {
		return nil
	}
	return r.dry.worlds[key]
}

func (r *stmtRun) keepWorld(key string, w *pluginWorld) {
	if r.dry != nil {
		r.dry.worlds[key] = w
	}
}

// The skills cpb added to plain directories are recorded in cpb's own
// state, never inside the directory: <playbooks root>/.state/dirs.toml,
// keyed by the directory's absolute path.

type dirStateFile struct {
	Dirs map[string]*dirStateEntry `toml:"dirs"`
}

type dirStateEntry struct {
	Skills map[string]*manifest.SkillRecord `toml:"skills"`
}

func dirStatePath() string {
	return filepath.Join(config.ResolvePlaybooksDir(), ".state", "dirs.toml")
}

func readDirState() (*dirStateFile, error) {
	st := &dirStateFile{Dirs: map[string]*dirStateEntry{}}
	data, err := os.ReadFile(dirStatePath())
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := toml.Decode(string(data), st); err != nil {
		return nil, fmt.Errorf("%s: %s", dirStatePath(), manifest.SanitizeTOMLError(err))
	}
	if st.Dirs == nil {
		st.Dirs = map[string]*dirStateEntry{}
	}
	for d, e := range st.Dirs {
		if e == nil {
			continue
		}
		for n, rec := range e.Skills {
			if manifest.ValidateProfileName(n) != nil || rec == nil || (rec.Mode != "link" && rec.Mode != "copy") {
				return nil, fmt.Errorf("%s: an invalid skill record %q for %s", dirStatePath(), n, d)
			}
		}
	}
	return st, nil
}

func dirSkillRecords(dir string) (map[string]*manifest.SkillRecord, error) {
	st, err := readDirState()
	if err != nil {
		return nil, err
	}
	if e := st.Dirs[dir]; e != nil {
		return cloneSkills(e.Skills), nil
	}
	return nil, nil
}

func writeDirSkillRecords(dir string, recs map[string]*manifest.SkillRecord) error {
	st, err := readDirState()
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		delete(st.Dirs, dir)
	} else {
		st.Dirs[dir] = &dirStateEntry{Skills: cloneSkills(recs)}
	}
	path := dirStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	if err := toml.NewEncoder(&b).Encode(st); err != nil {
		return err
	}
	return settings.WriteAtomic(path, []byte(b.String()), 0o644)
}

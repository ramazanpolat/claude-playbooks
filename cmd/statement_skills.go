package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// Skills (docs/reference/cli-grammar.md, "Skills"): ADD SKILL puts a skill
// directory at <config>/skills/<name>, a directory by a link (a skill under
// development: edits reach the next session) and a git source by a copy (a
// pinned artifact that survives the source moving). The manifest records
// each one, so DROP SKILL removes only what cpb added and update restores
// it. Claude Code has no CLI that installs a skill.

func skillClauses(clauses []grammar.Clause) bool {
	for _, c := range clauses {
		if c.Kind == grammar.AddSkill || c.Kind == grammar.DropSkill {
			return true
		}
	}
	return false
}

// skillOp is one file operation a statement makes under skills/.
type skillOp struct {
	what string       // what a dry run reports
	line string       // the report line once done
	do   func() error // the operation
}

type skillPlan struct {
	ops     []skillOp
	lines   []string
	records map[string]*manifest.SkillRecord // nil value: forget the record
}

func skillPath(configDir, name string) string {
	return filepath.Join(configDir, "skills", name)
}

func expandSkillPath(p string) (string, error) {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[2:])
	}
	return filepath.Clean(p), nil
}

// gitURL is the URL git clones for a skill source.
func gitURL(src string) string {
	if strings.HasPrefix(src, "github:") {
		return "https://github.com/" + strings.TrimPrefix(src, "github:")
	}
	return src
}

// planSkills plans the skill clauses against what is on disk. rec is the
// record as the run sees it; known reports that a dry run's earlier
// statements decided a skill's state (true: present and recorded, false:
// absent), overriding the disk.
func planSkills(configDir string, rec map[string]*manifest.SkillRecord, known map[string]bool, clauses []grammar.Clause) (*skillPlan, error) {
	p := &skillPlan{records: map[string]*manifest.SkillRecord{}}
	recordOf := func(name string) *manifest.SkillRecord {
		if r, ok := p.records[name]; ok {
			return r
		}
		return rec[name]
	}
	exists := func(name string) bool {
		if present, ok := known[name]; ok {
			return present
		}
		if configDir == "" {
			return false
		}
		_, err := os.Lstat(skillPath(configDir, name))
		return err == nil
	}
	for _, c := range clauses {
		name := ""
		if len(c.Names) > 0 {
			name = c.Names[0]
		}
		switch c.Kind {
		case grammar.AddSkill:
			kind, err := grammar.SkillSource(c.Skill.From)
			if err != nil {
				return nil, fmt.Errorf("ADD SKILL %s: %w", name, err)
			}
			old := recordOf(name)
			if exists(name) && old == nil {
				return nil, fmt.Errorf("ADD SKILL %s: skills/%s exists and cpb did not add it; remove it yourself, or pick another name", name, name)
			}
			want := &manifest.SkillRecord{Source: c.Skill.From, Branch: c.Skill.Branch, Subdir: c.Skill.Subdir, Mode: "copy"}
			if kind == grammar.SkillDirectory {
				want.Mode = "link"
				dir, err := expandSkillPath(c.Skill.From)
				if err != nil {
					return nil, err
				}
				if info, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil || !info.Mode().IsRegular() {
					return nil, fmt.Errorf("ADD SKILL %s: %s is not a skill: it has no SKILL.md", name, dir)
				}
			}
			p.records[name] = want
			if old != nil && *old == *want && exists(name) && (want.Mode != "link" || linkPointsTo(configDir, name, want.Source)) {
				continue // already true
			}
			op := skillOp{line: "skill     " + name + " (" + want.Mode + " of " + want.Source + ")"}
			if want.Mode == "link" {
				op.what = "link skills/" + name + " to " + want.Source
			} else {
				op.what = "copy skills/" + name + " from " + want.Source
			}
			if configDir != "" {
				op.do = func() error { return putSkill(configDir, name, old, want) }
			}
			p.ops = append(p.ops, op)
		case grammar.DropSkill:
			old := recordOf(name)
			if old == nil {
				if exists(name) {
					return nil, fmt.Errorf("DROP SKILL %s: skills/%s was not added by cpb; it is left alone", name, name)
				}
				p.lines = append(p.lines, "skill "+name+" was not added")
				continue
			}
			p.records[name] = nil
			if !exists(name) {
				continue
			}
			op := skillOp{what: "remove skills/" + name, line: "dropped   skill " + name}
			if configDir != "" {
				op.do = func() error { return removeSkill(configDir, name, old) }
			}
			p.ops = append(p.ops, op)
		}
	}
	return p, nil
}

func linkPointsTo(configDir, name, source string) bool {
	if configDir == "" {
		return true
	}
	want, err := expandSkillPath(source)
	if err != nil {
		return false
	}
	got, err := os.Readlink(skillPath(configDir, name))
	return err == nil && filepath.Clean(got) == want
}

// putSkill puts a skill in place, replacing the one cpb recorded before.
func putSkill(configDir, name string, old, want *manifest.SkillRecord) error {
	target := skillPath(configDir, name)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if want.Mode == "link" {
		dir, err := expandSkillPath(want.Source)
		if err != nil {
			return err
		}
		if old != nil {
			if err := removeSkill(configDir, name, old); err != nil {
				return err
			}
		}
		return os.Symlink(dir, target)
	}
	// A git source: clone, check, copy beside the target, then swap.
	work, cleanup, err := stageSource(io.Discard, gitURL(want.Source), true, want.Branch, want.Subdir)
	if err != nil {
		return fmt.Errorf("skill %s: %w", name, err)
	}
	defer cleanup()
	if info, err := os.Stat(filepath.Join(work, "SKILL.md")); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("skill %s: the source has no SKILL.md at its root (or SUBDIR)", name)
	}
	tmp := target + ".cpb-new"
	_ = os.RemoveAll(tmp)
	if err := copyDir(work, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	_ = os.RemoveAll(filepath.Join(tmp, ".git"))
	if old != nil {
		if err := removeSkill(configDir, name, old); err != nil {
			_ = os.RemoveAll(tmp)
			return err
		}
	}
	return os.Rename(tmp, target)
}

// removeSkill removes a skill cpb recorded, and refuses one that was
// replaced by hand since (a directory where a link was, or the reverse).
func removeSkill(configDir, name string, old *manifest.SkillRecord) error {
	target := skillPath(configDir, name)
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	isLink := info.Mode()&os.ModeSymlink != 0
	switch {
	case old.Mode == "link" && isLink:
		return os.Remove(target)
	case old.Mode == "copy" && !isLink && info.IsDir():
		return os.RemoveAll(target)
	}
	return fmt.Errorf("skills/%s is no longer what cpb put there (a %s was recorded); it is left alone", name, old.Mode)
}

// restoreSkills puts back every recorded skill after an update's overlay:
// a link is re-made, a copy refreshed from its source.
func restoreSkills(w io.Writer, configDir string, records map[string]*manifest.SkillRecord) error {
	names := make([]string, 0, len(records))
	for n, r := range records {
		if r != nil {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		r := records[n]
		if r.Mode == "link" && linkPointsTo(configDir, n, r.Source) {
			continue
		}
		// What the overlay left there is replaced only if it is cpb's kind
		// of entry; anything else is refused, as ADD SKILL would.
		if err := putSkill(configDir, n, r, r); err != nil {
			return err
		}
		fmt.Fprintf(w, "Restored skill %s (%s of %s)\n", n, r.Mode, r.Source)
	}
	return nil
}

// skillJSON is one skill as SHOW prints it.
type skillJSON struct {
	Name   string  `json:"name"`
	Source string  `json:"source"`
	Branch *string `json:"branch"`
	Subdir *string `json:"subdir"`
	Mode   string  `json:"mode"`
}

func describeSkills(m *manifest.Manifest) []skillJSON {
	out := []skillJSON{}
	if m == nil {
		return out
	}
	names := make([]string, 0, len(m.Skills))
	for n, r := range m.Skills {
		if r != nil {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		r := m.Skills[n]
		out = append(out, skillJSON{Name: n, Source: r.Source, Branch: optStr(r.Branch), Subdir: optStr(r.Subdir), Mode: r.Mode})
	}
	return out
}

func skillCreateClauses(m *manifest.Manifest) []grammar.Clause {
	var out []grammar.Clause
	for _, s := range describeSkills(m) {
		sk := &grammar.Skill{From: s.Source}
		if s.Branch != nil {
			sk.Branch = *s.Branch
		}
		if s.Subdir != nil {
			sk.Subdir = *s.Subdir
		}
		out = append(out, grammar.Clause{Kind: grammar.AddSkill, Names: []string{s.Name}, Skill: sk})
	}
	return out
}

func cloneSkills(in map[string]*manifest.SkillRecord) map[string]*manifest.SkillRecord {
	if in == nil {
		return nil
	}
	out := make(map[string]*manifest.SkillRecord, len(in))
	for k, v := range in {
		if v != nil {
			c := *v
			out[k] = &c
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

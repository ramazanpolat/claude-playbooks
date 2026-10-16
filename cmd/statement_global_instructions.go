package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/settings"
)

// The global_instructions property. Claude Code walks from the working
// directory up through its ancestors loading CLAUDE.md files, and
// $HOME/.claude, the bare install, is one of them: its CLAUDE.md and rules/
// load into every playbook run under $HOME (measured on Claude Code
// 2.1.296). global_instructions = 'excluded' is one claudeMdExcludes entry
// in the playbook's settings.json, <HOME>/.claude/**, which every launch
// path reads. It is absolute: Claude Code does not expand a tilde there. The
// state is read back from the file, never recorded twice.

const keyClaudeMdExcludes = "claudeMdExcludes"

// machineGlobalExclude is this machine's entry: <HOME>/.claude/**.
func machineGlobalExclude() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude") + "/**", nil
}

// inMachineConfig reports whether dir is ~/.claude or inside it, where the
// exclude would hide the install's own instructions.
func inMachineConfig(dir string) bool {
	home, err := os.UserHomeDir()
	if err != nil || dir == "" {
		return false
	}
	base, d := filepath.Join(home, ".claude"), filepath.Clean(dir)
	if r, err := filepath.EvalSymlinks(base); err == nil {
		base = r
	}
	if r, err := filepath.EvalSymlinks(d); err == nil {
		d = r
	}
	return d == base || strings.HasPrefix(d, base+string(filepath.Separator))
}

// otherHomeExclude matches the entry another machine's home would have
// written: a playbook copied from elsewhere carries it, and it excludes
// nothing here.
var otherHomeExclude = regexp.MustCompile(`^/(Users|home)/[^/]+/\.claude/\*\*$|^/root/\.claude/\*\*$`)

// globalInstructionsState reads the global_instructions setting from a
// settings.json: excluded when its claudeMdExcludes holds this machine's
// entry, inherited otherwise. other is an entry for another home's
// ~/.claude, when the state is inherited and one is there.
func globalInstructionsState(root *settings.Object) (state, other string) {
	state = "inherited"
	if root == nil {
		return state, ""
	}
	entry, err := machineGlobalExclude()
	var list []string
	if _, gerr := root.Get(keyClaudeMdExcludes, &list); gerr != nil {
		return state, ""
	}
	for _, e := range list {
		switch {
		case err == nil && e == entry:
			return "excluded", ""
		case otherHomeExclude.MatchString(e):
			other = e
		}
	}
	return state, other
}

// globalInstructionsStateOf reads the global_instructions property of a
// config directory.
func globalInstructionsStateOf(dir string) (state, other string) {
	sf, err := settings.Load(dir)
	if err != nil {
		return "inherited", ""
	}
	return globalInstructionsState(sf.Root)
}

// applyGlobalInstructions sets the global_instructions setting in a
// settings.json: excluded adds this machine's entry to claudeMdExcludes,
// inherited removes it. Every other entry stays, and a list left empty is
// removed. It is refused for the machine's own ~/.claude, whose CLAUDE.md is
// that install's own instructions.
func applyGlobalInstructions(f *settings.File, value string) (line string, changed bool, err error) {
	if f.Path != "" && inMachineConfig(filepath.Dir(f.Path)) {
		return "", false, fmt.Errorf("global_instructions = '%s' cannot apply to %s: it is the machine's own Claude Code configuration, and its CLAUDE.md is its own instructions", value, filepath.Dir(f.Path))
	}
	entry, err := machineGlobalExclude()
	if err != nil {
		return "", false, err
	}
	var list []string
	if f.Root.Has(keyClaudeMdExcludes) {
		if _, err := f.Root.Get(keyClaudeMdExcludes, &list); err != nil {
			return "", false, fmt.Errorf("%s: %s is not a list of strings; fix it by hand first", f.Path, keyClaudeMdExcludes)
		}
	}
	has := slices.Contains(list, entry)
	switch value {
	case "excluded":
		if has {
			return "", false, nil
		}
		if err := f.Root.Set(keyClaudeMdExcludes, append(list, entry)); err != nil {
			return "", false, err
		}
		return "global_instructions excluded: ~/.claude's CLAUDE.md and rules are not loaded", true, nil
	case "inherited":
		if !has {
			return "", false, nil
		}
		list = slices.DeleteFunc(list, func(e string) bool { return e == entry })
		if len(list) == 0 {
			f.Root.Delete(keyClaudeMdExcludes)
		} else if err := f.Root.Set(keyClaudeMdExcludes, list); err != nil {
			return "", false, err
		}
		return "global_instructions inherited: ~/.claude's CLAUDE.md and rules load again", true, nil
	}
	return "", false, fmt.Errorf("global_instructions takes 'excluded' or 'inherited'")
}

// writeGlobalInstructions applies the global_instructions setting to dir's
// settings.json and writes it when it changed: CREATE's default and its SET
// global_instructions.
func writeGlobalInstructions(dir, value string) error {
	sf, err := settings.Load(dir)
	if err != nil {
		return err
	}
	if _, changed, err := applyGlobalInstructions(sf, value); err != nil || !changed {
		return err
	}
	return sf.Write()
}

// globalInstructionsLine is SHOW's and EXPLAIN's line for the
// global_instructions property.
func globalInstructionsLine(state, other string) string {
	if state == "excluded" {
		return "Global instructions: excluded: ~/.claude's CLAUDE.md and rules are not loaded (SET global_instructions = 'inherited' loads them)"
	}
	line := "Global instructions: inherited: ~/.claude's CLAUDE.md and rules load into it (SET global_instructions = 'excluded' keeps them out)"
	if other != "" {
		line += "; its settings.json excludes " + other + ", another home's, which excludes nothing here"
	}
	return line
}

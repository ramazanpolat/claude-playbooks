package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/settings"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

// Which launches get the status line step (SPEC.md, "Status line and
// model"). Only the playbook's own status line is wrapped: the one in its
// config directory's settings.json. A status line in a file of the working
// folder is a folder's, which Claude Code runs only once the folder is
// trusted; cpb never copies it into a --settings of its own, where it would
// run at once. So when any folder-level file Claude Code reads sets
// statusLine, or managed settings do, the launch gets no step and Claude
// Code's own order applies untouched. Those files are read for the key
// alone.

// statuslineStep is what a launch does with the status line: the wrapped
// statusLine it passes, or why none.
type statuslineStep struct {
	wrapped *settings.Object
	reason  string
}

// Reasons a launch records no usage, as EXPLAIN names them.
const (
	stepSkipNone        = "the playbook has no status line of its own (statusline.command)"
	stepSkipSettingsArg = "the launch passes its own --settings"
	stepSkipNotCommand  = "the playbook's status line is not a command"
)

// managedSettingsPaths are where Claude Code reads managed settings, which
// outrank a launch's --settings.
var managedSettingsPaths = func() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/Library/Application Support/ClaudeCode/managed-settings.json"}
	case "linux":
		return []string{"/etc/claude-code/managed-settings.json"}
	}
	return nil
}

// folderSettingsFiles are the folder-level settings files Claude Code reads
// for a session started in wd: the folder's own, its repository's root,
// and the main checkout of a worktree, each .claude/settings.json and
// .claude/settings.local.json.
func folderSettingsFiles(wd string) []string {
	dirs := []string{wd}
	for d := wd; ; d = filepath.Dir(d) {
		gi, err := os.Lstat(filepath.Join(d, ".git"))
		if err == nil {
			dirs = append(dirs, d)
			if gi.Mode().IsRegular() {
				if main := mainCheckoutOf(filepath.Join(d, ".git")); main != "" {
					dirs = append(dirs, main)
				}
			}
			break
		}
		if p := filepath.Dir(d); p == d {
			break
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, d := range dirs {
		for _, f := range []string{"settings.json", "settings.local.json"} {
			p := filepath.Join(d, ".claude", f)
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// mainCheckoutOf reads a worktree's .git file (gitdir: <main>/.git/worktrees/<n>)
// for the main checkout; "" when it does not say.
func mainCheckoutOf(gitFile string) string {
	b, err := os.ReadFile(gitFile)
	if err != nil {
		return ""
	}
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return ""
	}
	dir = strings.TrimSpace(dir)
	i := strings.Index(dir, string(filepath.Separator)+".git"+string(filepath.Separator)+"worktrees"+string(filepath.Separator))
	if i < 0 {
		return ""
	}
	return dir[:i]
}

// setsStatusLine reports whether a settings file sets statusLine; err when
// it cannot be read or parsed (then cpb cannot tell, and adds no step).
func setsStatusLine(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return false, err
	}
	_, ok := top["statusLine"]
	return ok, nil
}

// statuslineStepFor decides the step of a launch of configDir from wd with
// claudeArgs, whose launch record is named launchID.
func statuslineStepFor(root, configDir, wd, launchID string, claudeArgs []string) statuslineStep {
	if hasSettingsArg(claudeArgs) {
		return statuslineStep{reason: stepSkipSettingsArg}
	}
	sf, err := settings.Load(configDir)
	if err != nil {
		return statuslineStep{reason: "the playbook's " + settings.FileName + " cannot be read"}
	}
	if !sf.Root.Has("statusLine") {
		return statuslineStep{reason: stepSkipNone}
	}
	sl, err := sf.Root.Object("statusLine")
	if err != nil {
		return statuslineStep{reason: stepSkipNotCommand}
	}
	var typ, command string
	_, _ = sl.Get("type", &typ)
	_, _ = sl.Get("command", &command)
	if typ != "command" || strings.TrimSpace(command) == "" {
		return statuslineStep{reason: stepSkipNotCommand}
	}
	for _, p := range managedSettingsPaths() {
		if set, _ := setsStatusLine(p); set {
			return statuslineStep{reason: "managed settings set a status line (" + p + "), which outranks the playbook's"}
		}
	}
	for _, p := range folderSettingsFiles(wd) {
		set, err := setsStatusLine(p)
		if err != nil {
			return statuslineStep{reason: p + " cannot be read, so cpb cannot tell whether it sets a status line"}
		}
		if set {
			return statuslineStep{reason: p + " sets a status line of the folder's, which Claude Code runs as it is"}
		}
	}
	self, err := os.Executable()
	if err != nil {
		return statuslineStep{reason: "cpb cannot find its own binary"}
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	wrapped := settings.NewObject()
	for _, k := range sl.Keys() {
		_ = wrapped.Set(k, sl.Raw(k))
	}
	_ = wrapped.Set("command", stepCommand(self, root, launchID, command))
	return statuslineStep{wrapped: wrapped}
}

// stepCommand is the status line command of a launch: cpb's step, then the
// playbook's own command as its last argument, every part quoted for the
// shell Claude Code runs it with.
func stepCommand(self, root, launchID, original string) string {
	return shell.QuoteArg(self) + " " + statuslineStepArg + " --root " + shell.QuoteArg(root) + " --launch " + launchID + " -- " + shell.QuoteArg(original)
}

// explainLine is EXPLAIN's line for the step.
func (s statuslineStep) explainLine() string {
	if s.wrapped != nil {
		return "Usage at launch: recorded by cpb's status line step (the playbook's own status line runs after it)"
	}
	return "Usage at launch: not recorded (" + s.reason + ")"
}

// Package shell manages playbook aliases in the user's shell config file.
//
// Aliases are plain single-line `alias` definitions — no comment markers, no
// metadata, no registry. Discovery works by grepping lines for either the
// alias name or the CLAUDE_CONFIG_DIR path.
package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
)

// AliasEntry represents a single alias line found in the shell config.
type AliasEntry struct {
	AliasName string // e.g. "experiment"
	Line      string // the full alias line as written
	Path      string // the CLAUDE_CONFIG_DIR value (absolute, expanded)
}

// aliasRegex matches: [whitespace] alias name = ... CLAUDE_CONFIG_DIR=<path> ...
// Tolerates leading whitespace and any quote style around the command.
var aliasRegex = regexp.MustCompile(`^\s*alias\s+([A-Za-z_][A-Za-z0-9_-]*)\s*=`)
var aliasNameRegex = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// QuoteArg renders s as a single shell word. Use it for any value interpolated
// into a command shown to the user: a playbook name may legally contain spaces
// and shell metacharacters, and an unquoted suggestion is both wrong to paste
// and dangerous if pasted.
func QuoteArg(s string) string { return shellQuote(s) }

// ValidAliasName reports whether aliasName is a portable shell alias name.
func ValidAliasName(aliasName string) bool {
	return aliasNameRegex.MatchString(aliasName)
}

// ReloadHint returns the command a user should run to load configFile into
// their current shell. The verb follows the user's shell, not the filename:
// bash/zsh/fish get the familiar `source`; every other shell (sh, dash, ksh,
// unknown) gets the POSIX `.`, which they all support — a dash user pointing
// --shell-config at a custom path has no `source` builtin. The path is quoted
// so the hint stays pasteable when it contains whitespace or metacharacters.
func ReloadHint(configFile string) string {
	sh := config.UserShell()
	if strings.Contains(sh, "bash") || strings.Contains(sh, "zsh") || strings.Contains(sh, "fish") {
		return "source " + shellQuote(configFile)
	}
	// The POSIX `.` searches PATH — not the cwd — for names without a
	// slash, so a bare relative path must carry a ./ prefix to run.
	if !strings.Contains(configFile, "/") {
		configFile = "./" + configFile
	}
	return ". " + shellQuote(configFile)
}

// Format returns the canonical alias line written by the tool.
func Format(aliasName, playbookDir string) string {
	return formatWith(aliasName, playbookDir, currentBinName())
}

func currentBinName() string {
	binName := "claude-playbook"
	if len(os.Args) > 0 {
		baseBin := filepath.Base(os.Args[0])
		if baseBin != "." && baseBin != "/" {
			binName = baseBin
		}
	}
	return binName
}

// formatWith builds the canonical alias line for an explicit binary name. A line
// already on disk may name a different binary than this process (installed as
// `cpb`, later renamed via `claude-playbook`), and regenerating it must preserve
// whichever it was.
func formatWith(aliasName, playbookDir, binName string) string {
	playbookName := derivePlaybookName(playbookDir)
	body := fmt.Sprintf("CLAUDE_CONFIG_DIR=%s %s run %s", shellDoubleQuote(playbookDir), shellQuote(binName), shellQuote(playbookName))
	return fmt.Sprintf("alias %s=%s", aliasName, shellQuote(body))
}

// canonicalBinRegex pulls the binary name out of a canonically generated line.
// In that form the body is single-quoted as a whole, so its inner quotes appear
// escaped as '\”.
var canonicalBinRegex = regexp.MustCompile(`'\\''(.*?)'\\'' run `)

func derivePlaybookName(playbookDir string) string {
	playbooksDir := config.ResolvePlaybooksDir()
	rel, err := filepath.Rel(playbooksDir, playbookDir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return filepath.Base(playbookDir)
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	return parts[0]
}

// ReadAll scans the shell config for every alias whose definition contains
// CLAUDE_CONFIG_DIR=<path>. Returns one entry per matching line (there may
// be duplicates with the same path or alias name).
func ReadAll(configFile string) ([]AliasEntry, error) {
	lines, err := readLines(configFile)
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()

	var entries []AliasEntry
	for _, line := range lines {
		m := aliasRegex.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		idx := strings.Index(line, "CLAUDE_CONFIG_DIR=")
		if idx < 0 {
			continue
		}
		val := scanEnvValue(line[idx+len("CLAUDE_CONFIG_DIR="):])
		if val == "" {
			continue
		}
		if strings.HasPrefix(val, "~/") {
			val = filepath.Join(home, val[2:])
		} else if strings.HasPrefix(val, "$HOME/") {
			val = filepath.Join(home, val[len("$HOME/"):])
		}
		entries = append(entries, AliasEntry{
			AliasName: m[1],
			Line:      line,
			Path:      val,
		})
	}
	return entries, nil
}

// FindByPath returns aliases whose CLAUDE_CONFIG_DIR path equals the given path.
func FindByPath(configFile, path string) ([]AliasEntry, error) {
	entries, err := ReadAll(configFile)
	if err != nil {
		return nil, err
	}
	want, _ := filepath.Abs(path)
	var matches []AliasEntry
	for _, e := range entries {
		have, _ := filepath.Abs(e.Path)
		if have == want {
			matches = append(matches, e)
		}
	}
	return matches, nil
}

// FindByAliasName returns the first alias entry whose alias name matches.
func FindByAliasName(configFile, aliasName string) (*AliasEntry, error) {
	entries, err := ReadAll(configFile)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].AliasName == aliasName {
			return &entries[i], nil
		}
	}
	return nil, nil
}

// Write sets an alias: removes any existing lines for this alias name or this
// playbook path, then appends a fresh line. If the alias name is already in
// use by a different CLAUDE_CONFIG_DIR, it is silently overwritten.
func Write(configFile, aliasName, playbookDir string) error {
	if !ValidAliasName(aliasName) {
		return fmt.Errorf("invalid alias name %q", aliasName)
	}
	configFile = resolveConfigPath(configFile)
	return withConfigLock(configFile, func() error {
		lines, err := readLines(configFile)
		if err != nil {
			return err
		}

		absPlaybookDir, _ := filepath.Abs(playbookDir)
		kept, _ := dropMatchingLines(lines, func(line string) bool {
			return shouldDrop(line, aliasName, absPlaybookDir)
		})

		if len(kept) > 0 && kept[len(kept)-1] != "" {
			kept = append(kept, "")
		}
		playbookName := strings.NewReplacer("\r", " ", "\n", " ").Replace(derivePlaybookName(playbookDir))
		kept = append(kept, fmt.Sprintf("# claude-playbook: %s", playbookName))
		kept = append(kept, Format(aliasName, playbookDir))
		return writeLines(configFile, kept)
	})
}

// RemoveByPath deletes every alias line whose CLAUDE_CONFIG_DIR matches the given path.
// Returns the number of lines removed.
func RemoveByPath(configFile, playbookDir string) (int, error) {
	configFile = resolveConfigPath(configFile)
	removed := 0
	err := withConfigLock(configFile, func() error {
		lines, err := readLines(configFile)
		if err != nil {
			return err
		}
		absPlaybookDir, _ := filepath.Abs(playbookDir)
		kept, n := dropMatchingLines(lines, func(line string) bool {
			return matchesPath(line, absPlaybookDir)
		})
		removed = n
		if removed == 0 {
			return nil
		}
		return writeLines(configFile, kept)
	})
	return removed, err
}

// RemoveByAliasName deletes every alias line whose alias name matches.
// Returns the number of lines removed.
func RemoveByAliasName(configFile, aliasName string) (int, error) {
	configFile = resolveConfigPath(configFile)
	removed := 0
	err := withConfigLock(configFile, func() error {
		lines, err := readLines(configFile)
		if err != nil {
			return err
		}
		kept, n := dropMatchingLines(lines, func(line string) bool {
			return matchesAliasName(line, aliasName)
		})
		removed = n
		if removed == 0 {
			return nil
		}
		return writeLines(configFile, kept)
	})
	return removed, err
}

// RemoveByPathPrefix deletes every alias line whose CLAUDE_CONFIG_DIR starts
// with the given prefix (used when deleting a container to clean up all nested
// playbook aliases).
func RemoveByPathPrefix(configFile, prefix string) (int, error) {
	configFile = resolveConfigPath(configFile)
	removed := 0
	err := withConfigLock(configFile, func() error {
		lines, err := readLines(configFile)
		if err != nil {
			return err
		}
		absPrefix, _ := filepath.Abs(prefix)
		if !strings.HasSuffix(absPrefix, string(filepath.Separator)) {
			absPrefix += string(filepath.Separator)
		}
		kept, n := dropMatchingLines(lines, func(line string) bool {
			return matchesPathPrefix(line, absPrefix)
		})
		removed = n
		if removed == 0 {
			return nil
		}
		return writeLines(configFile, kept)
	})
	return removed, err
}

func dropMatchingLines(lines []string, matchFn func(line string) bool) ([]string, int) {
	var kept []string
	removed := 0
	n := len(lines)
	for i := 0; i < n; i++ {
		if matchFn(lines[i]) {
			removed++
			if len(kept) > 0 && strings.HasPrefix(strings.TrimSpace(kept[len(kept)-1]), "# claude-playbook:") {
				kept = kept[:len(kept)-1]
			}
			continue
		}
		kept = append(kept, lines[i])
	}
	return kept, removed
}

// RewritePathPrefix updates every alias line whose CLAUDE_CONFIG_DIR starts
// with oldPrefix so it starts with newPrefix instead. Used by `rename`.
// RewritePathPrefix regenerates every canonical alias line for a playbook that
// moved. Non-canonical lines for that playbook are left untouched and returned
// in skipped, so the caller can tell the user to regenerate them.
func RewritePathPrefix(configFile, oldPrefix, newPrefix string) (int, []string, error) {
	configFile = resolveConfigPath(configFile)
	changed := 0
	var skipped []string
	err := withConfigLock(configFile, func() error {
		lines, err := readLines(configFile)
		if err != nil {
			return err
		}
		absOld, _ := filepath.Abs(oldPrefix)
		absNew, _ := filepath.Abs(newPrefix)
		for i, line := range lines {
			rewritten, ok, skip := rewriteLinePathPrefix(line, absOld, absNew)
			if skip != "" {
				skipped = append(skipped, skip)
			}
			if !ok {
				continue
			}
			lines[i] = rewritten
			changed++
			// Write emits a `# claude-playbook: <name>` marker above each alias.
			// It names the playbook too, so a rename leaves it stale for the same
			// reason the run argument was. Nothing reads the name (removal matches
			// the prefix alone), but a marker naming a playbook that no longer
			// exists is misleading in a file the user is invited to hand-edit.
			if i > 0 {
				lines[i-1] = rewriteMarkerComment(lines[i-1], newPlaybookName(rewritten))
			}
		}
		if changed == 0 {
			return nil
		}
		return writeLines(configFile, lines)
	})
	return changed, skipped, err
}

// --- internals ---

func shouldDrop(line, aliasName, absPlaybookDir string) bool {
	return matchesAliasName(line, aliasName) || matchesPath(line, absPlaybookDir)
}

func matchesAliasName(line, aliasName string) bool {
	m := aliasRegex.FindStringSubmatch(line)
	return m != nil && m[1] == aliasName
}

func matchesPath(line, absPath string) bool {
	if !aliasRegex.MatchString(line) {
		return false
	}
	have := extractPath(line)
	if have == "" {
		return false
	}
	abs, _ := filepath.Abs(have)
	return abs == absPath
}

func matchesPathPrefix(line, absPrefixWithSlash string) bool {
	if !aliasRegex.MatchString(line) {
		return false
	}
	have := extractPath(line)
	if have == "" {
		return false
	}
	abs, _ := filepath.Abs(have)
	abs += string(filepath.Separator)
	return strings.HasPrefix(abs, absPrefixWithSlash)
}

// rewriteLinePathPrefix regenerates an alias line for a moved playbook.
//
// Returns (line, true, "") when the line was regenerated; (line, false, "") when
// the line is not an alias for a path under absOld; and (line, false, aliasName)
// when it IS such an alias but is not in canonical form, so it was deliberately
// left untouched for the caller to report.
func rewriteLinePathPrefix(line, absOld, absNew string) (string, bool, string) {
	m := aliasRegex.FindStringSubmatch(line)
	if m == nil {
		return line, false, ""
	}
	have := extractPath(line)
	if have == "" {
		return line, false, ""
	}
	abs, _ := filepath.Abs(have)
	oldWithSlash := absOld + string(filepath.Separator)
	absWithSlash := abs + string(filepath.Separator)

	var newPath string
	switch {
	case abs == absOld:
		newPath = absNew
	case strings.HasPrefix(absWithSlash, oldWithSlash):
		newPath = absNew + strings.TrimPrefix(abs, absOld)
	default:
		return line, false, ""
	}

	// An alias line names the playbook TWICE: as CLAUDE_CONFIG_DIR and as the
	// `run <name>` argument. Rewriting one and not the other leaves a line that
	// resolves, launches, and dies with "unknown playbook".
	//
	// Both are regenerated together via formatWith, and ONLY for a line that is
	// byte-identical to what this package generated. Editing an arbitrary line in
	// place means splicing values into text that is already shell-encoded, and
	// every attempt at that produced a new defect: a `run <name>` search matching
	// inside the path, an unescaped name becoming a command injection, and
	// escaping that the path decoder could no longer read back. Regenerating
	// sidesteps all of it -- Format encodes correctly by construction.
	//
	// A line that is not in canonical form -- hand-edited to add Claude Code
	// flags, or written by hand -- is left exactly as it is and reported to the
	// caller, which tells the user to regenerate it with `alias`. Refusing is the
	// point: a wrong guess here silently corrupts the user's shell config.
	aliasName := m[1]
	binName := currentBinName()
	if bm := canonicalBinRegex.FindStringSubmatch(line); bm != nil {
		binName = bm[1]
	}
	// Compare against the path exactly as written. Write records whatever it was
	// given, so a relative --playbooks-dir produces a relative CLAUDE_CONFIG_DIR;
	// comparing against the absolutised form classified those genuinely canonical
	// lines as hand-edited, left them stale, and warned the user falsely.
	if line != formatWith(aliasName, have, binName) {
		return line, false, aliasName
	}
	// The regenerated line is absolute. A relative CLAUDE_CONFIG_DIR resolves
	// against the shell's working directory at invocation time, so it only worked
	// from one place; normalising here is a fix, not a side effect.
	return formatWith(aliasName, newPath, binName), true, ""
}

const markerPrefix = "# claude-playbook:"

// newPlaybookName derives the playbook name from an already-rewritten alias line.
func newPlaybookName(line string) string {
	p := extractPath(line)
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	return derivePlaybookName(abs)
}

// rewriteMarkerComment updates a `# claude-playbook: <name>` marker in place,
// preserving its original indentation. Any other line is returned untouched.
func rewriteMarkerComment(line, name string) string {
	if name == "" {
		return line
	}
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, markerPrefix) {
		return line
	}
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	safe := strings.NewReplacer("\r", " ", "\n", " ").Replace(name)
	return indent + markerPrefix + " " + safe
}

func extractPath(line string) string {
	idx := strings.Index(line, "CLAUDE_CONFIG_DIR=")
	if idx < 0 {
		return ""
	}
	val := scanEnvValue(line[idx+len("CLAUDE_CONFIG_DIR="):])
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(val, "~/") {
		val = filepath.Join(home, val[2:])
	} else if strings.HasPrefix(val, "$HOME/") {
		val = filepath.Join(home, val[len("$HOME/"):])
	}
	return val
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func shellDoubleQuote(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		`$`, `\$`,
		"`", "\\`",
	)
	return `"` + replacer.Replace(s) + `"`
}

func scanEnvValue(s string) string {
	if s == "" {
		return ""
	}
	switch s[0] {
	case '\'':
		return scanSingleQuoted(s[1:])
	case '"':
		return scanDoubleQuoted(s[1:])
	default:
		if end := strings.IndexAny(s, " \t'\""); end >= 0 {
			return s[:end]
		}
		return s
	}
}

func envValueEnd(s string) int {
	if s == "" {
		return 0
	}
	switch s[0] {
	case '\'':
		return quotedValueEnd(s, '\'')
	case '"':
		return quotedValueEnd(s, '"')
	default:
		if end := strings.IndexAny(s, " \t'\""); end >= 0 {
			return end
		}
		return len(s)
	}
}

func quotedValueEnd(s string, quote byte) int {
	escaped := false
	for i := 1; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if quote == '"' && c == '\\' {
			escaped = true
			continue
		}
		if c == quote {
			return i + 1
		}
	}
	return len(s)
}

func scanSingleQuoted(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			if strings.HasPrefix(s[i:], "'\\''") {
				b.WriteByte('\'')
				i += 3
				continue
			}
			return b.String()
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func scanDoubleQuoted(s string) string {
	var b strings.Builder
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			b.WriteByte(c)
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			return b.String()
		}
		b.WriteByte(c)
	}
	return b.String()
}

func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	content := string(data)
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, nil
}

func writeLines(path string, lines []string) error {
	content := strings.Join(lines, "\n") + "\n"
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".claude-playbook-shell-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func withConfigLock(configFile string, fn func() error) error {
	dir := filepath.Dir(configFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	lock, err := os.OpenFile(configFile+".claude-playbook.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn()
}

func resolveConfigPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	dir := filepath.Dir(path)
	if resolvedDir, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(resolvedDir, filepath.Base(path))
	}
	return path
}

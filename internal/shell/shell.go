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

// ValidAliasName reports whether aliasName is a portable shell alias name.
func ValidAliasName(aliasName string) bool {
	return aliasNameRegex.MatchString(aliasName)
}

// Format returns the canonical alias line written by the tool.
func Format(aliasName, playbookDir string) string {
	playbookName := derivePlaybookName(playbookDir)
	binName := "claude-playbook"
	if len(os.Args) > 0 {
		baseBin := filepath.Base(os.Args[0])
		if baseBin != "." && baseBin != "/" {
			binName = baseBin
		}
	}
	body := fmt.Sprintf("CLAUDE_CONFIG_DIR=%s %s run %s", shellDoubleQuote(playbookDir), shellQuote(binName), shellQuote(playbookName))
	return fmt.Sprintf("alias %s=%s", aliasName, shellQuote(body))
}

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
func RewritePathPrefix(configFile, oldPrefix, newPrefix string) (int, error) {
	configFile = resolveConfigPath(configFile)
	changed := 0
	err := withConfigLock(configFile, func() error {
		lines, err := readLines(configFile)
		if err != nil {
			return err
		}
		absOld, _ := filepath.Abs(oldPrefix)
		absNew, _ := filepath.Abs(newPrefix)
		for i, line := range lines {
			rewritten, ok := rewriteLinePathPrefix(line, absOld, absNew)
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
	return changed, err
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

func rewriteLinePathPrefix(line, absOld, absNew string) (string, bool) {
	if !aliasRegex.MatchString(line) {
		return line, false
	}
	have := extractPath(line)
	if have == "" {
		return line, false
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
		return line, false
	}

	// Replace the raw value of CLAUDE_CONFIG_DIR= in the line with the new absolute path.
	idx := strings.Index(line, "CLAUDE_CONFIG_DIR=")
	if idx < 0 {
		return line, false
	}
	prefix := line[:idx+len("CLAUDE_CONFIG_DIR=")]
	rest := line[idx+len("CLAUDE_CONFIG_DIR="):]
	end := envValueEnd(rest)
	after := rest[end:]

	// The path is only half of the alias. Its `run <name>` argument also names the
	// playbook, and a rename changes that name: leaving it stale yields a line that
	// points at the right directory and then asks to run a playbook that no longer
	// exists ("Error: unknown playbook"). The alias is dead until regenerated.
	//
	// Targeted rewrite rather than regenerating via Format, because README
	// documents hand-editing an alias to append Claude Code flags
	// (`... run work --model ... --permission-mode auto`) and regeneration would
	// silently discard them.
	//
	// Scoped to `after` -- the command text following the CLAUDE_CONFIG_DIR value --
	// never the whole line. A playbooks root whose own path contains " run <name>"
	// (e.g. "/tmp/team run old/old") would otherwise match inside the path first,
	// corrupting the directory while leaving the real argument stale and rendering
	// the alias undiscoverable by path (every lookup parses CLAUDE_CONFIG_DIR).
	q := bodyQuote(line)
	after, _ = rewriteRunArg(after, derivePlaybookName(abs), derivePlaybookName(newPath), q)
	// The path is spliced into the alias body too, and has never been escaped for
	// it -- the same injection, predating the run-argument rewrite.
	return prefix + escapeForBody(shellDoubleQuote(newPath), q) + after, true
}

// runArgQuoting lists the ways a playbook name can be quoted in an alias line,
// most specific first. The first entry is what Format produces: the body is
// single-quoted as a whole, so its inner single quotes are escaped as '\”.
var runArgQuoting = []struct{ open, close string }{
	{`'\''`, `'\''`},
	{`'`, `'`},
	{`"`, `"`},
	{``, ``},
}

// rewriteRunArg replaces the `run <oldName>` argument with newName, leaving the
// rest of the line verbatim -- including any flags appended after it.
//
// It rewrites only when the argument matches oldName exactly, so an unrelated
// token is never clobbered.
func rewriteRunArg(line, oldName, newName string, q byte) (string, bool) {
	if oldName == "" || newName == "" || oldName == newName {
		return line, false
	}
	replacement := " run " + encodeRunArg(newName, q)
	for _, form := range runArgQuoting {
		old := " run " + form.open + oldName + form.close
		i := strings.Index(line, old)
		if i < 0 {
			continue
		}
		if form.open == "" {
			// Unquoted: refuse a partial match, so `run demo` does not fire on
			// `run demo-staging`.
			if end := i + len(old); end < len(line) && isPlaybookNameByte(line[end]) {
				continue
			}
		}
		return line[:i] + replacement + line[i+len(old):], true
	}
	return line, false
}

// bodyQuote returns the quote character wrapping an alias body, or 0 when the
// body is unquoted. `alias v='...'` -> '\”, `alias v="..."` -> '"'.
func bodyQuote(line string) byte {
	loc := aliasRegex.FindStringIndex(line)
	if loc == nil {
		return 0
	}
	rest := line[loc[1]:]
	if rest == "" {
		return 0
	}
	if rest[0] == '\'' || rest[0] == '"' {
		return rest[0]
	}
	return 0
}

// escapeForBody re-encodes s so it can be spliced into an alias body wrapped in
// quote q without terminating that quote or otherwise changing its meaning.
//
// Splicing a raw value is a command injection, not a cosmetic bug: a playbook
// named `x'; touch PWNED; #` ends the single-quoted body and everything after it
// executes when the shell config is sourced. Names reach here from `rename` and
// from a `.playbook` manifest in an installed repo, and validateSinglePathSegment
// rejects only `/ \ CR LF` -- an apostrophe is a legal playbook name.
func escapeForBody(s string, q byte) string {
	switch q {
	case '\'':
		// A literal ' cannot appear inside a single-quoted string: leave the
		// string, emit an escaped quote, re-enter.
		return strings.ReplaceAll(s, "'", `'\''`)
	case '"':
		return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`).Replace(s)
	default:
		return s
	}
}

// encodeRunArg renders name as a single shell word for an alias body wrapped in
// quote q. The result is always safe to splice, whatever the name contains.
func encodeRunArg(name string, q byte) string {
	return escapeForBody(shellQuote(name), q)
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

func isPlaybookNameByte(b byte) bool {
	return b == '-' || b == '_' || b == '.' ||
		(b >= '0' && b <= '9') ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z')
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

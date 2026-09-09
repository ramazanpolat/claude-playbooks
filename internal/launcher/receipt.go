package launcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The receipt records the absolute path of every launcher this tool
// creates, one per line, followed since v3.10.1 by two tab-separated
// fields: the canonical playbooks root and the playbook name the launcher
// was written for. Its job is coverage, not authority: it lets uninstall
// find launchers in directories no heuristic would rediscover (custom
// --launcher-dir installs) and lets delete recognise the launchers it
// created for the playbook being deleted, but every entry is verified
// against the live filesystem before anything is removed, and launchers
// created before the receipt existed are still found by the resolution
// scan. An entry whose path the user renamed or deleted by hand simply no
// longer matches anything and is skipped. A path-only line (pre-v3.10.1,
// or written with no attribution) carries no ownership claim.

// ReceiptPath returns the receipt file location: $CLAUDE_LAUNCHER_RECEIPT
// (test seam), else $XDG_STATE_HOME/claude-playbook/launchers, else
// ~/.local/state/claude-playbook/launchers. Empty when no home is known —
// callers then skip receipt bookkeeping rather than guess a path.
func ReceiptPath() string {
	if v := os.Getenv("CLAUDE_LAUNCHER_RECEIPT"); v != "" {
		return v
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "claude-playbook", "launchers")
}

// normalizeLauncherPath is the form launcher paths are COMPARED in:
// absolute, with the launcher DIRECTORY resolved through symlinks and the
// launcher itself left alone (it is a symlink; resolving it would name the
// binary). Recording with `--launcher-dir ./bin` and matching later with
// the absolute directory then agree. Lines are stored as given, so
// Recorded() and uninstall see the paths they always saw. A path that
// cannot be resolved compares as given, made absolute where possible.
func normalizeLauncherPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	dir, base := filepath.Dir(path), filepath.Base(path)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return filepath.Join(dir, base)
}

// Recorded returns the deduplicated launcher paths in the receipt, in
// file order. A missing or unreadable receipt is an empty list.
func Recorded() []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range receiptLines() {
		p := entryPath(line)
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// Attribution reports which playbooks root and playbook the receipt says
// launcherPath was written for. ok is false for a path the receipt does
// not know, or knows without attribution (a pre-v3.10.1 line).
func Attribution(launcherPath string) (root, playbook string, ok bool) {
	for _, line := range receiptLines() {
		if !sameLauncher(entryPath(line), launcherPath) {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 || fields[1] == "" || fields[2] == "" {
			return "", "", false
		}
		return fields[1], fields[2], true
	}
	return "", "", false
}

// receiptLines returns the non-empty lines of the receipt, "" file aside,
// each passed through cleanLine.
func receiptLines() []string {
	path := ReceiptPath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = cleanLine(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// cleanLine trims a receipt line the way each format allows: a path-only
// line is trimmed of surrounding whitespace, as it always was; an
// attributed line keeps its fields verbatim (a playbook name may carry
// trailing whitespace and must read back unchanged), losing only the
// carriage return of a CRLF file.
func cleanLine(line string) string {
	line = strings.TrimRight(line, "\r")
	if strings.IndexByte(line, '\t') < 0 {
		return strings.TrimSpace(line)
	}
	return strings.TrimLeft(line, " ")
}

// sameLauncher reports whether two launcher paths name the same link.
func sameLauncher(a, b string) bool {
	return normalizeLauncherPath(a) == normalizeLauncherPath(b)
}

// entryPath is the launcher path of a receipt line: everything before the
// first tab, the whole line for a path-only entry.
func entryPath(line string) string {
	if i := strings.IndexByte(line, '\t'); i >= 0 {
		return line[:i]
	}
	return line
}

// entryLine renders a receipt line. Attribution is recorded only when
// both parts are known and neither carries a field or line separator; a
// launcher written without them stays a path-only line, which claims
// nothing. (Command names are validated against those characters; a
// registry root containing one is merely left unattributed.)
func entryLine(path, root, playbook string) string {
	if root == "" || playbook == "" || strings.ContainsAny(root+playbook, "\t\n\r") {
		return path
	}
	return path + "\t" + root + "\t" + playbook
}

// RemoveReceipt deletes the receipt file (and its lock, and the state
// directory if that leaves it empty). Called by uninstall once the
// launchers themselves are gone.
func RemoveReceipt() {
	path := ReceiptPath()
	if path == "" {
		return
	}
	os.Remove(path)
	os.Remove(path + ".lock")
	os.Remove(filepath.Dir(path)) // fails unless empty — exactly the intent
}

// record adds path to the receipt with its attribution, replacing an
// earlier line for the same path so a re-registered launcher carries the
// current owner; unrecord removes every line for the path. Both are
// best-effort bookkeeping: they return an error for the caller to warn
// about, but the launcher operation itself has already succeeded.
func record(path, root, playbook string) error {
	if strings.ContainsAny(path, "\t\n\r") {
		// The line format cannot carry it, and a truncated path would
		// later match nothing or the wrong thing.
		return fmt.Errorf("launcher path %q contains a tab or line break; not recorded", path)
	}
	line := entryLine(path, root, playbook)
	return editReceipt(func(lines []string) []string {
		var kept []string
		replaced := false
		for _, l := range lines {
			if !sameLauncher(entryPath(l), path) {
				kept = append(kept, l)
				continue
			}
			if !replaced {
				kept = append(kept, line)
				replaced = true
			}
		}
		if !replaced {
			kept = append(kept, line)
		}
		return kept
	})
}

func unrecord(path string) error {
	return editReceipt(func(lines []string) []string {
		var kept []string
		for _, l := range lines {
			if !sameLauncher(entryPath(l), path) {
				kept = append(kept, l)
			}
		}
		return kept
	})
}

func editReceipt(edit func([]string) []string) error {
	path := ReceiptPath()
	if path == "" {
		return fmt.Errorf("no home directory; launcher receipt not updated")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			if l = cleanLine(l); l != "" {
				lines = append(lines, l)
			}
		}
	}
	edited := edit(lines)
	if len(edited) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".launchers-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(strings.Join(edited, "\n") + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

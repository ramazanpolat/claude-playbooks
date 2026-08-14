package launcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The receipt records the absolute path of every launcher this tool
// creates, one per line. Its job is coverage, not authority: it lets
// uninstall find launchers in directories no heuristic would rediscover
// (custom --launcher-dir installs), but every entry is verified against
// the live filesystem before anything is removed, and launchers created
// before the receipt existed are still found by the resolution scan. An
// entry whose path the user renamed or deleted by hand simply no longer
// matches anything and is skipped.

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

// Recorded returns the deduplicated launcher paths in the receipt, in
// file order. A missing or unreadable receipt is an empty list.
func Recorded() []string {
	path := ReceiptPath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	return out
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

// record adds path to the receipt; unrecord removes it. Both are
// best-effort bookkeeping: they return an error for the caller to warn
// about, but the launcher operation itself has already succeeded.
func record(path string) error {
	return editReceipt(func(lines []string) []string {
		for _, l := range lines {
			if l == path {
				return lines
			}
		}
		return append(lines, path)
	})
}

func unrecord(path string) error {
	return editReceipt(func(lines []string) []string {
		var kept []string
		for _, l := range lines {
			if l != path {
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
			if l = strings.TrimSpace(l); l != "" {
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

// Package testenv gives tests a PATH that does not depend on the host.
package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
)

// tools are the commands tests and the code under test run through PATH.
// Each one found on the host is linked into the controlled directory; the
// rest of the host's commands are out of reach. Neither `claude` nor a
// sandbox backend is here: tests stub them.
var tools = []string{
	"awk", "basename", "bash", "cat", "chmod", "cmp", "cp", "cut", "date",
	"diff", "dirname", "env", "false", "find", "git", "grep", "gzip", "head",
	"id", "ln", "ls", "mkdir", "mktemp", "mount", "mv", "printf", "ps",
	"python3", "readlink", "rm", "rmdir", "script", "security", "sed",
	"sha256sum", "shasum", "sh", "sleep", "sort", "stat", "tail", "tar",
	"tee", "test", "touch", "tr", "true", "umount", "uname", "wc", "xargs",
}

// HermeticPath links the host's copy of each tool into a new temporary
// directory and returns that directory, to be the whole PATH. A test that
// creates a launcher is then judged against these names only, never
// against whatever else the host happens to have installed (a CI runner's
// /usr/bin/pl must not refuse a test's launcher named pl).
func HermeticPath() (string, error) {
	dir, err := os.MkdirTemp("", "cpb-test-path")
	if err != nil {
		return "", err
	}
	for _, t := range tools {
		p, err := exec.LookPath(t)
		if err != nil {
			continue
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if err := os.Symlink(p, filepath.Join(dir, t)); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

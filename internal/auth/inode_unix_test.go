//go:build unix

package auth

import (
	"os"
	"syscall"
)

// inode is the file's inode number; ok is false when the OS does not say.
func inode(info os.FileInfo) (uint64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Ino), true
}

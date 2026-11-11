//go:build !unix

package auth

import "os"

// inode is not told off unix; os.SameFile still compares the file's identity.
func inode(os.FileInfo) (uint64, bool) { return 0, false }

//go:build darwin

package cmd

import "golang.org/x/sys/unix"

// fread is FREAD from <sys/fcntl.h>: TIOCFLUSH with it discards input only.
const fread = 0x1

// flushInput discards what is waiting to be read on the terminal fd: what
// the person typed or pasted past the line a hidden read took. Nothing of
// it reaches the next reader of the terminal, such as the shell.
func flushInput(fd int) error { return unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, fread) }

//go:build linux

package cmd

import "golang.org/x/sys/unix"

// flushSupported: the store step can discard terminal input here.
const flushSupported = true

// flushInput discards what is waiting to be read on the terminal fd: what
// the person typed or pasted past the line a hidden read took. Nothing of
// it reaches the next reader of the terminal, such as the shell.
func flushInput(fd int) error { return unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH) }

// pendingInput is how many bytes wait to be read on the terminal fd.
func pendingInput(fd int) (int, error) { return unix.IoctlGetInt(fd, unix.TIOCINQ) }

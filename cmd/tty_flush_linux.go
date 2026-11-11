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

// echoOff turns the terminal fd's echo and its line editing off, and
// leaves the rest as it was (^C still signals): what is typed is not shown,
// and every key is waiting input at once, Enter or not, so it can be
// counted and discarded. The returned func puts it back. Where the mode
// cannot be read or set, it changes nothing.
func echoOff(fd int) (restore func()) {
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return func() {}
	}
	old := *t
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON
	t.Cc[unix.VMIN], t.Cc[unix.VTIME] = 1, 0
	if unix.IoctlSetTermios(fd, unix.TCSETS, t) != nil {
		return func() {}
	}
	return func() { unix.IoctlSetTermios(fd, unix.TCSETS, &old) }
}

//go:build darwin

package cmd

import "golang.org/x/sys/unix"

// flushSupported: the store step can discard terminal input here.
const flushSupported = true

// fread is FREAD from <sys/fcntl.h>: TIOCFLUSH with it discards input only.
const fread = 0x1

// fionread is FIONREAD from <sys/filio.h>, _IOR('f', 127, int), which
// golang.org/x/sys does not name on darwin.
const fionread = 0x4004667f

// flushInput discards what is waiting to be read on the terminal fd: what
// the person typed or pasted past the line a hidden read took. Nothing of
// it reaches the next reader of the terminal, such as the shell.
func flushInput(fd int) error { return unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, fread) }

// pendingInput is how many bytes wait to be read on the terminal fd.
func pendingInput(fd int) (int, error) { return unix.IoctlGetInt(fd, fionread) }

// echoOff turns the terminal fd's echo off and leaves everything else as it
// was (^C still signals, a line still ends at Enter); the returned func puts
// it back. Where the mode cannot be read or set, it changes nothing.
func echoOff(fd int) (restore func()) {
	t, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return func() {}
	}
	old := *t
	t.Lflag &^= unix.ECHO | unix.ECHONL
	if unix.IoctlSetTermios(fd, unix.TIOCSETA, t) != nil {
		return func() {}
	}
	return func() { unix.IoctlSetTermios(fd, unix.TIOCSETA, &old) }
}

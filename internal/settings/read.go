package settings

import (
	"errors"
	"io"
	"os"
	"syscall"
)

// ErrNotRegular is a settings path holding something other than a regular
// file: a folder, a FIFO, a device.
var ErrNotRegular = errors.New("not a regular file")

// ReadFile reads a settings file without ever waiting: the path is opened
// non-blocking, so a FIFO with no writer does not hold the caller, and
// anything but a regular file (a FIFO, a device, a folder) is refused with
// ErrNotRegular before a byte is read. A missing file is os.ErrNotExist, as
// os.ReadFile has it.
func ReadFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, &os.PathError{Op: "read", Path: path, Err: ErrNotRegular}
	}
	return io.ReadAll(f)
}

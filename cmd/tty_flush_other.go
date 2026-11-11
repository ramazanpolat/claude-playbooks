//go:build !linux && !darwin

package cmd

import "errors"

// flushInput is not available here; the store step has no default store
// on these systems either.
func flushInput(int) error {
	return errors.New("flushing terminal input is not supported on this system")
}

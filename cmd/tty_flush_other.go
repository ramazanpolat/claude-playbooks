//go:build !linux && !darwin

package cmd

import "errors"

// flushSupported: on these systems cpb cannot discard terminal input, so
// the store step does not run here (storeStepRefusal).
const flushSupported = false

func flushInput(int) error {
	return errors.New("discarding terminal input is not supported on this system")
}

func pendingInput(int) (int, error) { return 0, errors.New("not supported on this system") }

package cmd

import (
	"errors"
	"fmt"
)

type commandExitError struct {
	code int
}

func (e *commandExitError) Error() string {
	return fmt.Sprintf("command exited with code %d", e.code)
}

func exitCode(err error) (int, bool) {
	var exitErr *commandExitError
	if errors.As(err, &exitErr) {
		return exitErr.code, true
	}
	return 0, false
}

func preserveExitCode(err error) error {
	if err == nil {
		return nil
	}
	type exitCoder interface {
		ExitCode() int
	}
	var exitErr exitCoder
	if errors.As(err, &exitErr) {
		return &commandExitError{code: exitErr.ExitCode()}
	}
	return err
}

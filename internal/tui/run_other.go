//go:build !unix

package tui

import "errors"

// Run is `cpb tui`, which runs on Linux and macOS (cpb's release targets).
func Run(o Options) error { return errors.New("cpb tui runs on Linux and macOS") }

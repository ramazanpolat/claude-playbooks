package cmd

import (
	"fmt"
	"os"

	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// A playbook whose manifest cannot be read is contained (SPEC.md,
// "Unreadable manifests"): a list shows every playbook it can read, says on
// stderr which it left out and why, and exits 1, as ls does.

// leftOut writes one stderr line for each playbook a list could not read and
// returns the error that makes the list exit 1; nil when it left none out.
// The list has printed what it could read before this is called.
func leftOut(bad []playbook.Unreadable) error {
	for _, u := range bad {
		fmt.Fprintf(os.Stderr, "playbook %q is left out: %v\n", u.Name, u.Err)
	}
	if len(bad) > 0 {
		return &commandExitError{code: 1}
	}
	return nil
}

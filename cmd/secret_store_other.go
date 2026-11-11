//go:build !darwin && !linux

package cmd

import "errors"

// No default store elsewhere: a machine with none refuses and names the fix.
func osDefaultSecretStore() (osSecretStore, error) {
	return nil, errors.New("cpb:<name> references need the operating system's secret store, and cpb has none for this system; configure a helper of your own (ALTER DEFAULTS SET secret_helper = '<command>')")
}

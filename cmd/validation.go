package cmd

import (
	"fmt"
	"path/filepath"
	"strings"
)

func validateTopLevelName(field, name string) error {
	if name == "" {
		return fmt.Errorf("%s cannot be empty", field)
	}
	if filepath.IsAbs(name) || strings.ContainsAny(name, `/\`) || filepath.Clean(name) != name {
		return fmt.Errorf("%s must be a top-level playbook name, not a path", field)
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("%s cannot start with '.'", field)
	}
	return nil
}

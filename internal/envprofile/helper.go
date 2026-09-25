package envprofile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// The secret helper resolves secret references (docs/cli-grammar.md,
// "Secrets"): cpb execs `<helper> --check KEY=REF` when a reference is
// written and `<helper> K=REF … -- claude <args>` at launch. cpb defines
// the interface and never names or discovers a helper; the pilot configures
// one. Without one, references are refused and everything else works.
const (
	// SecretHelperFile under the profiles directory holds the stored
	// setting: one line, the command.
	SecretHelperFile = ".secret-helper"
	// SecretHelperEnv overrides the stored setting for one process.
	SecretHelperEnv = "CPB_SECRET_HELPER"
)

// Helper is the secret helper in effect and where it came from.
type Helper struct {
	Command string
	From    string // "setting" or SecretHelperEnv
}

// ValidateHelperCommand reports whether cmd can be a helper: one command, a
// name on PATH or an absolute path, with no arguments. It is exec'd with an
// argument vector and never through a shell, so whitespace would be read
// as part of the name.
func ValidateHelperCommand(cmd string) error {
	if cmd == "" || strings.ContainsAny(cmd, " \t\r\n\x00") {
		return fmt.Errorf("the secret helper is one command (a name on PATH or an absolute path), without arguments")
	}
	return nil
}

// SecretHelper returns the helper in effect, nil when none is configured.
// CPB_SECRET_HELPER wins over the stored setting. A stored setting that is
// empty or malformed is an error, not "none": references would otherwise
// fail later with a less useful message.
func SecretHelper(dir string) (*Helper, error) {
	if cmd, ok := os.LookupEnv(SecretHelperEnv); ok && cmd != "" {
		if err := ValidateHelperCommand(cmd); err != nil {
			return nil, fmt.Errorf("%s: %w", SecretHelperEnv, err)
		}
		return &Helper{Command: cmd, From: SecretHelperEnv}, nil
	}
	path := filepath.Join(dir, SecretHelperFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	cmd := strings.TrimSpace(string(data))
	if err := ValidateHelperCommand(cmd); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Helper{Command: cmd, From: "setting"}, nil
}

// SetSecretHelper stores cmd as the helper.
func SetSecretHelper(dir, cmd string) error {
	if err := ValidateHelperCommand(cmd); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return manifest.WritePrivate(filepath.Join(dir, SecretHelperFile), []byte(cmd+"\n"), 0o600)
}

// ClearSecretHelper removes the stored helper; clearing none is fine.
func ClearSecretHelper(dir string) error {
	err := os.Remove(filepath.Join(dir, SecretHelperFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

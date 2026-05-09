package auth

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// SyncCredentials ensures the target config directory shares the global Claude Code authentication.
func SyncCredentials(targetDir string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	globalClaude := filepath.Join(home, ".claude")
	globalCreds := filepath.Join(globalClaude, ".credentials.json")

	// On macOS, if the global credentials file doesn't exist, extract from Keychain.
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat(globalCreds); os.IsNotExist(err) {
			out, err := exec.Command("security", "find-generic-password", "-s", "Claude Code-credentials", "-w").Output()
			if err == nil && len(out) > 0 {
				os.MkdirAll(globalClaude, 0755)
				os.WriteFile(globalCreds, out, 0600)
			}
		}
	}

	// If global credentials exist, symlink them to the target dir.
	if _, err := os.Stat(globalCreds); err == nil {
		targetCreds := filepath.Join(targetDir, ".credentials.json")
		// Remove existing file or symlink if it exists
		os.Remove(targetCreds)
		return os.Symlink(globalCreds, targetCreds)
	}

	return nil
}

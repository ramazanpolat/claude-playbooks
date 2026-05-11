package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const CredentialsFileName = ".credentials.json"
const StateFileName = ".claude.json"

var accountStateKeys = []string{
	"oauthAccount",
	"userID",
	"hasCompletedOnboarding",
	"lastOnboardingVersion",
	"installMethod",
	"claudeCodeFirstTokenDate",
	"autoUpdates",
	"autoUpdatesProtectedForNative",
	"lastReleaseNotesSeen",
}

var findGenericPassword = func(service string) ([]byte, error) {
	return exec.Command("security", "find-generic-password", "-s", service, "-w").Output()
}

// SyncCredentials ensures the target config directory shares the global Claude Code authentication.
func SyncCredentials(targetDir string) error {
	globalCreds, err := EnsureGlobalCredentials()
	if err != nil {
		return err
	}
	if globalCreds == "" {
		return SyncAccountMetadata(targetDir)
	}
	if err := LinkCredentials(targetDir, globalCreds); err != nil {
		return err
	}
	return SyncAccountMetadata(targetDir)
}

// EnsureGlobalCredentials returns the usable global Claude credentials file.
// On macOS, it also tries to materialize the file from the default Keychain item
// when the file is missing.
func EnsureGlobalCredentials() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	globalClaude := filepath.Join(home, ".claude")
	globalCreds := filepath.Join(globalClaude, CredentialsFileName)

	if runtime.GOOS == "darwin" {
		out, err := findGenericPassword("Claude Code-credentials")
		out = bytes.TrimSpace(out)
		if err == nil && len(out) > 0 {
			if !json.Valid(out) {
				return "", fmt.Errorf("Keychain item Claude Code-credentials did not contain valid JSON credentials")
			}
			if err := os.MkdirAll(globalClaude, 0755); err != nil {
				return "", err
			}
			if err := os.WriteFile(globalCreds, out, 0600); err != nil {
				return "", err
			}
			return globalCreds, nil
		}
	}

	usable, err := credentialsFileUsable(globalCreds)
	if err != nil {
		return "", err
	}
	if usable {
		return globalCreds, nil
	}

	return "", nil
}

// LinkCredentials points targetDir/.credentials.json at sourceCreds. Existing
// valid regular credentials are left intact so a playbook can keep its own login.
func LinkCredentials(targetDir, sourceCreds string) error {
	targetInfo, err := os.Stat(targetDir)
	if err != nil {
		return err
	}
	if !targetInfo.IsDir() {
		return fmt.Errorf("%s is not a directory", targetDir)
	}

	sourceAbs, err := filepath.Abs(sourceCreds)
	if err != nil {
		return err
	}
	targetDirAbs, err := filepath.Abs(targetDir)
	if err != nil {
		return err
	}
	if targetDirAbs == filepath.Dir(sourceAbs) {
		return nil
	}

	targetCreds := filepath.Join(targetDir, CredentialsFileName)
	if info, err := os.Lstat(targetCreds); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(targetCreds)
			if err == nil {
				linkAbs := link
				if !filepath.IsAbs(linkAbs) {
					linkAbs = filepath.Join(targetDir, linkAbs)
				}
				linkAbs, _ = filepath.Abs(linkAbs)
				if linkAbs == sourceAbs {
					return nil
				}
			}
			if err := os.Remove(targetCreds); err != nil {
				return err
			}
		} else {
			usable, err := credentialsFileUsable(targetCreds)
			if err != nil {
				return fmt.Errorf("existing credentials at %s are not usable: %w", targetCreds, err)
			}
			if usable {
				return nil
			}
			if err := os.Remove(targetCreds); err != nil {
				return err
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	return os.Symlink(sourceAbs, targetCreds)
}

// SyncAccountMetadata copies Claude Code's non-token account metadata into the
// target config. Interactive Claude startup uses this state to decide whether a
// config directory is already logged in.
func SyncAccountMetadata(targetDir string) error {
	sourceState, err := findAccountState(targetDir)
	if err != nil {
		return err
	}
	if sourceState == nil {
		return nil
	}

	targetPath := filepath.Join(targetDir, StateFileName)
	targetState := map[string]any{}
	if data, err := os.ReadFile(targetPath); err == nil {
		if len(bytes.TrimSpace(data)) > 0 {
			if err := json.Unmarshal(data, &targetState); err != nil {
				return fmt.Errorf("invalid %s at %s: %w", StateFileName, targetPath, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	changed := false
	for _, key := range accountStateKeys {
		value, ok := sourceState[key]
		if !ok {
			continue
		}
		if key == "oauthAccount" {
			if changedAccount := mergeMap(targetState, key, value); changedAccount {
				changed = true
			}
			continue
		}
		if _, exists := targetState[key]; !exists {
			targetState[key] = value
			changed = true
		}
	}
	if !changed {
		return nil
	}

	data, err := json.MarshalIndent(targetState, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(targetPath, data, 0600)
}

func credentialsFileUsable(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return false, nil
	}
	if !json.Valid(data) {
		return false, fmt.Errorf("invalid JSON")
	}
	return true, nil
}

func findAccountState(targetDir string) (map[string]any, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	targetState, _ := filepath.Abs(filepath.Join(targetDir, StateFileName))
	candidates := []string{
		filepath.Join(home, ".claude", StateFileName),
	}

	playbooksDir := os.Getenv("CLAUDE_PLAYBOOKS_DIR")
	if playbooksDir == "" {
		playbooksDir = filepath.Join(home, ".claude-playbooks")
	}
	if paths, err := discoverStateFiles(playbooksDir, targetState); err == nil {
		candidates = append(candidates, paths...)
	}

	var fallback map[string]any
	for _, path := range candidates {
		abs, _ := filepath.Abs(path)
		if abs == targetState {
			continue
		}
		state, err := readAccountState(path)
		if err != nil {
			return nil, err
		}
		if state != nil {
			if state["hasCompletedOnboarding"] == true {
				return state, nil
			}
			if fallback == nil {
				fallback = state
			}
		}
	}

	return fallback, nil
}

func discoverStateFiles(root, skipPath string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "backups", "cache", "file-history", "node_modules", "projects", "session-env", "sessions", "telemetry":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != StateFileName {
			return nil
		}
		abs, _ := filepath.Abs(path)
		if abs != skipPath {
			out = append(out, path)
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	return out, err
}

func readAccountState(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("invalid %s at %s: %w", StateFileName, path, err)
	}
	if _, ok := state["oauthAccount"]; !ok {
		return nil, nil
	}
	return state, nil
}

func mergeMap(target map[string]any, key string, value any) bool {
	sourceMap, ok := value.(map[string]any)
	if !ok {
		if _, exists := target[key]; exists {
			return false
		}
		target[key] = value
		return true
	}

	existing, ok := target[key].(map[string]any)
	if !ok {
		target[key] = sourceMap
		return true
	}

	changed := false
	for k, v := range sourceMap {
		if _, exists := existing[k]; !exists {
			existing[k] = v
			changed = true
		}
	}
	return changed
}

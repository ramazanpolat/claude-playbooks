package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// identityStateKeys are the parts of .claude.json that describe an Anthropic
// ACCOUNT rather than the installation: who is logged in, and the feature
// flags and eligibility Claude Code fetched for that account. Claude Code
// turns its claude.ai-hosted tools (Artifact and friends) on from those cached
// flags, whatever ANTHROPIC_BASE_URL points at.
var identityStateKeys = []string{
	"oauthAccount",
	"cachedGrowthBookFeatures",
	"cachedGrowthBookFeaturesAt",
	"cachedExperimentFeatures",
	"cachedExperimentData",
	"passesEligibilityCache",
	"cachedExtraUsageDisabledReason",
}

// StaleIdentityState lists the identity keys present in configDir's
// .claude.json, in identityStateKeys order; nil when the file is absent,
// empty, or unreadable.
func StaleIdentityState(configDir string) []string {
	data, err := os.ReadFile(filepath.Join(configDir, StateFileName))
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	var state map[string]json.RawMessage
	if json.Unmarshal(data, &state) != nil {
		return nil
	}
	var present []string
	for _, key := range identityStateKeys {
		if _, ok := state[key]; ok {
			present = append(present, key)
		}
	}
	return present
}

// QuarantineAccountState removes identityStateKeys from configDir's
// .claude.json and returns the keys it removed.
//
// The launch path applies it to an isolated playbook that holds no login of
// its own (no grant in its store) and whose block sets no token. In that
// state any account record or flag cache is a leftover from a non-isolated
// past: seeded by the token path's metadata sync, or fetched by Claude Code
// while the global token was injected. Left alone it keeps steering the
// session as the global account, claude.ai-hosted tools included, although
// nothing authenticates as that account any more. A playbook that logs in on
// its own regenerates every one of these keys, so removing them costs a
// logged-in playbook nothing either; the login test merely avoids touching
// state the playbook is actively using. The store FILE is the login the tool
// reasons about, as everywhere else in this package; a macOS Keychain-only
// login is not consulted.
//
// An absent or empty file is nothing to do. Invalid JSON is an error, left to
// the caller to treat as advisory: the launch still works, the leftovers stay.
func QuarantineAccountState(configDir string) ([]string, error) {
	path := filepath.Join(configDir, StateFileName)
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
	var state map[string]json.RawMessage
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("invalid %s at %s: %w", StateFileName, path, err)
	}
	var removed []string
	for _, key := range identityStateKeys {
		if _, ok := state[key]; ok {
			delete(state, key)
			removed = append(removed, key)
		}
	}
	if len(removed) == 0 {
		return nil, nil
	}
	out, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, err
	}
	out = append(out, '\n')
	if err := writeFilePrivate(path, out); err != nil {
		return nil, err
	}
	return removed, nil
}

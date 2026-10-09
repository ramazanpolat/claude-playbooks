package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"runtime"
)

// KeychainState is what a presence-only Keychain probe found.
type KeychainState string

const (
	KeychainPresent KeychainState = "present"
	KeychainAbsent  KeychainState = "absent"
	// KeychainUnknown: the probe could not answer (no `security`, a locked
	// or unreadable Keychain, an exit other than "not found"), or the item's
	// name could not be derived with certainty. Never reported as absent.
	KeychainUnknown KeychainState = "unknown"
)

// KeychainProbe reports whether a generic-password item exists for account
// and service, never reading its secret: `security find-generic-password`
// without -w reads the item's attributes only and raises no password prompt.
// Exit 0 is present, 44 (errSecItemNotFound) is absent, anything else is
// unknown. Off darwin there is no Keychain: absent. Tests replace it; no test
// may reach a real Keychain.
var KeychainProbe = func(account, service string) KeychainState {
	if runtime.GOOS != "darwin" {
		return KeychainAbsent
	}
	err := exec.Command("security", "find-generic-password", "-a", account, "-s", service).Run()
	if err == nil {
		return KeychainPresent
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 44 {
		return KeychainAbsent
	}
	return KeychainUnknown
}

// machineKeychainService is the item Claude Code keeps the machine's login
// in (CLAUDE_CONFIG_DIR unset), which a shared-login launch materialises
// into ~/.claude/.credentials.json (EnsureGlobalCredentials).
const machineKeychainService = "Claude Code-credentials"

var keychainAccountRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// keychainAccount is the account Claude Code files its items under: $USER
// (else the OS user name) when it is a plain name, else "claude-code-user".
func keychainAccount() string {
	name := os.Getenv("USER")
	if name == "" {
		if u, err := user.Current(); err == nil {
			name = u.Username
		}
	}
	if !keychainAccountRe.MatchString(name) {
		return "claude-code-user"
	}
	return name
}

// configKeychainService is the item a config directory's own login lives
// in when Claude Code runs with CLAUDE_CONFIG_DIR set: the machine name plus
// "-" and the first 8 hex digits of the sha256 of the CLAUDE_CONFIG_DIR
// string exactly as passed (Claude Code 2.1.292; verified on a logged-in
// isolated playbook, 2026-10-07: a trailing "/" names another item). Claude
// Code NFC-normalises that string first; a non-ASCII path, where that could
// matter, gives ok=false and the caller answers unknown rather than guess.
func configKeychainService(configDir string) (service string, ok bool) {
	for i := 0; i < len(configDir); i++ {
		if configDir[i] >= 0x80 {
			return "", false
		}
	}
	sum := sha256.Sum256([]byte(configDir))
	return machineKeychainService + "-" + hex.EncodeToString(sum[:])[:8], true
}

// keychainLogin probes the items a stored-login launch of configDir can
// authenticate from when the file store holds no grant: the playbook's own
// item, and for a shared login also the machine's item the launch
// materialises. The machine's own config dir (machine=true, CLAUDE_CONFIG_DIR
// unset) has only the machine's item. Present wins, then unknown; absent
// only when every probe answered absent.
func keychainLogin(configDir string, shared, machine bool) KeychainState {
	account := keychainAccount()
	states := []KeychainState{}
	if !machine {
		if service, ok := configKeychainService(configDir); ok {
			states = append(states, KeychainProbe(account, service))
		} else {
			states = append(states, KeychainUnknown)
		}
	}
	if shared || machine {
		states = append(states, KeychainProbe(account, machineKeychainService))
	}
	result := KeychainAbsent
	for _, s := range states {
		switch s {
		case KeychainPresent:
			return KeychainPresent
		case KeychainUnknown:
			result = KeychainUnknown
		}
	}
	return result
}

// Login is where a stored-login launch would find its login: "store" (the
// file store, or its link, holds a grant the launch keeps), "keychain" (the
// Keychain does), "none", or "unknown" when the Keychain or the store could
// not be read. An isolated playbook's link to the shared store is detached
// at launch, so a grant reached through it does not count. A token launch
// has no stored login to report: "".
func (r Report) Login() string {
	if !r.usesStoredLogin() {
		return ""
	}
	switch {
	case r.ownGrant():
		return "store"
	case r.Keychain == KeychainPresent:
		return "keychain"
	case r.Keychain == KeychainUnknown, r.storeUnknownCounts():
		return "unknown"
	}
	return "none"
}

package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Notice reports what a sync did that the pilot should know, one line on
// stderr. Tests replace it.
var Notice = func(msg string) { fmt.Fprintln(os.Stderr, "Warning: "+msg) }

// now is the clock for set-aside names; tests replace it.
var now = time.Now

// accountUUID reads oauthAccount.accountUuid from a .claude.json: the
// account Claude Code last recorded for that config directory. "" when the
// file, the record or the field is absent or unreadable. No credential is
// read: .claude.json holds none.
func accountUUID(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var state struct {
		OAuthAccount struct {
			AccountUUID string `json:"accountUuid"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(data, &state) != nil {
		return ""
	}
	return state.OAuthAccount.AccountUUID
}

// machineAccountUUID is the account of the machine's own login, from its
// own state (see findAccountState). When both machine state files name an
// account and the accounts differ, one of them is stale and nothing tells
// which: the machine's account is then unknown, so no login is promoted
// over the machine store on its strength.
func machineAccountUUID(targetDir string) string {
	state, err := findAccountState(targetDir)
	if err != nil || state == nil {
		return ""
	}
	account, _ := state["oauthAccount"].(map[string]any)
	uuid, _ := account["accountUuid"].(string)
	canonical, nested, err := machineStateFiles()
	if err != nil {
		return ""
	}
	targetState, _ := filepath.Abs(filepath.Join(targetDir, StateFileName))
	for _, path := range []string{canonical, nested} {
		if abs, _ := filepath.Abs(path); abs == targetState {
			continue
		}
		if other := accountUUID(path); other != "" && other != uuid {
			return ""
		}
	}
	return uuid
}

// sameAccount reports whether a playbook's own login is the machine's
// account: both sides record an accountUuid, and they are equal. Unknown on
// either side is not the same account.
func sameAccount(targetDir string) bool {
	own := accountUUID(filepath.Join(targetDir, StateFileName))
	machine := machineAccountUUID(targetDir)
	return own != "" && own == machine
}

// setAsideOwnLogin keeps a playbook's own login of another (or an
// unconfirmed) account out of the machine's store: the store is renamed to
// .credentials.json.cpb-own-<stamp>, and the account state that came with it
// leaves .claude.json (backed up first), so the metadata sync fills in the
// machine's. Nothing is deleted, and nothing of either account is printed.
// then says what the launch does instead, for the notice. With copy, the
// store stays in place and a copy is kept, for a caller that strips only the
// grant from it and keeps its other logins (mcpOAuth) where they are.
func setAsideOwnLogin(targetDir, then string, copy bool) error {
	stamp := now().Format("2006-01-02-15_04_05")
	creds := StorePath(targetDir)
	kept := creds + ".cpb-own-" + stamp
	keep := os.Rename
	if copy {
		keep = func(from, to string) error { return copyFile(from, to, 0600) }
	}
	if err := keep(creds, kept); err != nil {
		return fmt.Errorf("cannot set aside the login in %s: %w", targetDir, err)
	}
	state := filepath.Join(targetDir, StateFileName)
	if _, err := removeStateKeys(state, state+".cpb-backup-"+stamp); err != nil {
		return fmt.Errorf("set aside the login in %s, but cannot clear its account state: %w", targetDir, err)
	}
	own, machine := accountUUID(state+".cpb-backup-"+stamp), machineAccountUUID(targetDir)
	why := "another account's login"
	switch {
	case own == "" || machine == "":
		why = "a login that cannot be confirmed as the machine's account"
	case own == machine:
		// The machine's account, set aside only because the machine store
		// holds no grant of it to fall back on.
		why = "the machine account's login, which the machine store does not hold"
	}
	Notice(fmt.Sprintf("%s held %s; kept it as %s and %s. To keep that account there: ALTER PLAYBOOK <name> SET login = 'isolated', then move the file back",
		targetDir, why, filepath.Base(kept), then))
	return nil
}

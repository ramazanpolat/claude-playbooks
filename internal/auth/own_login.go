package auth

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// hostOS is runtime.GOOS; tests replace it.
var hostOS = runtime.GOOS

// ErrOwnLogin is what a launch refused by ownLoginRefusal satisfies
// (errors.Is): every launching caller treats it as fatal, like an env set
// that cannot be resolved.
var ErrOwnLogin = errors.New("token launch over the playbook's own login")

type ownLoginError struct{ dir, where string }

func (e *ownLoginError) Is(target error) bool { return target == ErrOwnLogin }

func (e *ownLoginError) Error() string {
	return fmt.Sprintf("%s has a login of its own (%s), and this launch would authenticate with a token. "+
		"Claude Code can fall back from a token to a stored login, so the launch would have to delete that login, and cpb never deletes a login. "+
		"To use the login, launch without the token: --block %s for this launch, or remove it from the playbook "+
		"(ALTER PLAYBOOK <name> UNSET VAR %s, or DROP ENV <set> for the env set that sets it). "+
		"To use the token, log the playbook out first: launch it with --block %s and run /logout",
		e.dir, e.where, OAuthTokenEnv, OAuthTokenEnv, OAuthTokenEnv)
}

// ownToken is the token an isolated playbook supplies itself: the one its
// own block, the env sets it uses, or a launch flag sets. DEFAULTS never
// counts: it is the machine's layer, and an isolated login has no machine
// token. The value is "" when none is set, or it is set empty or blocked;
// ref reports a token supplied by reference instead, which the secret
// helper sets at exec.
func ownToken(configDir string, layers []*manifest.Env) (tok string, ref bool, err error) {
	dir := envset.Dir(config.ResolvePlaybooksDir())
	var block *manifest.Env
	if m, _ := manifest.Nearest(configDir); m != nil {
		block = m.Env
	}
	own, err := envset.Expand(dir, block)
	if err != nil {
		return "", false, err
	}
	flat := []*manifest.Env{own}
	for _, layer := range layers {
		expanded, err := envset.Expand(dir, layer)
		if err != nil {
			return "", false, err
		}
		flat = append(flat, expanded)
	}
	menv := manifest.MergeEnv(flat...)
	if menv.Blocks(OAuthTokenEnv) {
		return "", false, nil
	}
	return manifestToken(menv), tokenRef(menv), nil
}

// ownLoginRefusal refuses a token launch of an isolated playbook that holds,
// or may hold, a login of its own: a grant in its store, a store that cannot
// be read, or a Keychain item that is present or cannot be probed. Until a
// login can be set aside and restored around a token run, the only way to
// launch on the token would be to delete the login. A store that is a link
// holds the shared login, which isolation detaches; it is not the
// playbook's own. Nothing is changed here.
func ownLoginRefusal(configDir string) error {
	store := StorePath(configDir)
	where := ""
	info, err := os.Lstat(store)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
	case err == nil || !os.IsNotExist(err):
		absent, aErr := storeGrantAbsent(store)
		switch {
		case aErr != nil:
			where = "a credential store cpb cannot read, which may hold one"
		case !absent:
			where = "in its " + CredentialsFileName
		}
	}
	if where == "" {
		switch keychainLogin(configDir, false, false) {
		case KeychainPresent:
			where = "in the Keychain"
		case KeychainUnknown:
			where = "the Keychain could not be checked, and may hold one"
		}
	}
	if where == "" {
		return nil
	}
	return &ownLoginError{dir: configDir, where: where}
}

// keepStoreGrant runs before the token path's quarantine strips a shared
// playbook's stored grant. A store that is a regular file holding a grant is
// that grant's only copy unless it is the machine's own account: then a
// newer copy goes over the machine store first, as the credential sync does,
// and the strip loses nothing. Any other login, or one that cannot be
// confirmed as the machine's, or one with no machine grant to fall back on,
// is copied aside first (the quarantine then strips only the grant, and the
// store's other logins stay). A link, an unreadable store and the machine's
// own store are left to the quarantine, which keeps them.
func keepStoreGrant(configDir string) error {
	if isGlobalConfigDir(configDir) {
		return nil
	}
	store := StorePath(configDir)
	info, err := os.Lstat(store)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || isGlobalCredentialsFile(store) {
		return nil
	}
	if usable, err := credentialsFileUsable(store); err != nil || !usable || !storeHasOAuthGrant(store) {
		return nil
	}
	global, err := globalClaudeDir()
	if err != nil {
		return err
	}
	globalStore := StorePath(global)
	globalInfo, err := os.Stat(globalStore)
	// The machine store is the recovery copy only when it holds a grant of
	// its own: one holding nothing but MCP logins would leave the stripped
	// grant nowhere.
	globalAbsent, gErr := storeGrantAbsent(globalStore)
	switch {
	case err == nil && gErr == nil && !globalAbsent && sameAccount(configDir):
		if info.ModTime().After(globalInfo.ModTime()) {
			if err := copyFile(store, globalStore, 0600); err != nil {
				return fmt.Errorf("failed to copy newer credentials to global: %w", err)
			}
		}
		return nil
	case err != nil && !os.IsNotExist(err):
		return err
	}
	return setAsideOwnLogin(configDir, "launched on the token", true)
}

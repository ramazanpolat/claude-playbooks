package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// Mode is how a launch of a config directory would authenticate, decided
// exactly as PrepareLaunchEnv decides it, without touching anything.
type Mode string

const (
	ModeIsolated    Mode = "isolated"     // isolate_auth: own store, shares nothing
	ModeToken       Mode = "token"        // machine-global long-lived token injected
	ModeOwnToken    Mode = "own-token"    // a token the manifest or a profile sets
	ModeOwnLogin    Mode = "own-login"    // token unset for this playbook: stored login
	ModeSharedLogin Mode = "shared-login" // no token anywhere: stored login, shared store
	ModeError       Mode = "error"        // the launch would be refused (profile error)
)

// StoreKind describes what sits at <configDir>/.credentials.json.
type StoreKind string

const (
	StoreAbsent  StoreKind = "absent"
	StoreSymlink StoreKind = "symlink"
	StoreFile    StoreKind = "file"
)

// daemonRefreshLead is how early Claude Code's daemon refreshes a grant
// (expiresAt - 4min, from the 2.1.261 supervisor auth manager). A
// daemon-auth-status.json older than that instant for the CURRENT grant
// predates it and describes a previous grant: the file is never cleared on
// recovery, so age is the only way to tell "still broken" from "healed".
const daemonRefreshLead = 4 * time.Minute

// Report is the read-only authentication state of one config directory.
type Report struct {
	Name      string    `json:"name"`
	Dir       string    `json:"dir"`
	Mode      Mode      `json:"mode"`
	ModeError string    `json:"mode_error,omitempty"`
	Store     StoreKind `json:"store"`
	// StoreTarget is the symlink target when Store is StoreSymlink.
	StoreTarget string `json:"store_target,omitempty"`
	// HasGrant reports whether the store (through a link) holds claudeAiOauth.
	HasGrant bool `json:"has_grant"`
	// ExpiresAt is the grant's expiry, zero when unknown.
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	// Expired is true when a grant exists and its expiry has passed.
	Expired bool `json:"expired"`
	// DaemonStatus is the raw status from daemon-auth-status.json, "" when absent.
	DaemonStatus string    `json:"daemon_status,omitempty"`
	DaemonSince  time.Time `json:"daemon_since,omitempty"`
	// ReauthRequired is DaemonStatus == auth_required AND the marker is newer
	// than the current grant's refresh instant (see daemonRefreshLead).
	ReauthRequired bool `json:"reauth_required"`
	// TokenFile is the machine-global token file when it exists and is non-empty.
	TokenFile string `json:"token_file,omitempty"`
}

// Inspect reports the authentication state of configDir as a claude-playbook
// launch would see it. Nothing is written, no credential is read into the
// report beyond its expiry, and no process is spawned. now is injectable for
// tests.
func Inspect(name, configDir string, now time.Time) Report {
	return inspect(name, configDir, now, false)
}

// InspectGlobal is Inspect for ~/.claude, which is not launched by
// claude-playbook: a raw `claude` there never reads the token FILE (a
// claude-playbook convention), so only an exported CLAUDE_CODE_OAUTH_TOKEN
// counts as token mode.
func InspectGlobal(configDir string, now time.Time) Report {
	return inspect("~/.claude", configDir, now, true)
}

func inspect(name, configDir string, now time.Time, raw bool) Report {
	r := Report{Name: name, Dir: configDir}

	// Mode: the same decision PrepareLaunchEnv makes, minus its side effects.
	var menv *manifest.Env
	if m, _ := manifest.Nearest(configDir); m != nil {
		var err error
		menv, err = envprofile.Expand(envprofile.Dir(config.ResolvePlaybooksDir()), m.Env)
		if err != nil {
			r.Mode, r.ModeError = ModeError, err.Error()
		}
	}
	if r.Mode == "" {
		switch {
		case isAuthIsolated(configDir):
			r.Mode = ModeIsolated
		case menv.Unsets(OAuthTokenEnv):
			r.Mode = ModeOwnLogin
		case manifestToken(menv) != "":
			r.Mode = ModeOwnToken
		default:
			active := os.Getenv(OAuthTokenEnv) != ""
			if !raw {
				_, active = TokenActive()
			}
			if active {
				r.Mode = ModeToken
			} else {
				r.Mode = ModeSharedLogin
			}
		}
	}
	if f := OAuthTokenFile(); f != "" {
		if data, err := os.ReadFile(f); err == nil && len(trimSpace(data)) > 0 {
			r.TokenFile = f
		}
	}

	// Store shape and grant expiry.
	store := filepath.Join(configDir, CredentialsFileName)
	if info, err := os.Lstat(store); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			r.Store = StoreSymlink
			r.StoreTarget, _ = os.Readlink(store)
		} else {
			r.Store = StoreFile
		}
		if data, err := os.ReadFile(store); err == nil {
			var parsed struct {
				Grant *struct {
					ExpiresAt int64 `json:"expiresAt"`
				} `json:"claudeAiOauth"`
			}
			if json.Unmarshal(data, &parsed) == nil && parsed.Grant != nil {
				r.HasGrant = true
				if parsed.Grant.ExpiresAt > 0 {
					r.ExpiresAt = time.UnixMilli(parsed.Grant.ExpiresAt)
					r.Expired = !r.ExpiresAt.After(now)
				}
			}
		}
	} else {
		r.Store = StoreAbsent
	}

	// Daemon hint.
	if data, err := os.ReadFile(filepath.Join(configDir, "daemon-auth-status.json")); err == nil {
		var d struct {
			Status string `json:"status"`
			Since  int64  `json:"since"`
		}
		if json.Unmarshal(data, &d) == nil {
			r.DaemonStatus = d.Status
			if d.Since > 0 {
				r.DaemonSince = time.UnixMilli(d.Since)
			}
			if d.Status == "auth_required" {
				refreshAt := r.ExpiresAt.Add(-daemonRefreshLead)
				r.ReauthRequired = r.ExpiresAt.IsZero() || !r.DaemonSince.Before(refreshAt)
			}
		}
	}
	return r
}

// NeedsAttention summarises the report as one word, "" when all is well.
func (r Report) NeedsAttention() string {
	switch {
	case r.Mode == ModeError:
		return "launch refused"
	case r.ReauthRequired:
		return "re-auth required"
	case r.Mode == ModeToken || r.Mode == ModeOwnToken:
		return ""
	case !r.HasGrant:
		return "no login"
	case r.Expired:
		return "grant expired"
	}
	return ""
}

func trimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\n' || b[i] == '\r' || b[i] == '\t') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\n' || b[j-1] == '\r' || b[j-1] == '\t') {
		j--
	}
	return b[i:j]
}

// ErrNoGlobal is returned by GlobalDir when HOME cannot be resolved.
var ErrNoGlobal = errors.New("cannot resolve the global Claude config directory")

// GlobalDir returns ~/.claude, the shared store's owner, for inspection.
func GlobalDir() (string, error) {
	d, err := globalClaudeDir()
	if err != nil || d == "" {
		return "", ErrNoGlobal
	}
	return d, nil
}

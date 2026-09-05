package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
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
	// Isolated is true under isolate_auth, whatever the mode: an isolated
	// playbook may still authenticate by an own token (own-token) or by its
	// own stored login (isolated).
	Isolated bool `json:"isolated"`
	// HasGrant reports whether the store (through a link) holds claudeAiOauth.
	HasGrant bool `json:"has_grant"`
	// ExpiresAt is the grant's expiry, zero when unknown. Marshalled by
	// MarshalJSON so an unknown value is omitted rather than 0001-01-01.
	ExpiresAt time.Time `json:"-"`
	// Expired is true when a grant exists and its expiry has passed.
	Expired bool `json:"expired"`
	// DaemonStatus is the raw status from daemon-auth-status.json, "" when absent.
	DaemonStatus string    `json:"daemon_status,omitempty"`
	DaemonSince  time.Time `json:"-"`
	// ReauthRequired is DaemonStatus == auth_required AND the marker is newer
	// than the current grant's refresh instant (see daemonRefreshLead).
	ReauthRequired bool `json:"reauth_required"`
	// TokenFile is the machine-global token file when it exists and is non-empty.
	TokenFile string `json:"token_file,omitempty"`
}

// MarshalJSON emits the report with expires_at and daemon_since present only
// when known: encoding/json's omitempty does not apply to time.Time, and a
// script would otherwise read 0001-01-01 as a real instant.
func (r Report) MarshalJSON() ([]byte, error) {
	type plain Report
	out := struct {
		plain
		ExpiresAt   *time.Time `json:"expires_at,omitempty"`
		DaemonSince *time.Time `json:"daemon_since,omitempty"`
	}{plain: plain(r)}
	if !r.ExpiresAt.IsZero() {
		t := r.ExpiresAt
		out.ExpiresAt = &t
	}
	if !r.DaemonSince.IsZero() {
		t := r.DaemonSince
		out.DaemonSince = &t
	}
	return json.Marshal(out)
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

	if raw {
		// ~/.claude is not a playbook: no manifest, no profiles, no
		// isolation apply. Its mode is decided by the exported variable
		// alone (the token FILE is a claude-playbook convention).
		if os.Getenv(OAuthTokenEnv) != "" {
			r.Mode = ModeToken
		} else {
			r.Mode = ModeSharedLogin
		}
	} else {
		// Mode: the same decision PrepareLaunchEnv makes, minus its side
		// effects. Isolation is a manifest property and is reported even
		// when profile resolution fails.
		r.Isolated = isAuthIsolated(configDir)
		var menv *manifest.Env
		if m, _ := manifest.Nearest(configDir); m != nil {
			var err error
			menv, err = envprofile.Expand(envprofile.Dir(config.ResolvePlaybooksDir()), m.Env)
			if err != nil {
				r.Mode, r.ModeError = ModeError, sanitizeProfileError(err)
			}
		}
		if r.Mode == "" {
			_, setsToken := (map[string]string)(nil), false
			if menv != nil {
				_, setsToken = menv.Set[OAuthTokenEnv]
			}
			switch {
			case setsToken && manifestToken(menv) != "":
				// Honoured on the isolated path too: PrepareLaunchEnv injects
				// it and quarantines the stored grant exactly as elsewhere.
				r.Mode = ModeOwnToken
			case r.Isolated:
				r.Mode = ModeIsolated
			case menv.Unsets(OAuthTokenEnv):
				r.Mode = ModeOwnLogin
			case setsToken:
				// Set to an empty value: resolveToken treats that as
				// inactive, so the launch takes the stored-login path.
				r.Mode = ModeOwnLogin
			default:
				if _, active := TokenActive(); active {
					r.Mode = ModeToken
				} else {
					r.Mode = ModeSharedLogin
				}
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
			// The marker concerns the STORED login. Under a token mode the
			// launch quarantines that login and never uses it, so the
			// marker cannot mean "this playbook will fail to authenticate".
			// Judged only against a grant that exists: with no grant the
			// row already says "no login", and a leftover marker from some
			// earlier grant would otherwise read as fresh.
			// Freshness needs BOTH instants: a grant with no expiry or a
			// marker with no since cannot be ordered, and an unprovable
			// marker is reported as stale rather than as a live failure.
			if d.Status == "auth_required" && r.usesStoredLogin() && r.HasGrant && !r.ExpiresAt.IsZero() && !r.DaemonSince.IsZero() {
				refreshAt := r.ExpiresAt.Add(-daemonRefreshLead)
				r.ReauthRequired = !r.DaemonSince.Before(refreshAt)
			}
		}
	}
	return r
}

// usesStoredLogin reports whether the launch authenticates from the stored
// grant (as opposed to an injected token).
func (r Report) usesStoredLogin() bool {
	return r.Mode == ModeOwnLogin || r.Mode == ModeSharedLogin || r.Mode == ModeIsolated
}

// NeedsAttention summarises the report as one phrase, "" when all is well.
// Token modes have no stored login to judge and always report "". An
// expired grant is advisory: Claude Code refreshes it at launch while the
// refresh token is valid, so it is a warning, not a refusal.
func (r Report) NeedsAttention() string {
	switch {
	case r.Mode == ModeError:
		return "launch refused"
	case !r.usesStoredLogin():
		return ""
	case r.ReauthRequired:
		return "re-auth required"
	case !r.HasGrant:
		return "no login"
	case r.Expired:
		return "grant expired (refreshes at launch if the refresh token is still valid)"
	}
	return ""
}

// sanitizeProfileError renders a profile resolution error without echoing
// any file content: the TOML parser quotes the offending text, which in a
// profile may be a credential value.
func sanitizeProfileError(err error) string {
	var missing *envprofile.MissingError
	if errors.As(err, &missing) {
		return missing.Error() // names the profile and the directory only
	}
	var resolve *envprofile.ResolveError
	if errors.As(err, &resolve) {
		return "env profile " + strconv.Quote(resolve.Name) + " cannot be read or is invalid (content not shown; run: claude-playbook env-profile " + resolve.Name + ")"
	}
	return "env profile cannot be resolved (details withheld; see claude-playbook env-profile)"
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

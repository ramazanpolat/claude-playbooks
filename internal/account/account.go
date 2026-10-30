// Package account reads and writes accounts: one record per account that
// says where a playbook's requests go and how they authenticate (provider,
// address, identity, one credential, the provider's model names). Accounts
// are stored as TOML files under <playbooks root>/.accounts/ and named from
// playbook manifests and the DEFAULTS marker. At launch the routing,
// credential and model variables are derived from the account a playbook
// runs on, never set one by one (SPEC.md, "CREATE / ALTER / DROP ACCOUNT").
//
//	provider = "gateway"
//	base_url = "http://localhost:4000/v1"
//	model = "glm-5.3"
//
//	[auth]
//	kind = "auth_token"
//	ref = "keychain:glm"
package account

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/tomlfile"
)

// DirName is the account directory under the playbooks root. Dot-prefixed,
// so playbook discovery skips it.
const DirName = ".accounts"

// FileExt is the extension of an account file.
const FileExt = ".toml"

// DefaultMarker names the DEFAULTS account: one name, one line.
const DefaultMarker = ".default"

// Machine is the implicit account of a launch that names none: Anthropic,
// the playbook's own login and, on a shared playbook, the machine's token
// file. It is never stored, and no account may take its name.
const Machine = "machine"

// Providers.
const (
	Anthropic = "anthropic"
	Gateway   = "gateway"
	Bedrock   = "bedrock"
	Vertex    = "vertex"
	Foundry   = "foundry"
)

// Providers lists every provider, in the order messages name them.
var Providers = []string{Anthropic, Gateway, Bedrock, Vertex, Foundry}

// Credential kinds: the one way an account authenticates.
const (
	AuthToken   = "auth_token"   // ANTHROPIC_AUTH_TOKEN, a bearer token
	APIKey      = "api_key"      // ANTHROPIC_API_KEY, X-Api-Key
	ClaudeToken = "claude_token" // CLAUDE_CODE_OAUTH_TOKEN, a Claude subscription token
	Helper      = "helper"       // apiKeyHelper, a command that prints the key
	ClaudeLogin = "claude_login" // the playbook's own Claude login
	Cloud       = "cloud"        // the cloud's own credential chain
	None        = "none"         // no credential (a local router)
)

// SecretKinds are the credential kinds that hold a secret: a reference, or
// a literal stored knowingly (AS PLAINTEXT).
var SecretKinds = []string{AuthToken, APIKey, ClaudeToken}

// AuthValues are the kinds set with auth = '<kind>': they hold nothing.
var AuthValues = []string{ClaudeLogin, Cloud, None}

// Access values.
const (
	AccessAny    = "any"
	AccessListed = "listed"
)

// Auth is an account's one credential. Ref and Value are for the secret
// kinds (never both), Helper for the helper kind.
type Auth struct {
	Kind   string `toml:"kind"`
	Ref    string `toml:"ref,omitempty"`
	Value  string `toml:"value,omitempty"`
	Helper string `toml:"helper,omitempty"`
}

// Account is one account file.
type Account struct {
	Name               string `toml:"-"`
	Description        string `toml:"description,omitempty"`
	Provider           string `toml:"provider"`
	BaseURL            string `toml:"base_url,omitempty"`
	Identity           string `toml:"identity,omitempty"`
	Access             string `toml:"access,omitempty"`
	ClaudeLoginTrusted bool   `toml:"claude_login_trusted,omitempty"`
	DefaultModel       string `toml:"model,omitempty"`
	OpusModel          string `toml:"opus_model,omitempty"`
	SonnetModel        string `toml:"sonnet_model,omitempty"`
	HaikuModel         string `toml:"haiku_model,omitempty"`
	FableModel         string `toml:"fable_model,omitempty"`
	Auth               Auth   `toml:"auth"`
	// Headers are extra request headers, name -> secret reference
	// (headers.go).
	Headers map[string]string `toml:"headers,omitempty"`
}

// ModelKeys are the model keys, in the order SHOW CREATE writes them.
var ModelKeys = []string{"model", "opus_model", "sonnet_model", "haiku_model", "fable_model"}

// ModelOf returns the model key's value ("" for an unknown key).
func (a *Account) ModelOf(key string) string {
	switch key {
	case "model":
		return a.DefaultModel
	case "opus_model":
		return a.OpusModel
	case "sonnet_model":
		return a.SonnetModel
	case "haiku_model":
		return a.HaikuModel
	case "fable_model":
		return a.FableModel
	}
	return ""
}

// SetModel sets one model key; "" clears it. It reports false for a key
// that is not a model key.
func (a *Account) SetModel(key, value string) bool {
	switch key {
	case "model":
		a.DefaultModel = value
	case "opus_model":
		a.OpusModel = value
	case "sonnet_model":
		a.SonnetModel = value
	case "haiku_model":
		a.HaikuModel = value
	case "fable_model":
		a.FableModel = value
	default:
		return false
	}
	return true
}

// Listed reports whether only playbooks that list the account may use it.
func (a *Account) Listed() bool { return a.Access == AccessListed }

// UsesClaudeCredential reports whether the account authenticates with a
// Claude login or a Claude subscription token.
func (a *Account) UsesClaudeCredential() bool {
	return a.Auth.Kind == ClaudeLogin || a.Auth.Kind == ClaudeToken
}

// Dir returns the account directory for a playbooks root.
func Dir(playbooksDir string) string { return filepath.Join(playbooksDir, DirName) }

func path(dir, name string) string { return filepath.Join(dir, name+FileExt) }

// ValidateName reports whether name can name a new account: the account
// name rule (manifest.ValidateAccountName: lowercase, short), and not the
// implicit account's name. The name is never quoted back: whatever landed
// in a name slot may be a pasted value.
func ValidateName(name string) error {
	if err := manifest.ValidateAccountName(name); err != nil {
		return err
	}
	if name == Machine {
		return fmt.Errorf("%q names the implicit account (a launch that names none) and cannot name another", Machine)
	}
	return nil
}

// ErrAccount is matched (errors.Is) by every error a launch gets resolving
// an account: missing, unreadable, malformed or invalid. Launch paths treat
// all of them as fatal: running on another account than the one named would
// send requests and credentials somewhere the playbook did not choose.
var ErrAccount = errors.New("account cannot be resolved")

// MissingError reports an account that does not exist.
type MissingError struct {
	Name string
}

func (e *MissingError) Error() string {
	return fmt.Sprintf("account %q does not exist (create it with: cpb CREATE ACCOUNT %s SET provider = '<provider>', …)", e.Name, e.Name)
}

func (e *MissingError) Is(target error) bool { return target == ErrAccount }

// ResolveError wraps a read or validation failure of a named account.
type ResolveError struct {
	Name string
	Err  error
}

func (e *ResolveError) Error() string        { return fmt.Sprintf("account %q: %v", e.Name, e.Err) }
func (e *ResolveError) Unwrap() error        { return e.Err }
func (e *ResolveError) Is(target error) bool { return target == ErrAccount }

// Read parses one account. It returns (nil, nil) when none exists.
func Read(dir, name string) (*Account, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	at := path(dir, name)
	data, err := os.ReadFile(at)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var a Account
	if err := tomlfile.Decode(at, data, &a); err != nil {
		if errors.As(err, new(*tomlfile.UnknownKeyError)) {
			return nil, err
		}
		return nil, fmt.Errorf("invalid account at %s: %s", at, manifest.SanitizeTOMLError(err))
	}
	a.Name = name
	if err := a.Validate(); err != nil {
		return nil, fmt.Errorf("invalid account at %s: %w", at, err)
	}
	return &a, nil
}

// Must reads an account that has to exist: a missing one is a
// *MissingError, any other failure a *ResolveError, both ErrAccount.
func Must(dir, name string) (*Account, error) {
	a, err := Read(dir, name)
	if err != nil {
		return nil, &ResolveError{Name: name, Err: err}
	}
	if a == nil {
		return nil, &MissingError{Name: name}
	}
	return a, nil
}

// Validate checks the record whole: a provider, a credential the provider
// takes, and an address that may carry it. Errors never quote a value or a
// reference.
func (a *Account) Validate() error {
	switch a.Provider {
	case "":
		return errors.New("no provider: SET provider = '" + strings.Join(Providers, "' | '") + "'")
	case Anthropic, Gateway, Bedrock, Vertex, Foundry:
	default:
		return errors.New("unknown provider: use '" + strings.Join(Providers, "', '") + "'")
	}
	switch a.Access {
	case "", AccessAny, AccessListed:
	default:
		return errors.New("access takes 'any' or 'listed'")
	}
	if err := a.Auth.validate(); err != nil {
		return err
	}
	var u *url.URL
	if a.BaseURL != "" {
		var err error
		if u, err = ParseBaseURL(a.BaseURL); err != nil {
			return err
		}
	}
	kind := a.Auth.Kind
	switch a.Provider {
	case Anthropic:
		if u != nil && !IsAnthropicURL(u) {
			return errors.New("an anthropic account's base_url is https on anthropic.com or a subdomain; another address is a provider = 'gateway' account")
		}
		if kind == Cloud || kind == None {
			return fmt.Errorf("an anthropic account authenticates with api_key, auth_token, claude_token, helper or auth = 'claude_login', not auth = '%s'", kind)
		}
	case Gateway:
		if u == nil {
			return errors.New("a gateway account needs base_url: where its requests go")
		}
		if kind == Cloud {
			return errors.New("a gateway account authenticates with auth_token, api_key, helper or auth = 'none', not auth = 'cloud'")
		}
		if kind == ClaudeLogin || kind == ClaudeToken {
			if !a.ClaudeLoginTrusted {
				return fmt.Errorf("%s would send your Claude credential to %s, which is not Anthropic: give the gateway its own key (auth_token FROM '<ref>'), or, for a gateway you trust that forwards to Anthropic, SET claude_login_trusted = true", authLabel(kind), u.Host)
			}
			if u.Scheme != "https" {
				return errors.New("claude_login_trusted sends your Claude credential only over https: this base_url is http")
			}
		}
	case Bedrock, Vertex, Foundry:
		if kind != Cloud {
			return fmt.Errorf("a %s account authenticates with the cloud's own credentials: auth = 'cloud'", a.Provider)
		}
	}
	if a.ClaudeLoginTrusted && a.Provider != Gateway {
		return errors.New("claude_login_trusted applies to a gateway account only")
	}
	return a.validateHeaders()
}

func authLabel(kind string) string {
	if kind == ClaudeLogin {
		return "auth = 'claude_login'"
	}
	return kind
}

func (au *Auth) validate() error {
	switch au.Kind {
	case "":
		return errors.New("no credential: SET auth_token FROM '<ref>', api_key FROM '<ref>', claude_token FROM '<ref>', helper = '<command>', or auth = 'claude_login' | 'cloud' | 'none'")
	case AuthToken, APIKey, ClaudeToken:
		switch {
		case au.Ref != "" && au.Value != "":
			return fmt.Errorf("%s holds a reference or a value, not both", au.Kind)
		case au.Ref == "" && au.Value == "":
			return fmt.Errorf("%s needs a reference: %s FROM '<ref>'", au.Kind, au.Kind)
		case au.Ref != "" && !manifest.LooksLikeRef(au.Ref):
			return fmt.Errorf("%s: not a secret reference (a scheme form such as 'keychain:<service>')", au.Kind)
		case au.Helper != "":
			return fmt.Errorf("%s takes no helper", au.Kind)
		}
		if strings.ContainsAny(au.Value, "\r\n\x00") {
			return fmt.Errorf("%s: a value holds no line breaks", au.Kind)
		}
	case Helper:
		if au.Ref != "" || au.Value != "" {
			return errors.New("helper takes a command, not a secret")
		}
		if strings.TrimSpace(au.Helper) == "" {
			return errors.New("helper needs a command: helper = '<command>'")
		}
		if strings.ContainsAny(au.Helper, "\r\n\x00") {
			return errors.New("helper: the command is one line")
		}
	case ClaudeLogin, Cloud, None:
		if au.Ref != "" || au.Value != "" || au.Helper != "" {
			return fmt.Errorf("auth = '%s' holds nothing", au.Kind)
		}
	default:
		return errors.New("unknown credential kind")
	}
	return nil
}

// ParseBaseURL parses an account's base_url: https, or http only to this
// machine (localhost, 127.0.0.1, ::1); a host; no user:password. The value
// is never quoted back: it may carry a credential.
func ParseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return nil, errors.New("base_url is not a URL with a host (https://host[:port][/path])")
	}
	if u.User != nil {
		return nil, errors.New("base_url carries a user or password: give the credential as auth_token FROM '<ref>' instead")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !IsLoopback(u.Hostname()) {
			return nil, errors.New("base_url is http to another machine: credentials would cross the network unencrypted; use https (http only to localhost, 127.0.0.1 or ::1)")
		}
	default:
		return nil, errors.New("base_url takes https (or http to localhost)")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("base_url takes no query or fragment")
	}
	return u, nil
}

// IsLoopback reports a host that names this machine.
func IsLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// IsAnthropicURL reports an https address on anthropic.com or a subdomain,
// on the default port.
func IsAnthropicURL(u *url.URL) bool {
	h := strings.ToLower(u.Hostname())
	if strings.ToLower(u.Scheme) != "https" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	return h == "anthropic.com" || strings.HasSuffix(h, ".anthropic.com")
}

// Write serializes an account, creating the directory on first use, mode
// 0600 through a temp file and a rename (a value written AS PLAINTEXT must
// never be readable by others, not even for a moment).
func Write(dir string, a *Account) error {
	if err := ValidateName(a.Name); err != nil {
		return err
	}
	if err := a.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var b strings.Builder
	line := func(key, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%s = %s\n", key, manifest.QuoteTOML(value))
		}
	}
	line("description", a.Description)
	line("provider", a.Provider)
	line("base_url", a.BaseURL)
	line("identity", a.Identity)
	if a.Access == AccessListed {
		line("access", a.Access)
	}
	if a.ClaudeLoginTrusted {
		b.WriteString("claude_login_trusted = true\n")
	}
	for _, k := range ModelKeys {
		line(k, a.ModelOf(k))
	}
	b.WriteString("\n[auth]\n")
	line("kind", a.Auth.Kind)
	line("ref", a.Auth.Ref)
	line("value", a.Auth.Value)
	line("helper", a.Auth.Helper)
	if names := a.HeaderNames(); len(names) > 0 {
		// name = reference; a name is quoted like any key with a dash.
		b.WriteString("\n[headers]\n")
		for _, h := range names {
			fmt.Fprintf(&b, "%s = %s\n", manifest.QuoteTOML(h), manifest.QuoteTOML(a.Headers[h]))
		}
	}
	return manifest.WritePrivate(path(dir, a.Name), []byte(b.String()), 0o600)
}

// Delete removes an account file; removing one that does not exist is not
// an error.
func Delete(dir, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	err := os.Remove(path(dir, name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// List returns every account in dir, sorted by name. A directory that does
// not exist lists nothing; an unreadable account is an error, so a listing
// never hides the account a launch is about to fail on.
func List(dir string) ([]*Account, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Account
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), FileExt)
		if !ok || e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		a, err := Read(dir, name)
		if err != nil {
			return nil, &ResolveError{Name: name, Err: err}
		}
		if a != nil {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Default returns the DEFAULTS account, "" for none. Only an absent marker
// means none: an empty marker, an invalid name or a dangling link is an
// error, so a launch never silently runs on another account than the one
// DEFAULTS names.
func Default(dir string) (string, error) {
	marker := filepath.Join(dir, DefaultMarker)
	data, err := os.ReadFile(marker)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, lerr := os.Lstat(marker); lerr == nil {
				return "", fmt.Errorf("%s: dangling symlink", marker)
			}
			return "", nil
		}
		return "", err
	}
	name := strings.TrimSpace(string(data))
	if name == "" {
		return "", fmt.Errorf("%s: empty DEFAULTS account marker", marker)
	}
	if strings.ContainsAny(name, "\r\n") {
		return "", fmt.Errorf("%s: DEFAULTS names one account", marker)
	}
	if err := ValidateName(name); err != nil {
		return "", fmt.Errorf("%s: %w", marker, err)
	}
	return name, nil
}

// WriteDefault records name as the DEFAULTS account; "" removes the marker.
func WriteDefault(dir, name string) error {
	if name == "" {
		err := os.Remove(filepath.Join(dir, DefaultMarker))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return manifest.WritePrivate(filepath.Join(dir, DefaultMarker), []byte(name+"\n"), 0o600)
}

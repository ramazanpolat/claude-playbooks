package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

var envCmd = &cobra.Command{
	Use:   "env [name] [set KEY=VALUE... | unset KEY... | clear KEY... | use PROFILE... | unuse PROFILE...]",
	Short: "Show or manage a playbook's environment overrides",
	Long: `A playbook can declare environment variables in the [env] block of its
.playbook manifest. Every launch of that playbook (its launcher command,
'run', or 'start' at its directory) applies them to the child claude
process: 'set' entries override whatever the shell exported, 'unset'
entries are removed even when the shell exports them.

With no arguments: list every playbook that declares overrides.
With a name: show that playbook's overrides.
  set KEY=VALUE...   record values (replacing any previous ones)
  unset KEY...       remove the variables from every launch
  clear KEY...       forget the entries; the shell's values apply again
  use PROFILE...     layer shared env profiles under this playbook's entries
  unuse PROFILE...   detach profiles

Profiles ('claude-playbook env-profile') apply in the order listed, later ones
overriding earlier; the playbook's own set/unset entries apply last.

Unsetting CLAUDE_CODE_OAUTH_TOKEN switches the playbook to stored
credentials: the machine-global long-lived token is neither injected nor
allowed to quarantine the playbook's own login, so /login sticks there.

The block is install-local. 'update' keeps it and ignores the source's;
'install' drops one the source ships. CLAUDE_CONFIG_DIR cannot be
overridden.`,
	Args:              cobra.ArbitraryArgs,
	ValidArgsFunction: autocompletePlaybookNames,
	RunE:              runEnv,
}

// revealSecrets backs --reveal on env, env-profile, and info: without it,
// credential-looking values are redacted in every status display that would
// otherwise print them (see printEnvBlock and displayEnvValue).
var revealSecrets bool

func init() {
	envCmd.Flags().BoolVar(&revealSecrets, "reveal", false, "show credential-looking values instead of redacting them")
}

func runEnv(cmd *cobra.Command, args []string) error {
	playbooksDir := config.ResolvePlaybooksDir()
	profileDir := envprofile.Dir(playbooksDir)

	if len(args) == 0 {
		pbs, err := playbook.Discover(playbooksDir)
		if err != nil {
			return err
		}
		shown := 0
		for _, pb := range pbs {
			if pb.Manifest == nil || pb.Manifest.Env.Empty() {
				continue
			}
			fmt.Printf("%s\n", pb.Name)
			printEnvBlock("  ", pb.Manifest.Env, revealSecrets)
			shown++
		}
		if shown == 0 {
			fmt.Println("No playbook declares environment overrides.")
			fmt.Println("Use 'claude-playbook env <name> set KEY=VALUE' or 'claude-playbook env <name> unset KEY' to add some.")
		}
		return nil
	}

	name := args[0]
	if len(args) == 1 {
		pb, err := playbook.Require(playbooksDir, name)
		if err != nil {
			return err
		}
		defaultName, derr := envprofile.Default(profileDir)
		if derr != nil {
			// Shown, not returned: the pilot came here to see the launch's
			// environment, and "refused" is the true answer.
			fmt.Printf("Registry default marker is invalid (%v): every launch is refused until 'claude-playbook env-profile <name> undefault' clears it.\n", derr)
		}
		governing, governingDir := governingManifest(pb)
		if governingDir != "" {
			if manifest.Exists(pb.RootPath) {
				fmt.Printf("Note: %s governs the launch of %q (nearest manifest to its config directory); the root manifest that 'claude-playbook env %s set' edits is not applied.\n", filepath.Join(governingDir, manifest.FileName), name, name)
			} else {
				fmt.Printf("Note: %s governs the launch of %q (nearest manifest to its config directory, which has none of its own); 'claude-playbook env %s set' would create a root manifest, which would then govern instead.\n", filepath.Join(governingDir, manifest.FileName), name, name)
			}
		}
		var block *manifest.Env
		if governing != nil {
			block = governing.Env
		}
		if block.Empty() {
			fmt.Printf("Playbook %q declares no environment overrides.\n", name)
			if defaultName != "" {
				// The default still decides this playbook's launch: show
				// what it contributes, or that it would refuse.
				effective, err := envprofile.ExpandWithDefault(profileDir, nil)
				if err != nil {
					fmt.Printf("Registry default profile %q applies to it, and the launch is refused: %v\n", defaultName, err)
				} else {
					fmt.Printf("Registry default profile %q applies to it. Effective at launch:\n", defaultName)
					printEnvBlock("  ", effective, revealSecrets)
				}
			}
			fmt.Printf("Use 'claude-playbook env %s set KEY=VALUE' or 'claude-playbook env %s unset KEY' to add some.\n", name, name)
			return nil
		}
		fmt.Printf("Environment overrides for %q:\n", name)
		if defaultName != "" {
			fmt.Printf("  default   %s\n", defaultName)
		}
		printEnvBlock("  ", block, revealSecrets)
		if len(block.Profiles) > 0 || defaultName != "" {
			effective, err := envprofile.ExpandWithDefault(profileDir, block)
			if err != nil {
				fmt.Printf("Effective at launch: launch refused -- %v\n", err)
				return nil
			}
			fmt.Println("Effective at launch:")
			printEnvBlock("  ", effective, revealSecrets)
		}
		return nil
	}

	verb := args[1]
	keys := args[2:]
	switch verb {
	case "set", "unset", "clear", "use", "unuse":
	default:
		return fmt.Errorf("unknown action %q: expected set, unset, clear, use, or unuse\nUsage: claude-playbook env <name> [set KEY=VALUE... | unset KEY... | clear KEY... | use PROFILE... | unuse PROFILE...]", verb)
	}
	if len(keys) == 0 {
		switch verb {
		case "set":
			return fmt.Errorf("set requires at least one KEY=VALUE")
		case "use", "unuse":
			return fmt.Errorf("%s requires at least one profile name", verb)
		}
		return fmt.Errorf("%s requires at least one KEY", verb)
	}

	// Parse and validate everything BEFORE locking or touching the manifest:
	// a bad third argument must not leave the first two applied.
	set := map[string]string{}
	var names []string
	if verb == "use" || verb == "unuse" {
		for _, arg := range keys {
			if err := manifest.ValidateProfileName(arg); err != nil {
				return err
			}
			names = append(names, arg)
		}
		keys = nil
	}
	for _, arg := range keys {
		key, value := arg, ""
		if verb == "set" {
			k, v, ok := strings.Cut(arg, "=")
			if !ok {
				return fmt.Errorf("set expects KEY=VALUE, got %q", arg)
			}
			key, value = k, v
		} else if strings.Contains(arg, "=") {
			return fmt.Errorf("%s expects a variable name, got %q", verb, arg)
		}
		if err := manifest.ValidateEnvKey(key); err != nil {
			return err
		}
		set[key] = value
		names = append(names, key)
	}

	// Manifest mutations serialize under the registry lock like alias does:
	// two concurrent edits would otherwise race on the read-modify-write.
	unlock, lerr := lockRegistry()
	if lerr != nil {
		return lerr
	}
	defer unlock()

	pb, err := playbook.Require(playbooksDir, name)
	if err != nil {
		return err
	}
	if verb == "use" {
		// A profile must exist to be attached: launch refuses a missing one,
		// and recording it would only arm that. Checked under the lock, so
		// a concurrent `env-profile delete` (which verifies no user under
		// the same lock) cannot slip between the check and the write.
		for _, arg := range names {
			p, err := envprofile.Read(profileDir, arg)
			if err != nil {
				return err
			}
			if p == nil {
				return fmt.Errorf("unknown env profile %q. Create it with 'claude-playbook env-profile %s set KEY=VALUE'", arg, arg)
			}
		}
	}
	// A linked playbook's manifest is shared with every registration of the
	// target directory -- same refusal as alias and rename.
	if info, lerr := os.Lstat(pb.RootPath); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cannot change environment overrides on a linked target's shared %s. Edit the target's manifest directly if you really mean it", manifest.FileName)
	}

	m := pb.Manifest
	if m == nil {
		m = &manifest.Manifest{Name: pb.Name}
	}
	if m.Env == nil {
		m.Env = &manifest.Env{}
	}
	if m.Env.Set == nil {
		m.Env.Set = map[string]string{}
	}
	for _, key := range names {
		switch verb {
		case "use":
			m.Env.Profiles = dropString(m.Env.Profiles, key)
			m.Env.Profiles = append(m.Env.Profiles, key)
		case "unuse":
			m.Env.Profiles = dropString(m.Env.Profiles, key)
		default:
			m.Env.Unset = dropString(m.Env.Unset, key)
			delete(m.Env.Set, key)
			switch verb {
			case "set":
				m.Env.Set[key] = set[key]
			case "unset":
				m.Env.Unset = append(m.Env.Unset, key)
			}
		}
	}
	if m.Env.Empty() {
		m.Env = nil
	}
	if err := manifest.Write(pb.RootPath, m); err != nil {
		return fmt.Errorf("cannot record environment overrides: %w", err)
	}

	switch verb {
	case "set":
		for _, key := range names {
			fmt.Printf("Set %s for playbook %q\n", key, name)
		}
	case "unset":
		for _, key := range names {
			fmt.Printf("Unset %s for playbook %q\n", key, name)
		}
	case "clear":
		for _, key := range names {
			fmt.Printf("Cleared %s for playbook %q (the shell's value applies again)\n", key, name)
		}
	case "use":
		fmt.Printf("Playbook %q now uses env profiles: %s\n", name, strings.Join(m.Env.Profiles, ", "))
	case "unuse":
		for _, key := range names {
			fmt.Printf("Detached env profile %q from playbook %q\n", key, name)
		}
	}
	for _, key := range names {
		if key == auth.OAuthTokenEnv && verb == "unset" {
			fmt.Printf("Playbook %q now authenticates from stored credentials: the long-lived token is not injected and its login is left alone. Run it and /login once if it asks.\n", name)
		}
	}
	return nil
}

func printEnvBlock(indent string, e *manifest.Env, reveal bool) {
	if len(e.Profiles) > 0 {
		fmt.Printf("%sprofiles  %s\n", indent, strings.Join(e.Profiles, ", "))
	}
	keys := make([]string, 0, len(e.Set))
	for key := range e.Set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Printf("%sset    %s=%s\n", indent, key, displayEnvValue(key, e.Set[key], reveal))
	}
	unset := append([]string(nil), e.Unset...)
	sort.Strings(unset)
	for _, key := range unset {
		fmt.Printf("%sunset  %s\n", indent, key)
	}
}

// looksLikeSecretKey is manifest.LooksLikeSecretKey; see there for the rules.
func looksLikeSecretKey(key string) bool { return manifest.LooksLikeSecretKey(key) }

// displayEnvValue is what every env status display prints for a value:
// redacted by default when the key looks like a credential, full value when
// it doesn't or --reveal was passed.
func displayEnvValue(key, value string, reveal bool) string {
	if reveal || value == "" {
		return value
	}
	if looksLikeSecretKey(key) {
		return redactSecretValue(value)
	}
	return redactURLCredentials(value)
}

// urlUserinfo matches the credential an absolute URL carries before its
// host. Anchored on "://" so a bare "user:pass@host" or a mailto: address is
// left alone. The authority ends at the first "/", "?" or "#", so none of
// them may appear in userinfo -- without "?" and "#" here, the "@" in
// https://service.test?email=a@example.com reads as a userinfo delimiter and
// an ordinary callback URL is mangled as though it carried a credential.
// RFC 3986 requires both to be percent-encoded inside userinfo anyway, so
// excluding them cannot miss a real one.
var urlUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^/?#@\s]+)@`)

// redactURLCredentials masks the credential inside a connection URL. Every
// other rule here reads the key, and this case cannot: DATABASE_URL,
// REDIS_URL, AMQP_URL and MONGODB_URI all name nothing secret while carrying
// a password in the value. Only the credential is masked, not the whole
// value -- the scheme, host and database are what the pilot came to read,
// and a wholly masked DATABASE_URL would just train them to reach for
// --reveal, which is how a feature like this stops being used.
func redactURLCredentials(value string) string {
	return urlUserinfo.ReplaceAllStringFunc(value, func(match string) string {
		parts := urlUserinfo.FindStringSubmatch(match)
		scheme, userinfo := parts[1], parts[2]
		left, right, hasColon := strings.Cut(userinfo, ":")
		if !hasColon {
			return scheme + redactSecretValue(userinfo) + "@"
		}
		return scheme + maskUserinfoField(left) + ":" + maskUserinfoField(right) + "@"
	})
}

// maskUserinfoField masks one side of a URL's user:password pair. Both sides
// are masked, because the colon says only that there are two fields -- never
// which one holds the secret. postgres://user:pw@host keeps it on the right,
// while https://TOKEN:x-oauth-basic@host (GitHub's documented form) and
// https://TOKEN:@host (what `git credential` writes) keep it on the left
// beside a dummy or empty password. Nothing in the structure distinguishes
// them, so masking only one side leaks the other half the time, and the
// username is the cheaper thing to lose. An empty field stays empty: there
// is nothing to hide, and "<redacted, 0 chars>" is noise that also advertises
// which shape this is.
func maskUserinfoField(field string) string {
	if field == "" {
		return ""
	}
	return redactSecretValue(field)
}

// redactSecretValue keeps a few characters at each end -- enough to tell
// which credential is attached without disclosing it -- and states the
// length outright rather than leaving it to be inferred from a masked run of
// characters. At least minHidden characters always stay hidden, so anything
// under 12 characters is redacted whole rather than showing half of itself:
// PASSWORD is in scope above, and a human-chosen password is both short and
// guessable enough that half of one is most of one. Runes, not bytes, so a
// value is never sliced through a multi-byte character.
func redactSecretValue(value string) string {
	const minHidden = 8
	r := []rune(value)
	n := len(r)
	keep := n / 4
	if keep > 4 {
		keep = 4
	}
	if most := (n - minHidden) / 2; keep > most {
		keep = most
	}
	if keep < 2 {
		return fmt.Sprintf("<redacted, %d chars>", n)
	}
	return fmt.Sprintf("%s...%s (%d chars)", string(r[:keep]), string(r[n-keep:]), n)
}

func dropString(list []string, s string) []string {
	out := list[:0:0]
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

// governingManifest returns the manifest a launch of pb consults, resolved
// exactly as the launch resolves it: the nearest valid manifest walking up
// from the config directory (manifest.NearestPath, the lookup behind
// PrepareLaunchEnv and auth status). Usually that is the playbook's own root
// manifest, and the returned directory is "". It is the directory of the
// governing manifest when that is some other file: a legacy `subdir` layout
// whose subdirectory carries a manifest of its own, or a manifest-free
// playbook under an ancestor directory that has one.
func governingManifest(pb *playbook.Playbook) (*manifest.Manifest, string) {
	m, dir, _ := manifest.NearestPath(pb.Path)
	if m == nil || dir == pb.RootPath {
		return m, ""
	}
	return m, dir
}

// Package manifest reads and writes the .playbook TOML file inside a playbook
// directory. The file is optional and holds metadata only; a directory is a
// valid playbook with or without it.
package manifest

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/tomlfile"
)

const FileName = ".playbook"

// Source holds provenance data used by native updates.
type Source struct {
	Repository string `toml:"repository,omitempty"`
	Branch     string `toml:"branch,omitempty"`
	Subdir     string `toml:"subdir,omitempty"`
}

// Update holds per-playbook update policy. Preserve names install-local files
// that must survive an update even though the source ships its own copy; the
// CLI already preserves settings.json and the Claude Code state files, so this
// is for anything beyond that. Migrate is the playbook's migration step: a
// script `cpb update` runs, once you consent, after the new files are in
// place, as `<script> <from-version> <to-version> <install-dir>`. Without it
// no migration runs. Paths are relative to the playbook root.
type Update struct {
	Preserve []string `toml:"preserve,omitempty"`
	Migrate  string   `toml:"migrate,omitempty"`
}

// Env holds per-install environment overrides applied by `run`, `start`, and
// launcher dispatch to the child claude process, after the process's own
// environment and before CLAUDE_CONFIG_DIR is bound. Set entries override
// inherited values; Block entries are removed from the child's environment
// even when the shell exports them.
//
// The [env] table is INSTALL-LOCAL state, like `launcher`: `update` carries the live
// block forward and ignores the source's, and `install` drops a block the
// source ships. A playbook repository must not be able to point an install's
// ANTHROPIC_BASE_URL somewhere else by publishing a manifest.
//
// Blocking CLAUDE_CODE_OAUTH_TOKEN has a documented side effect: the
// long-lived token is treated as inactive for that install, so the launch
// takes the stored-credentials path (no quarantine, no injection). Setting
// it supplies a per-install token that wins over the machine-global file.
//
// Sets names shared env sets (files under the playbooks root's .env-sets/
// directory) layered UNDER this table: sets apply in list order, later ones
// overriding earlier, and the table's own Set/Block apply last. Resolution happens at launch; the manifest records names only.
//
// Refs holds secret REFERENCES (keychain:…, op://…), never values: the
// launch execs claude through the configured secret helper, which resolves
// them (SPEC.md, "Secrets"). A key lives in at most one of Set,
// Refs and Block.
type Env struct {
	Sets  []string          `toml:"sets,omitempty"`
	Set   map[string]string `toml:"set,omitempty"`
	Refs  map[string]string `toml:"refs,omitempty"`
	Block []string          `toml:"block,omitempty"`
}

var profileNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateSetName reports whether name can name an env set file.
func ValidateSetName(name string) error {
	if !profileNamePattern.MatchString(name) {
		return fmt.Errorf("invalid env set name %q: use letters, digits, dots, dashes, underscores", name)
	}
	return nil
}

// Uses reports whether profile is listed.
func (e *Env) Uses(profile string) bool {
	if e == nil {
		return false
	}
	for _, p := range e.Sets {
		if p == profile {
			return true
		}
	}
	return false
}

// MergeEnv flattens layers into one block: within a layer, Refs, then Set,
// then Unset. A Ref or a Set entry overrides the key's earlier value,
// reference or removal; an Unset entry drops it. Profiles are not carried
// into the result -- callers resolve them into layers first. The result
// lists a key in at most one of Set, Refs and Unset, and Unset keeps
// first-seen order.
func MergeEnv(layers ...*Env) *Env {
	out := &Env{Set: map[string]string{}}
	for _, layer := range layers {
		if layer == nil {
			continue
		}
		for key, ref := range layer.Refs {
			out.Block = dropKey(out.Block, key)
			delete(out.Set, key)
			if out.Refs == nil {
				out.Refs = map[string]string{}
			}
			out.Refs[key] = ref
		}
		for key, value := range layer.Set {
			out.Block = dropKey(out.Block, key)
			delete(out.Refs, key)
			out.Set[key] = value
		}
		for _, key := range layer.Block {
			delete(out.Set, key)
			delete(out.Refs, key)
			if !out.Blocks(key) {
				out.Block = append(out.Block, key)
			}
		}
	}
	return out
}

func dropKey(list []string, key string) []string {
	out := list[:0:0]
	for _, k := range list {
		if k != key {
			out = append(out, k)
		}
	}
	return out
}

var tomlLinePattern = regexp.MustCompile(`line (\d+)`)

// SanitizeTOMLError reduces a TOML decode error to its line number. The
// parser quotes the offending text in its messages, and since [env.set] and
// profile values may be credentials, that text must never reach a terminal
// or a log through any command that reads the file.
func SanitizeTOMLError(err error) string {
	if m := tomlLinePattern.FindStringSubmatch(err.Error()); m != nil {
		return "TOML syntax error at line " + m[1] + " (content not shown)"
	}
	return "TOML syntax error (content not shown)"
}

// QuoteTOML renders s as a TOML basic string. Go's %q emits escapes TOML
// does not define (\a, \v, \x..), which turn a manifest holding such a value
// unreadable -- and an unreadable manifest aborts registry discovery for
// every command. Control characters go out as \uXXXX; s must be valid
// UTF-8, which validate enforces for env values before anything is written.
func QuoteTOML(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// ValidateEnvValue reports whether value can be stored AND passed on: TOML
// strings must be valid UTF-8, so a value that cannot be written faithfully
// is refused rather than corrupt the manifest; and a NUL byte, which TOML
// can carry as \u0000, would make os/exec reject the child's environment
// and fail every launch, so it is refused on read as well as on write.
func ValidateEnvValue(key, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("value of %s is not valid UTF-8 and cannot be stored in a manifest", key)
	}
	if strings.ContainsRune(value, 0) {
		return fmt.Errorf("value of %s contains a NUL byte, which cannot be passed in an environment", key)
	}
	return nil
}

// ReservedEnvKeys cannot be set or unset through the manifest, a profile,
// --env or --env-file: the tool owns them and binds them after every override
// is applied.
//
// CPB_CONFIG_DIR is reserved for a subtler reason than
// CLAUDE_CONFIG_DIR. Declaring it could never redirect the launch that
// declares it (the request is read from the process environment before any
// layer is applied), but it would place the variable in the child's
// environment, from where it WOULD redirect a further launch made inside the
// session. A key that cannot do the thing it names, yet silently affects the
// next launch, is worth refusing outright.
var ReservedEnvKeys = map[string]bool{
	"CLAUDE_CONFIG_DIR":         true,
	config.ConfigDirOverrideEnv: true,
}

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateEnvKey reports whether key is a well-formed, non-reserved
// environment variable name.
func ValidateEnvKey(key string) error {
	if !envKeyPattern.MatchString(key) {
		return fmt.Errorf("invalid environment variable name %q", key)
	}
	if ReservedEnvKeys[key] {
		return fmt.Errorf("%s is managed by cpb and cannot be overridden", key)
	}
	return nil
}

// Empty reports whether the block declares nothing.
func (e *Env) Empty() bool {
	return e == nil || (len(e.Sets) == 0 && len(e.Set) == 0 && len(e.Refs) == 0 && len(e.Block) == 0)
}

// Blocks reports whether key is listed for removal.
func (e *Env) Blocks(key string) bool {
	if e == nil {
		return false
	}
	for _, k := range e.Block {
		if k == key {
			return true
		}
	}
	return false
}

// Manifest holds the parsed contents of a .playbook file.
type Manifest struct {
	Version       string   `toml:"version"`
	Name          string   `toml:"name"`
	Launcher      string   `toml:"launcher"`
	Description   string   `toml:"description"`
	Homepage      string   `toml:"homepage"`
	Author        string   `toml:"author"`
	IsolatedLogin bool     `toml:"isolated_login"`
	Source        *Source  `toml:"source,omitempty"`
	Update        *Update  `toml:"update,omitempty"`
	Env           *Env     `toml:"env,omitempty"`
	Sandbox       *Sandbox `toml:"sandbox,omitempty"`

	// MCP records, per MCP server a statement declared, the variables cpb
	// derived for its secret references, so dropping the server forgets
	// exactly those (SPEC.md, "MCP servers").
	MCP map[string]*MCPRecord `toml:"mcp,omitempty"`

	// Skills records, per skill a statement added, where it came from and
	// how it was put in place, so DROP SKILL removes only what cpb added and
	// update restores it (SPEC.md, "Skills").
	Skills map[string]*SkillRecord `toml:"skills,omitempty"`

	// Play records where a kept played recipe came from, so `cpb update`
	// can fetch it again (v4.0.0; docs/guides/play.md).
	Play *Play `toml:"play,omitempty"`
}

// Play is the [play] record of a playbook `cpb play --keep` built.
type Play struct {
	// Ref is what was played, as `cpb update` resolves it again: a template
	// name, a URL, a github: ref, or a local file's absolute path.
	Ref string `toml:"ref"`
	// URL is the address the bytes came from; empty for a local file.
	URL    string `toml:"url,omitempty"`
	SHA256 string `toml:"sha256"`
	// PlayedAt is when, in RFC 3339, UTC.
	PlayedAt string `toml:"played_at"`
}

// SkillRecord is one skill cpb put at <config>/skills/<name>.
type SkillRecord struct {
	Source string `toml:"source"`
	Branch string `toml:"branch,omitempty"`
	Subdir string `toml:"subdir,omitempty"`
	Mode   string `toml:"mode"` // "link" (a directory) or "copy" (a git source)
}

// MCPRecord is what cpb derived for one MCP server.
type MCPRecord struct {
	Vars []string `toml:"vars"`
}

// Sandbox describes how `run --sandbox` boxes this playbook: what of the
// host it may see beyond its own directory and the working directory, what
// it may reach on the network beyond the sandbox policy, and which Claude
// Code to run inside.
type Sandbox struct {
	// Always sandboxes every launch of this playbook (run and launcher
	// dispatch; start for a directory carrying the manifest). A launch
	// passes --no-sandbox to override it, loudly.
	Always bool `toml:"always,omitempty"`
	// Backend names the sandbox implementation ("sbx"); empty means the
	// default.
	Backend string `toml:"backend,omitempty"`
	// ShareSkills mounts the backend's shared skills store into the
	// sandbox (sbx does so by default; cpb does not, since a
	// sandbox could then plant a skill a later sandbox runs).
	ShareSkills bool `toml:"share_skills,omitempty"`
	// Host names the machine the sandbox runs on ("user@host", reached
	// over ssh), where cpb and this playbook are installed;
	// empty runs the sandbox here.
	Host string `toml:"host,omitempty"`
	// Secrets says how backend API keys reach the sandbox: "proxy" (the
	// default) registers them as proxy-injected secrets and hands the
	// sandbox a placeholder; "env" passes the values as plain variables.
	Secrets string `toml:"secrets,omitempty"`
	// Mounts are extra host paths bind-mounted into the sandbox at the
	// same absolute path, "~"-prefixed or absolute, ":ro" for read-only.
	Mounts []string `toml:"mounts,omitempty"`
	// AllowNet lists hosts (domains, wildcards, CIDRs) allowed for this
	// sandbox on top of the active sandbox policy.
	AllowNet []string `toml:"allow_net,omitempty"`
	// ClaudeVersion pins the Claude Code version installed inside the
	// sandbox at creation ("2.1.263"); empty runs the sandbox image's own.
	ClaudeVersion string `toml:"claude_version,omitempty"`
	// Workdir is the default working directory mounted and entered,
	// "~"-prefixed or absolute; the invocation directory when empty.
	Workdir string `toml:"workdir,omitempty"`
}

// Read parses the .playbook file inside dir. Returns (nil, nil) if the file
// does not exist. Returns an error if the file exists but is invalid TOML or
// has structural problems.
func Read(dir string) (*Manifest, error) {
	path := filepath.Join(dir, FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var m Manifest
	if err := tomlfile.Decode(path, data, &m); err != nil {
		if errors.As(err, new(*tomlfile.UnknownKeyError)) {
			return nil, err
		}
		return nil, fmt.Errorf("invalid .playbook at %s: %s", path, SanitizeTOMLError(err))
	}
	if err := m.validate(path); err != nil {
		return nil, err
	}
	return &m, nil
}

// Nearest returns the manifest governing dir: the one in dir itself, or the
// closest ancestor's. A config directory that is a manifest `subdir` has no
// manifest of its own; its install root's applies.
//
// An unreadable or invalid manifest on the way up does not stop the walk --
// the closest VALID manifest still governs, so a stray broken file in a
// subdir cannot silently switch off an install root's isolated_login. The
// first such error is returned alongside whatever was found, so callers can
// report it. Returns (nil, nil) when no ancestor has a manifest.
func Nearest(dir string) (*Manifest, error) {
	m, _, err := NearestPath(dir)
	return m, err
}

// NearestPath is Nearest, also returning the directory whose manifest governs
// ("" when none does), for callers that must say WHICH manifest a launch
// consults rather than only what it says.
func NearestPath(dir string) (*Manifest, string, error) {
	var firstErr error
	for {
		m, err := Read(dir)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if m != nil {
			return m, dir, firstErr
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, "", firstErr
		}
		dir = parent
	}
}

// Exists reports whether dir contains a .playbook file.
func Exists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, FileName))
	return err == nil
}

// validate checks structural invariants. Path existence is checked by callers
// that have access to the playbook directory.
func (m *Manifest) validate(path string) error {
	// Record names become path segments (skills/<name>) and command
	// arguments: a name from a hand-written or source-shipped manifest is
	// held to the same rule the grammar applies, never trusted.
	for name, r := range m.Skills {
		if ValidateSetName(name) != nil {
			return fmt.Errorf("invalid %s at %s: [skills] entry %q is not a valid skill name", FileName, path, name)
		}
		if r != nil && r.Mode != "link" && r.Mode != "copy" {
			return fmt.Errorf("invalid %s at %s: [skills.%s] mode must be \"link\" or \"copy\"", FileName, path, name)
		}
	}
	for name := range m.MCP {
		if ValidateSetName(name) != nil {
			return fmt.Errorf("invalid %s at %s: [mcp] entry %q is not a valid MCP server name", FileName, path, name)
		}
	}
	if m.Sandbox != nil {
		if err := m.Sandbox.validate(); err != nil {
			return fmt.Errorf("invalid %s at %s: %w", FileName, path, err)
		}
	}
	if m.Source != nil {
		if err := validateRelativePath(path, "source.subdir", m.Source.Subdir); err != nil {
			return err
		}
	}
	if m.Update != nil {
		for _, rel := range m.Update.Preserve {
			if err := validateRelativePath(path, "update.preserve", rel); err != nil {
				return err
			}
		}
		if err := validateRelativePath(path, "update.migrate", m.Update.Migrate); err != nil {
			return err
		}
	}
	if m.Env != nil {
		for _, name := range m.Env.Sets {
			if err := ValidateSetName(name); err != nil {
				return fmt.Errorf("invalid .playbook at %s: env.sets: %w", path, err)
			}
		}
		for key, value := range m.Env.Set {
			if err := ValidateEnvKey(key); err != nil {
				return fmt.Errorf("invalid .playbook at %s: env.set: %w", path, err)
			}
			if err := ValidateEnvValue(key, value); err != nil {
				return fmt.Errorf("invalid .playbook at %s: env.set: %w", path, err)
			}
		}
		for _, key := range m.Env.Block {
			if err := ValidateEnvKey(key); err != nil {
				return fmt.Errorf("invalid .playbook at %s: env.block: %w", path, err)
			}
			if _, both := m.Env.Set[key]; both {
				return fmt.Errorf("invalid .playbook at %s: env: %s is both set and blocked", path, key)
			}
		}
		if err := ValidateRefs(m.Env.Refs, m.Env.Set, m.Env.Block); err != nil {
			return fmt.Errorf("invalid .playbook at %s: env.refs: %w", path, err)
		}
	}
	return nil
}

// ValidateRelativePath reports whether value is a relative path that stays
// below a playbook root. manifestPath appears in the error text only.
func ValidateRelativePath(manifestPath, field, value string) error {
	return validateRelativePath(manifestPath, field, value)
}

func validateRelativePath(manifestPath, field, value string) error {
	if value == "" {
		return nil
	}
	cleaned := path.Clean(filepath.ToSlash(value))
	if filepath.IsAbs(value) || strings.HasPrefix(cleaned, "/") || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("invalid .playbook at %s: %s must be a relative path below the playbook root", manifestPath, field)
	}
	return nil
}

// ResolveSubdir resolves a manifest or source subdirectory and verifies that
// symlinks do not escape the supplied root.
func ResolveSubdir(root, field, value string) (string, error) {
	if value == "" {
		return root, nil
	}
	candidate, err := ResolvePath(root, field, value)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s %q is not a directory below %s", field, value, root)
	}
	return candidate, nil
}

// ResolvePath resolves a relative path and verifies that symlinks keep it
// physically below root. The returned path retains root's lexical form.
func ResolvePath(root, field, value string) (string, error) {
	if err := validateRelativePath(filepath.Join(root, FileName), field, value); err != nil {
		return "", err
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(root, filepath.FromSlash(value))
	candidateResolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("%s %q not found below %s: %w", field, value, root, err)
	}
	rel, err := filepath.Rel(rootResolved, candidateResolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s %q resolves outside %s", field, value, root)
	}
	return candidate, nil
}

// Write serializes a manifest to the .playbook file inside dir.
func Write(dir string, m *Manifest) error {
	path := filepath.Join(dir, FileName)
	if err := m.validate(path); err != nil {
		return err
	}
	var b strings.Builder
	if m.Version != "" {
		fmt.Fprintf(&b, "version = %s\n", QuoteTOML(m.Version))
	}
	if m.Name != "" {
		fmt.Fprintf(&b, "name = %s\n", QuoteTOML(m.Name))
	}
	if m.Launcher != "" {
		fmt.Fprintf(&b, "launcher = %s\n", QuoteTOML(m.Launcher))
	}
	if m.Description != "" {
		fmt.Fprintf(&b, "description = %s\n", QuoteTOML(m.Description))
	}
	if m.Homepage != "" {
		fmt.Fprintf(&b, "homepage = %s\n", QuoteTOML(m.Homepage))
	}
	if m.Author != "" {
		fmt.Fprintf(&b, "author = %s\n", QuoteTOML(m.Author))
	}
	if m.IsolatedLogin {
		fmt.Fprintf(&b, "isolated_login = true\n")
	}
	if m.Source != nil {
		b.WriteString("\n[source]\n")
		if m.Source.Repository != "" {
			fmt.Fprintf(&b, "repository = %s\n", QuoteTOML(m.Source.Repository))
		}
		if m.Source.Branch != "" {
			fmt.Fprintf(&b, "branch = %s\n", QuoteTOML(m.Source.Branch))
		}
		if m.Source.Subdir != "" {
			fmt.Fprintf(&b, "subdir = %s\n", QuoteTOML(m.Source.Subdir))
		}
	}
	if m.Update != nil && (len(m.Update.Preserve) > 0 || m.Update.Migrate != "") {
		b.WriteString("\n[update]\n")
		if len(m.Update.Preserve) > 0 {
			b.WriteString("preserve = [")
			for i, rel := range m.Update.Preserve {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(QuoteTOML(rel))
			}
			b.WriteString("]\n")
		}
		if m.Update.Migrate != "" {
			fmt.Fprintf(&b, "migrate = %s\n", QuoteTOML(m.Update.Migrate))
		}
	}
	if !m.Env.Empty() {
		// [env] must precede [env.set] in TOML; both are emitted in sorted
		// order so a rewrite never reorders a hand-edited file arbitrarily.
		b.WriteString("\n[env]\n")
		if len(m.Env.Sets) > 0 {
			b.WriteString("sets = [")
			for i, name := range m.Env.Sets {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(QuoteTOML(name))
			}
			b.WriteString("]\n")
		}
		if len(m.Env.Block) > 0 {
			block := append([]string(nil), m.Env.Block...)
			sort.Strings(block)
			b.WriteString("block = [")
			for i, key := range block {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(QuoteTOML(key))
			}
			b.WriteString("]\n")
		}
		if len(m.Env.Set) > 0 {
			keys := make([]string, 0, len(m.Env.Set))
			for key := range m.Env.Set {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			b.WriteString("\n[env.set]\n")
			for _, key := range keys {
				fmt.Fprintf(&b, "%s = %s\n", key, QuoteTOML(m.Env.Set[key]))
			}
		}
		WriteRefsTable(&b, "[env.refs]", m.Env.Refs)
	}
	if !m.Sandbox.Empty() {
		b.WriteString("\n[sandbox]\n")
		if m.Sandbox.Always {
			b.WriteString("always = true\n")
		}
		if m.Sandbox.Backend != "" {
			fmt.Fprintf(&b, "backend = %s\n", QuoteTOML(m.Sandbox.Backend))
		}
		if m.Sandbox.Host != "" {
			fmt.Fprintf(&b, "host = %s\n", QuoteTOML(m.Sandbox.Host))
		}
		if m.Sandbox.ShareSkills {
			b.WriteString("share_skills = true\n")
		}
		if m.Sandbox.Secrets != "" {
			fmt.Fprintf(&b, "secrets = %s\n", QuoteTOML(m.Sandbox.Secrets))
		}
		if m.Sandbox.Workdir != "" {
			fmt.Fprintf(&b, "workdir = %s\n", QuoteTOML(m.Sandbox.Workdir))
		}
		writeTOMLList(&b, "mounts", m.Sandbox.Mounts)
		writeTOMLList(&b, "allow_net", m.Sandbox.AllowNet)
		if m.Sandbox.ClaudeVersion != "" {
			fmt.Fprintf(&b, "claude_version = %s\n", QuoteTOML(m.Sandbox.ClaudeVersion))
		}
	}
	// [mcp.<server>]: the variables cpb derived for each MCP server.
	if len(m.MCP) > 0 {
		names := make([]string, 0, len(m.MCP))
		for n, r := range m.MCP {
			if r != nil && len(r.Vars) > 0 {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(&b, "\n[mcp.%s]\n", QuoteTOML(n))
			writeTOMLList(&b, "vars", m.MCP[n].Vars)
		}
	}
	// [skills.<name>]: the skills cpb added.
	if len(m.Skills) > 0 {
		names := make([]string, 0, len(m.Skills))
		for n, r := range m.Skills {
			if r != nil {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		for _, n := range names {
			r := m.Skills[n]
			fmt.Fprintf(&b, "\n[skills.%s]\n", QuoteTOML(n))
			fmt.Fprintf(&b, "source = %s\n", QuoteTOML(r.Source))
			if r.Branch != "" {
				fmt.Fprintf(&b, "branch = %s\n", QuoteTOML(r.Branch))
			}
			if r.Subdir != "" {
				fmt.Fprintf(&b, "subdir = %s\n", QuoteTOML(r.Subdir))
			}
			fmt.Fprintf(&b, "mode = %s\n", QuoteTOML(r.Mode))
		}
	}
	if m.Play != nil {
		b.WriteString("\n[play]\n")
		fmt.Fprintf(&b, "ref = %s\n", QuoteTOML(m.Play.Ref))
		if m.Play.URL != "" {
			fmt.Fprintf(&b, "url = %s\n", QuoteTOML(m.Play.URL))
		}
		fmt.Fprintf(&b, "sha256 = %s\n", QuoteTOML(m.Play.SHA256))
		fmt.Fprintf(&b, "played_at = %s\n", QuoteTOML(m.Play.PlayedAt))
	}
	// Values under [env.set] can be bearer tokens or API keys, so a manifest
	// carrying any is written private, like an env set. Existing files
	// are only ever tightened, never loosened.
	// An existing file keeps its mode exactly (as an in-place rewrite would
	// have), and is tightened to owner-only when values are present. It is
	// never loosened, whatever it was.
	perm := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}
	if m.Env != nil && len(m.Env.Set) > 0 {
		perm &= 0o600
	}
	return WritePrivate(path, []byte(b.String()), perm)
}

// writeTOMLList emits `key = ["a", "b"]` when items is non-empty.
func writeTOMLList(b *strings.Builder, key string, items []string) {
	if len(items) == 0 {
		return
	}
	b.WriteString(key + " = [")
	for i, it := range items {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(QuoteTOML(it))
	}
	b.WriteString("]\n")
}

// WritePrivate replaces path with data through a temporary file created
// 0600 in the same directory and renamed into place. The content is never
// readable at a looser mode than perm, not even for the duration of the
// write: truncating an existing 0644 file in place and chmodding afterwards
// would expose a freshly written secret to any local reader in between.
func WritePrivate(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

var claudeVersionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// SandboxBackends lists the sandbox implementations cpb drives.
var SandboxBackends = []string{"sbx"}

// KnownSandboxBackend reports whether name is one of SandboxBackends.
func KnownSandboxBackend(name string) bool {
	for _, b := range SandboxBackends {
		if b == name {
			return true
		}
	}
	return false
}

// ValidSandboxHost reports whether host is a plain ssh destination: no
// whitespace, no slash, and no leading dash (an option to ssh).
func ValidSandboxHost(host string) bool {
	return host != "" && !strings.ContainsAny(host, " \t\n\r/") && !strings.HasPrefix(host, "-")
}

// Empty reports whether the block carries nothing.
func (s *Sandbox) Empty() bool {
	return s == nil || (!s.Always && s.Backend == "" && s.Host == "" && !s.ShareSkills && s.Secrets == "" && len(s.Mounts) == 0 && len(s.AllowNet) == 0 && s.ClaudeVersion == "" && s.Workdir == "")
}

// validate checks the [sandbox] block: paths are absolute or "~"-prefixed
// (a relative mount would mean a different directory on every invocation),
// an optional ":ro" suffix is the only mount option, network entries and
// the version pin have the shapes sbx and the installer accept.
func (s *Sandbox) validate() error {
	if s.Backend != "" && !KnownSandboxBackend(s.Backend) {
		return fmt.Errorf("sandbox.backend %q is not a known backend (%s)", s.Backend, strings.Join(SandboxBackends, ", "))
	}
	if s.Host != "" && !ValidSandboxHost(s.Host) {
		return fmt.Errorf("sandbox.host %q must be an ssh destination such as user@host", s.Host)
	}
	if s.Secrets != "" && s.Secrets != "proxy" && s.Secrets != "env" {
		return fmt.Errorf("sandbox.secrets %q must be \"proxy\" or \"env\"", s.Secrets)
	}
	for _, m := range s.Mounts {
		p := strings.TrimSuffix(m, ":ro")
		if p == "" || !(strings.HasPrefix(p, "/") || p == "~" || strings.HasPrefix(p, "~/")) || strings.ContainsAny(p, ":\t\n\r") {
			return fmt.Errorf("sandbox.mounts entry %q must be an absolute or ~-prefixed path, optionally suffixed :ro", m)
		}
	}
	for _, h := range s.AllowNet {
		if h == "" || strings.ContainsAny(h, " \t\n\r") {
			return fmt.Errorf("sandbox.allow_net entry %q must be a host, wildcard or CIDR without whitespace", h)
		}
	}
	if s.ClaudeVersion != "" && !claudeVersionPattern.MatchString(s.ClaudeVersion) {
		return fmt.Errorf("sandbox.claude_version %q must look like 2.1.263", s.ClaudeVersion)
	}
	if w := s.Workdir; w != "" && !(strings.HasPrefix(w, "/") || w == "~" || strings.HasPrefix(w, "~/")) {
		return fmt.Errorf("sandbox.workdir %q must be an absolute or ~-prefixed path", w)
	}
	return nil
}

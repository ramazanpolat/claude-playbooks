package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// Secret references are resolved by a helper the pilot configures, git
// credential.helper style (SPEC.md, "Secrets"). cpb owns the
// interface and never names or discovers a helper:
//
//	<helper> --check KEY=REF              when a reference is written
//	<helper> K1=REF1 … -- claude <args>   at launch; the helper execs claude
//
// cpb never fetches a value: there is no call that returns one. References
// travel on argv, values never do.

var errNoHelper = errors.New("no secret helper configured (ALTER DEFAULTS SET secret_helper = '<command>', or CPB_SECRET_HELPER)")

// lookHelper finds a helper named without a path. A variable so tests stay
// hermetic, as lookPilot was.
var lookHelper = exec.LookPath

// resolveHelper returns the helper in effect and the executable to run.
func resolveHelper() (*envset.Helper, string, error) {
	h, err := envset.SecretHelper(envset.Dir(config.ResolvePlaybooksDir()))
	if err != nil {
		return nil, "", err
	}
	if h == nil {
		return nil, "", errNoHelper
	}
	if filepath.IsAbs(h.Command) {
		return h, h.Command, nil
	}
	path, err := lookHelper(h.Command)
	if err != nil {
		return nil, "", fmt.Errorf("secret helper %q (from %s) is not on PATH", h.Command, h.From)
	}
	return h, path, nil
}

// helperExe is how a secret helper runs: its executable and the arguments
// before its own. The default helper is this binary with secretHelperArg
// (secret_default.go).
type helperExe struct {
	path string
	pre  []string
	def  bool   // cpb's default helper
	name string // for messages: the configured command, or the default's name
}

// command is the helper process for args (its own arguments).
func (x helperExe) command(args ...string) *exec.Cmd {
	return exec.Command(x.path, append(append([]string{}, x.pre...), args...)...)
}

// allCPBRefs reports references that are all cpb:<name>, the only scheme
// the default helper resolves.
func allCPBRefs(refs []string) bool {
	if len(refs) == 0 {
		return false
	}
	for _, r := range refs {
		if !strings.HasPrefix(r, manifest.CPBRefPrefix) {
			return false
		}
	}
	return true
}

// resolveHelperFor is the helper for these references: the configured one,
// else, when every reference is cpb:<name>, cpb's default helper. Any other
// reference with no helper configured is refused as before.
func resolveHelperFor(refs []string) (helperExe, error) {
	h, path, err := resolveHelper()
	if err == nil {
		return helperExe{path: path, name: h.Command}, nil
	}
	if !errors.Is(err, errNoHelper) || !allCPBRefs(refs) {
		return helperExe{}, err
	}
	return defaultHelperExe()
}

// helperState is the secret helper a run will have configured by a given
// point: a playbook file may set the helper and use it in the same run, so
// its references are checked against that helper, not the one configured
// before the file ran. Unset means "whatever is configured now".
type helperState struct {
	set bool
	h   *envset.Helper // nil after RESET secret_helper
}

// after returns the state once c has run. CPB_SECRET_HELPER still wins over
// any setting, as it does at launch.
func (s helperState) after(c grammar.Clause) helperState {
	if v, ok := os.LookupEnv(envset.SecretHelperEnv); ok && v != "" {
		return s
	}
	switch c.Kind {
	case grammar.SetHelper:
		return helperState{set: true, h: &envset.Helper{Command: c.Arg, From: "setting"}}
	case grammar.UnsetHelper:
		return helperState{set: true}
	}
	return s
}

// resolveFor is resolveHelperFor at this point of the run.
func (s helperState) resolveFor(refs []string) (helperExe, error) {
	h, path, err := s.resolve()
	if err == nil {
		return helperExe{path: path, name: h.Command}, nil
	}
	if !errors.Is(err, errNoHelper) || !allCPBRefs(refs) {
		return helperExe{}, err
	}
	return defaultHelperExe()
}

func (s helperState) resolve() (*envset.Helper, string, error) {
	if !s.set {
		return resolveHelper()
	}
	if s.h == nil {
		return nil, "", errNoHelper
	}
	if filepath.IsAbs(s.h.Command) {
		return s.h, s.h.Command, nil
	}
	path, err := lookHelper(s.h.Command)
	if err != nil {
		return nil, "", fmt.Errorf("secret helper %q (set earlier in this run) is not on PATH", s.h.Command)
	}
	return s.h, path, nil
}

// checkRefsWith asks the helper (state s) whether every reference a
// statement writes resolves, before anything is written. The helper reports
// presence only; its own words go to stderr.
func checkRefsWith(s helperState, clauses []grammar.Clause) error {
	var refs []grammar.Var
	for _, c := range clauses {
		if c.Kind == grammar.SetRef {
			refs = append(refs, c.Vars...)
		}
	}
	if len(refs) == 0 {
		return nil
	}
	names := make([]string, 0, len(refs))
	for _, v := range refs {
		if err := manifest.ValidateCPBRef(v.Ref); err != nil {
			return fmt.Errorf("%s: %w", v.Key, err)
		}
		names = append(names, v.Ref)
	}
	x, err := s.resolveFor(names)
	if err != nil {
		return err
	}
	// The helper never sees a value the shell already exports under a key
	// it is asked about: the check is about the reference alone.
	keys := make([]string, len(refs))
	for i, v := range refs {
		keys[i] = v.Key
	}
	env := withoutKeys(os.Environ(), keys)
	for _, v := range refs {
		if err := manifest.ValidateRefKey(v.Key); err != nil {
			return err
		}
		// A reference play is about to ask the person to store
		// (play_store.go) is not stored yet, by design: the plan shows it
		// as such, and the real run checks it again after the store step.
		if refsPendingStore[v.Ref] {
			continue
		}
		if err := runHelper(x, nil, os.Stderr, os.Stderr, env, "--check", v.Key+"="+v.Ref); err != nil {
			return fmt.Errorf("secret helper %s could not resolve the reference for %s (%v); nothing was written", x.name, v.Key, err)
		}
	}
	return nil
}

// launchPlan is how a launch execs: claude directly, or through the helper
// when the launch's layers hold references.
type launchPlan struct {
	helper    string            // executable; "" launches claude directly
	helperPre []string          // the helper's arguments before its own (the default helper's)
	def       bool              // cpb's default helper: it gets cpb's own store environment
	refs      map[string]string // key -> reference
	// headers are the account's header names (account.Rendered.Headers):
	// the helper then execs cpb's header step, self, before claude
	// (account_headers.go).
	headers []string
	self    string
}

// planLaunch decides, before anything is mutated, how a launch with the
// effective block eff runs, and refuses one that cannot: references with
// no helper configured.
func planLaunch(eff *manifest.Env) (*launchPlan, error) {
	if eff == nil || len(eff.Refs) == 0 {
		return &launchPlan{}, nil
	}
	x, err := resolveHelperFor(refValues(eff.Refs))
	if err != nil {
		return nil, fmt.Errorf("this launch uses secret references (%s): %w", strings.Join(sortedKeys(eff.Refs), ", "), err)
	}
	return &launchPlan{helper: x.path, helperPre: x.pre, def: x.def, refs: eff.Refs}, nil
}

// withHeaders makes a launch through the helper pass the account's headers.
func (lp *launchPlan) withHeaders(names []string) error {
	if len(names) == 0 {
		return nil
	}
	if lp.helper == "" {
		return errors.New("the account's headers are secret references, and this launch has no secret helper")
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("the account's headers need cpb's own binary for the launch, and it cannot be found: %w", err)
	}
	lp.headers, lp.self = names, self
	return nil
}

// command builds the process to run. Through a helper, the keys it will
// set are removed from the environment first, so a stale value from the
// shell can never stand in for one the helper failed to supply.
func (lp *launchPlan) command(claudePath string, claudeArgs, env []string) *exec.Cmd {
	// A header variable from the shell never reaches claude, nor stands in
	// for one the helper sets: all of them go, on every launch.
	env = withoutPrefix(env, account.HeaderVarPrefix)
	if lp.helper == "" {
		c := exec.Command(claudePath, claudeArgs...)
		c.Env = env
		return c
	}
	keys := sortedKeys(lp.refs)
	argv := make([]string, 0, len(lp.helperPre)+len(keys)+2+len(claudeArgs))
	argv = append(argv, lp.helperPre...)
	for _, k := range keys {
		argv = append(argv, k+"="+lp.refs[k])
	}
	if len(lp.headers) > 0 {
		argv = append(append(argv, "--"), headerStepArgv(lp.self, lp.headers, claudePath, claudeArgs)...)
	} else {
		argv = append(append(argv, "--", claudePath), claudeArgs...)
	}
	c := exec.Command(lp.helper, argv...)
	c.Env = withoutKeys(env, keys)
	if lp.def {
		// The launch environment is the command's; the store is cpb's own,
		// whatever HOME, XDG_DATA_HOME or bus the playbook sets.
		c.Env = defaultHelperEnv(c.Env)
	}
	return c
}

// refuseRefsInSandbox: a sandboxed launch cannot exec through a helper on
// this machine yet; refusing beats a launch without its secrets.
func refuseRefsInSandbox(eff *manifest.Env, label string, headers []string) error {
	if eff != nil && len(eff.Refs) > 0 {
		var what []string
		for _, k := range sortedKeys(eff.Refs) {
			if !strings.HasPrefix(k, account.HeaderVarPrefix) {
				what = append(what, k)
			}
		}
		if len(headers) > 0 {
			what = append(what, "the account's headers ("+strings.Join(headers, ", ")+")")
		}
		return fmt.Errorf("%s uses secret references (%s), and a sandboxed launch cannot resolve them yet", label, strings.Join(what, ", "))
	}
	return nil
}

// refValues is a key->reference map's references, in key order.
func refValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, m[k])
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func withoutKeys(env, keys []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		drop := false
		for _, k := range keys {
			if strings.HasPrefix(kv, k+"=") {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

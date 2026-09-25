package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// Secret references are resolved by a helper the pilot configures, git
// credential.helper style (docs/cli-grammar.md, "Secrets"). cpb owns the
// interface and never names or discovers a helper:
//
//	<helper> --check KEY=REF              when a reference is written
//	<helper> K1=REF1 … -- claude <args>   at launch; the helper execs claude
//
// cpb never fetches a value: there is no call that returns one. References
// travel on argv, values never do.

var errNoHelper = errors.New("no secret helper configured (ALTER DEFAULTS SET SECRET HELPER '<command>', or CPB_SECRET_HELPER)")

// lookHelper finds a helper named without a path. A variable so tests stay
// hermetic, as lookPilot was.
var lookHelper = exec.LookPath

// resolveHelper returns the helper in effect and the executable to run.
func resolveHelper() (*envprofile.Helper, string, error) {
	h, err := envprofile.SecretHelper(envprofile.Dir(config.ResolvePlaybooksDir()))
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

// checkRefs asks the helper whether every reference a statement writes
// resolves, before anything is written. The helper reports presence only;
// its own words go to stderr.
func checkRefs(clauses []grammar.Clause) error {
	var refs []grammar.Var
	for _, c := range clauses {
		if c.Kind == grammar.SetRef {
			refs = append(refs, c.Vars...)
		}
	}
	if len(refs) == 0 {
		return nil
	}
	h, path, err := resolveHelper()
	if err != nil {
		return err
	}
	for _, v := range refs {
		if err := manifest.ValidateRefKey(v.Key); err != nil {
			return err
		}
		c := exec.Command(path, "--check", v.Key+"="+v.Ref)
		c.Stdout, c.Stderr = os.Stderr, os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("secret helper %s could not resolve the reference for %s (%v); nothing was written", h.Command, v.Key, err)
		}
	}
	return nil
}

// launchPlan is how a launch execs: claude directly, or through the helper
// when the launch's layers hold references.
type launchPlan struct {
	helper string            // executable; "" launches claude directly
	refs   map[string]string // key -> reference
}

// planLaunch decides, before anything is mutated, how a launch with the
// effective block eff runs, and refuses one that cannot: references with
// no helper configured.
func planLaunch(eff *manifest.Env) (*launchPlan, error) {
	if eff == nil || len(eff.Refs) == 0 {
		return &launchPlan{}, nil
	}
	_, path, err := resolveHelper()
	if err != nil {
		return nil, fmt.Errorf("this launch uses secret references (%s): %w", strings.Join(sortedKeys(eff.Refs), ", "), err)
	}
	return &launchPlan{helper: path, refs: eff.Refs}, nil
}

// command builds the process to run. Through a helper, the keys it will
// set are removed from the environment first, so a stale value from the
// shell can never stand in for one the helper failed to supply.
func (lp *launchPlan) command(claudePath string, claudeArgs, env []string) *exec.Cmd {
	if lp.helper == "" {
		c := exec.Command(claudePath, claudeArgs...)
		c.Env = env
		return c
	}
	keys := sortedKeys(lp.refs)
	argv := make([]string, 0, len(keys)+2+len(claudeArgs))
	for _, k := range keys {
		argv = append(argv, k+"="+lp.refs[k])
	}
	argv = append(append(argv, "--", claudePath), claudeArgs...)
	c := exec.Command(lp.helper, argv...)
	c.Env = withoutKeys(env, keys)
	return c
}

// refuseRefsInSandbox: a sandboxed launch cannot exec through a helper on
// this machine yet; refusing beats a launch without its secrets.
func refuseRefsInSandbox(eff *manifest.Env, label string) error {
	if eff != nil && len(eff.Refs) > 0 {
		return fmt.Errorf("%s uses secret references (%s), and a sandboxed launch cannot resolve them yet", label, strings.Join(sortedKeys(eff.Refs), ", "))
	}
	return nil
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

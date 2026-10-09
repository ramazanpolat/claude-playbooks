package cmd

import (
	"fmt"
	"slices"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// isolated reports whether a playbook's login is isolated (isolated_login),
// as the run sees it.
func (r *stmtRun) isolated(name string, m *manifest.Manifest) bool {
	if r.dry != nil {
		if v, ok := r.dry.isolated[name]; ok {
			return v
		}
	}
	return m != nil && m.IsolatedLogin
}

// sandboxed reports whether a playbook always runs in a sandbox, as the run
// sees it.
func (r *stmtRun) sandboxed(name string, m *manifest.Manifest) bool {
	if r.dry != nil {
		if v, ok := r.dry.sandboxed[name]; ok {
			return v
		}
	}
	return m != nil && m.Sandbox != nil && m.Sandbox.Always
}

// planIsolatedLogin decides the login setting (MODIFY SETTING login, and
// RESET SETTING login, which is 'shared'). 'shared' is refused where
// sharing would be wrong: a sandboxed playbook never shares a login,
// and a playbook holding a login of its own would have it copied over the
// machine's login in ~/.claude by the next shared launch (LinkCredentials
// keeps the newer store), switching every shared playbook to that account.
func (r *stmtRun) planIsolatedLogin(name string, m *manifest.Manifest, cfg string, clauses []grammar.Clause) (bool, []string, error) {
	before := r.isolated(name, m)
	after := before
	switch v, _ := grammar.SettingValue(clauses, "login"); v {
	case "isolated":
		after = true
	case "shared":
		if r.sandboxed(name, m) {
			return before, nil, fmt.Errorf("login = 'shared': PLAYBOOK %s always runs in a sandbox, which shares no login with the machine", name)
		}
		if before && cfg != "" && auth.OwnLoginGrant(cfg) {
			return before, nil, fmt.Errorf("login = 'shared': PLAYBOOK %s has a login of its own (%s): a shared launch would set it aside (another account's) or copy it over the machine's login in ~/.claude (the same account's); run /logout in it first", name, auth.CredentialsFileName)
		}
		after = false
	}
	var lines []string
	if after != before {
		if after {
			lines = append(lines, "login     isolated: shares nothing with ~/.claude; /login once in it")
		} else {
			lines = append(lines, "login     shared with ~/.claude again from the next launch")
		}
	}
	return after, lines, nil
}

// planSandbox applies SET / UNSET SANDBOX to a copy of the [sandbox] block.
// Bare SET SANDBOX (or always=true) is always = true, and isolate reports
// that the login must be isolated, as CREATE … SANDBOX does; bare UNSET
// SANDBOX is always = false and leaves the login as it is (MODIFY SETTING
// login = 'shared' shares it again). The keyed forms change only the keys they name.
func planSandbox(cur *manifest.Sandbox, clauses []grammar.Clause) (sb *manifest.Sandbox, isolate bool, lines []string, err error) {
	sb = cloneSandbox(cur)
	if sb == nil {
		sb = &manifest.Sandbox{}
	}
	for _, c := range clauses {
		switch c.Kind {
		case grammar.SetSandbox:
			sb.Always, isolate = true, true
			lines = append(lines, "sandbox   always: every launch runs in a sandbox")
		case grammar.UnsetSandbox:
			sb.Always = false
			lines = append(lines, "sandbox   not always: a launch runs on this machine unless it asks for --sandbox; the login stays isolated")
		case grammar.SetSandboxKeys:
			for _, v := range c.Settings {
				if err := sb.SetKey(v.Key, v.Value); err != nil {
					return nil, false, nil, err
				}
				if v.Key == "always" && v.Value == "true" {
					isolate = true
				}
				lines = append(lines, "sandbox   "+v.Key+"="+v.Value)
			}
		case grammar.UnsetSandboxKeys:
			for _, v := range c.Settings {
				if err := sb.UnsetKey(v.Key); err != nil {
					return nil, false, nil, err
				}
				lines = append(lines, "sandbox   "+v.Key+" unset")
			}
		}
	}
	if sb.Empty() {
		sb = nil
	}
	return sb, isolate, lines, nil
}

// cloneSandbox is a deep copy of a [sandbox] block.
func cloneSandbox(s *manifest.Sandbox) *manifest.Sandbox {
	if s == nil {
		return nil
	}
	c := *s
	c.Mounts = slices.Clone(s.Mounts)
	c.AllowNet = slices.Clone(s.AllowNet)
	return &c
}

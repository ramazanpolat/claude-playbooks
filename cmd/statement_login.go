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

// planIsolatedLogin decides the login property (SET login, and DELETE
// login, which is 'shared'). 'shared' is refused where
// sharing would be wrong: a sandboxed playbook never shares a login,
// and a playbook holding a login of its own would have it copied over the
// machine's login in ~/.claude by the next shared launch (LinkCredentials
// keeps the newer store), switching every shared playbook to that account.
func (r *stmtRun) planIsolatedLogin(name string, m *manifest.Manifest, cfg string, clauses []grammar.Clause) (bool, []string, error) {
	before := r.isolated(name, m)
	after := before
	switch v, _ := grammar.PropertyValue(clauses, "login"); v {
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

// planSandbox applies SET and DELETE sandbox.<key> to a copy of the
// [sandbox] block. Each changes only the keys it names; always is one of
// them. alwaysOn reports a statement that sets sandbox.always = true, which
// needs an isolated login: the caller checks it, since a sandbox never
// isolates the login on its own.
func planSandbox(cur *manifest.Sandbox, clauses []grammar.Clause) (sb *manifest.Sandbox, alwaysOn bool, lines []string, err error) {
	sb = cloneSandbox(cur)
	if sb == nil {
		sb = &manifest.Sandbox{}
	}
	for _, c := range clauses {
		switch c.Kind {
		case grammar.SetSandboxKeys:
			for _, v := range c.Settings {
				if err := sb.SetKey(v.Key, v.Value); err != nil {
					return nil, false, nil, err
				}
				if v.Key == "always" && v.Value == "true" {
					alwaysOn = true
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
	return sb, alwaysOn, lines, nil
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

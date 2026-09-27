package cmd

import (
	"fmt"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// isolated reports whether a playbook's login is isolated (isolate_auth),
// as the run sees it.
func (r *stmtRun) isolated(name string, m *manifest.Manifest) bool {
	if r.dry != nil {
		if v, ok := r.dry.isolated[name]; ok {
			return v
		}
	}
	return m != nil && m.IsolateAuth
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

// planIsolatedLogin decides SET / UNSET ISOLATED LOGIN. UNSET is refused
// where sharing would be wrong: a sandboxed playbook never shares a login,
// and a playbook holding a login of its own would have it copied over the
// machine's login in ~/.claude by the next shared launch (LinkCredentials
// keeps the newer store), switching every shared playbook to that account.
func (r *stmtRun) planIsolatedLogin(name string, m *manifest.Manifest, cfg string, clauses []grammar.Clause) (bool, []string, error) {
	before := r.isolated(name, m)
	after := before
	for _, c := range clauses {
		switch c.Kind {
		case grammar.SetIsolatedLogin:
			after = true
		case grammar.UnsetIsolatedLogin:
			if r.sandboxed(name, m) {
				return before, nil, fmt.Errorf("UNSET ISOLATED LOGIN: PLAYBOOK %s always runs in a sandbox, which shares no login with the machine", name)
			}
			if before && cfg != "" && auth.OwnLoginGrant(cfg) {
				return before, nil, fmt.Errorf("UNSET ISOLATED LOGIN: PLAYBOOK %s has a login of its own (%s): a shared launch would set it aside (another account's) or copy it over the machine's login in ~/.claude (the same account's); run /logout in it first", name, auth.CredentialsFileName)
			}
			after = false
		}
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

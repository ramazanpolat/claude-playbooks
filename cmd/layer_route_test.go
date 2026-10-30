package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// Derived routes are named in a colon form no stored account can take; a
// LINK target's manifest never routes; header names are checked; headers by
// reference are refused; a sandboxed launch on a route with headers is
// refused.
func TestLayerRouteRules(t *testing.T) {
	root := sandboxDefaultRoot(t)
	clearRouteEnv(t)
	route := map[string]string{"ANTHROPIC_BASE_URL": "http://localhost:4000/v1", "ANTHROPIC_AUTH_TOKEN": "k-1"}
	writePlaybook(t, root, "blk", &manifest.Manifest{IsolatedLogin: true, Env: &manifest.Env{Set: route}})
	if r := routeOf(t, "blk"); r.Refused != nil || r.Account.Name != "block:blk" {
		t.Fatalf("a registered playbook's own block: %+v", r)
	}

	writePlaybook(t, root, "plain", &manifest.Manifest{IsolatedLogin: true})
	pb := filepath.Join(root, "plain")
	flags := []*manifest.Env{{Set: route}}
	acct, err := planAccount(pb, "plain", flags, t.TempDir(), nil)
	if err != nil || acct.sel.Name != "flags:localhost:4000" {
		t.Fatalf("a route in the launch's flags: %v %+v", err, acct)
	}

	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "CLAUDE.md"), []byte("# t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(target, &manifest.Manifest{Name: "target", IsolatedLogin: true, Env: &manifest.Env{Set: route}}); err != nil {
		t.Fatal(err)
	}
	captureStderr(t, func() { _, err = quotedStmt(t, "CREATE PLAYBOOK lk SET launcher = '' LINK '"+target+"'") })
	if err != nil {
		t.Fatal(err)
	}
	if r := routeOf(t, "lk"); r.Refused == nil || !strings.Contains(*r.Refused, "not a playbook cpb keeps in its registry") || strings.Contains(*r.Refused, "k-1") {
		t.Fatalf("a LINK target's route: %+v", r)
	}

	bad := map[string]string{"ANTHROPIC_BASE_URL": "http://localhost:4000/v1", "ANTHROPIC_AUTH_TOKEN": "k-1", "ANTHROPIC_CUSTOM_HEADERS": "Authorization: Bearer x"}
	if _, err := planAccount(pb, "plain", []*manifest.Env{{Set: bad}}, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "the account's own") {
		t.Fatalf("a credential header in a layer route: %v", err)
	}
	byRef := []*manifest.Env{{Set: route, Refs: map[string]string{"ANTHROPIC_CUSTOM_HEADERS": "keychain:h"}}}
	if _, err := planAccount(pb, "plain", byRef, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_CUSTOM_HEADERS by reference") {
		t.Fatalf("headers by reference in a layer route: %v", err)
	}
	withHeaders := map[string]string{"ANTHROPIC_BASE_URL": "http://localhost:4000/v1", "ANTHROPIC_AUTH_TOKEN": "k-1", "ANTHROPIC_CUSTOM_HEADERS": "X-Team: blue"}
	if _, err := planSandboxAccount(pb, "plain", []*manifest.Env{{Set: withHeaders}}, nil, sandboxOpts{workdir: t.TempDir()}, nil); err == nil || !strings.Contains(err.Error(), "sandboxed launch would carry them in as a plain variable") {
		t.Fatalf("a sandboxed launch on a route with headers: %v", err)
	}
}

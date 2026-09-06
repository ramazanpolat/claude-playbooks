package cmd

import (
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

func TestProfileLifecycleAndAttachment(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "router")
	dir := envprofile.Dir(config.ResolvePlaybooksDir())

	// set creates the profile; describe/unset/clear edit it.
	if err := runEnvProfile(nil, []string{"glm", "set", "ANTHROPIC_BASE_URL=http://proxy/v1", "MODEL=glm"}); err != nil {
		t.Fatal(err)
	}
	if err := runEnvProfile(nil, []string{"glm", "describe", "GLM via", "router"}); err != nil {
		t.Fatal(err)
	}
	if err := runEnvProfile(nil, []string{"glm", "unset", "CLAUDE_CODE_OAUTH_TOKEN", "MODEL"}); err != nil {
		t.Fatal(err)
	}
	if err := runEnvProfile(nil, []string{"glm", "clear", "MODEL"}); err != nil {
		t.Fatal(err)
	}
	p, err := envprofile.Read(dir, "glm")
	if err != nil || p == nil {
		t.Fatalf("profile: %#v %v", p, err)
	}
	if p.Description != "GLM via router" || p.Set["ANTHROPIC_BASE_URL"] != "http://proxy/v1" ||
		len(p.Set) != 1 || len(p.Unset) != 1 || p.Unset[0] != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Fatalf("profile after edits: %#v", p)
	}

	// use attaches (and refuses an unknown profile); show renders the
	// effective block; delete is refused while attached.
	if err := runEnv(nil, []string{"router", "use", "ghost"}); err == nil {
		t.Fatal("attached a profile that does not exist")
	}
	if err := runEnv(nil, []string{"router", "use", "glm"}); err != nil {
		t.Fatal(err)
	}
	if err := runEnv(nil, []string{"router", "set", "MODEL=own"}); err != nil {
		t.Fatal(err)
	}
	m, _ := manifest.Read(root)
	if !m.Env.Uses("glm") {
		t.Fatalf("manifest after use: %#v", m.Env)
	}
	out := captureStdout(t, func() {
		if err := runEnv(nil, []string{"router"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{
		"  profiles  glm\n",
		"Effective at launch:\n",
		"  set    ANTHROPIC_BASE_URL=http://proxy/v1\n",
		"  set    MODEL=own\n",
		"  unset  CLAUDE_CODE_OAUTH_TOKEN\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q:\n%s", want, out)
		}
	}
	list := captureStdout(t, func() {
		if err := runEnvProfile(nil, nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(list, "glm  GLM via router (1 set, 1 unset; used by router)") {
		t.Fatalf("profile list:\n%s", list)
	}
	if err := runEnvProfile(nil, []string{"glm", "delete"}); err == nil || !strings.Contains(err.Error(), "used by router") {
		t.Fatalf("delete of an attached profile: %v", err)
	}

	// unuse detaches; delete then succeeds; a launch-time reference to the
	// deleted profile is what run refuses, covered in e2e.
	if err := runEnv(nil, []string{"router", "unuse", "glm"}); err != nil {
		t.Fatal(err)
	}
	if err := runEnvProfile(nil, []string{"glm", "delete"}); err != nil {
		t.Fatal(err)
	}
	if p, _ := envprofile.Read(dir, "glm"); p != nil {
		t.Fatal("profile survived delete")
	}
	m, _ = manifest.Read(root)
	if m.Env.Uses("glm") || m.Env.Set["MODEL"] != "own" {
		t.Fatalf("manifest after unuse: %#v", m.Env)
	}
}

func TestProfileRejectsBadInput(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	for _, args := range [][]string{
		{"bad name!", "set", "A=1"},
		{"p", "set", "NOEQUALS"},
		{"p", "set", "CLAUDE_CONFIG_DIR=/x"},
		{"p", "unset"},
		{"p", "frob"},
		{"p", "delete", "extra"},
		{"p", "unset", "A"}, // does not exist yet; only set creates
		{"p", "describe"},
		{"absent"},
	} {
		if err := runEnvProfile(nil, args); err == nil {
			t.Errorf("profile %v succeeded", args)
		}
	}
	if got, _ := envprofile.List(envprofile.Dir(config.ResolvePlaybooksDir())); len(got) != 0 {
		t.Fatalf("a rejected command created a profile: %v", got)
	}
}

func TestProfileDefaultLifecycle(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "router")
	dir := envprofile.Dir(config.ResolvePlaybooksDir())

	if err := runEnvProfile(nil, []string{"base", "default"}); err == nil {
		t.Fatal("made a missing profile the default")
	}
	if err := runEnvProfile(nil, []string{"base", "set", "FROM_DEFAULT=yes"}); err != nil {
		t.Fatal(err)
	}
	if err := runEnvProfile(nil, []string{"base", "default"}); err != nil {
		t.Fatal(err)
	}
	if d, _ := envprofile.Default(dir); d != "base" {
		t.Fatalf("default = %q", d)
	}
	list := captureStdout(t, func() {
		if err := runEnvProfile(nil, nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(list, "registry default") {
		t.Fatalf("list does not mark the default:\n%s", list)
	}
	// env show mentions the default even for a playbook with no block, and
	// the effective view includes it when a block exists.
	show := captureStdout(t, func() {
		if err := runEnv(nil, []string{"router"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(show, `Registry default profile "base" applies`) {
		t.Fatalf("show without block:\n%s", show)
	}
	if err := runEnv(nil, []string{"router", "set", "OWN=1"}); err != nil {
		t.Fatal(err)
	}
	show = captureStdout(t, func() {
		if err := runEnv(nil, []string{"router"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(show, "  default   base\n") || !strings.Contains(show, "Effective at launch:\n  set    FROM_DEFAULT=yes\n  set    OWN=1\n") {
		t.Fatalf("show with block:\n%s", show)
	}
	// Delete is refused while it is the default; undefault clears it.
	if err := runEnvProfile(nil, []string{"base", "delete"}); err == nil || !strings.Contains(err.Error(), "registry default") {
		t.Fatalf("delete of the default: %v", err)
	}
	if err := runEnvProfile(nil, []string{"other", "undefault"}); err == nil {
		t.Fatal("undefault of a non-default profile succeeded")
	}
	if err := runEnvProfile(nil, []string{"base", "undefault"}); err != nil {
		t.Fatal(err)
	}
	if d, _ := envprofile.Default(dir); d != "" {
		t.Fatalf("default after undefault = %q", d)
	}
	if err := runEnvProfile(nil, []string{"base", "delete"}); err != nil {
		t.Fatal(err)
	}
	_ = root
}

package cmd

import (
	"strconv"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
	"github.com/ramazanpolat/claude-playbooks/internal/settings"
)

// SET IF UNSET k = v, … (SPEC.md, "Playbook properties"): the list applies
// whole, and only when none of its keys is set, each key at the value
// DELETE gives it. Otherwise it changes nothing and says which key was set.

// resolveIfUnset is the statement with each SET IF UNSET either replaced by
// its pairs (none of its keys is set) or left out (one is). A statement
// that has none is returned as it is.
func (r *stmtRun) resolveIfUnset(st *grammar.Stmt) (*grammar.Stmt, error) {
	if !hasIfUnset(st.Clauses) {
		return st, nil
	}
	pb, exists, err := r.findPlaybook(st.Name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return st, nil // the statement fails on the playbook, as it would anyway
	}
	live, err := r.propertyReader(st.Name, pb)
	if err != nil {
		return nil, err
	}
	out := *st
	out.Clauses = nil
	for _, c := range st.Clauses {
		if c.Kind != grammar.SetIfUnset {
			out.Clauses = append(out.Clauses, c)
			continue
		}
		if key := setKeyOf(st.Name, c.Group, live); key != "" {
			msg := "PLAYBOOK " + st.Name + ": " + key + " is set; SET IF UNSET changed nothing"
			r.say(msg, nil)
			if r.dryRun {
				r.note = strings.TrimPrefix(r.note+"; ", "; ") + key + " is set, so SET IF UNSET is skipped"
			}
			continue
		}
		out.Clauses = append(out.Clauses, c.Group...)
	}
	return &out, nil
}

func hasIfUnset(clauses []grammar.Clause) bool {
	for _, c := range clauses {
		if c.Kind == grammar.SetIfUnset {
			return true
		}
	}
	return false
}

// setKeyOf is the first key of a SET IF UNSET list that is set, "" when none
// is.
func setKeyOf(name string, group []grammar.Clause, live func(string) (string, bool)) string {
	for _, key := range ifUnsetKeys(group) {
		v, ok := live(key)
		if !ok {
			continue
		}
		if d, has := propertyDefault(name, key); !has || v != d {
			return key
		}
	}
	return ""
}

// ifUnsetKeys lists the property keys a SET IF UNSET list names.
func ifUnsetKeys(group []grammar.Clause) []string {
	var keys []string
	for _, c := range group {
		switch c.Kind {
		case grammar.SetProperties:
			for _, v := range c.Settings {
				keys = append(keys, v.Key)
			}
		case grammar.SetSandboxKeys:
			for _, v := range c.Settings {
				keys = append(keys, "sandbox."+v.Key)
			}
		case grammar.SetStatusline:
			keys = append(keys, "statusline.command")
			if c.Refresh > 0 {
				keys = append(keys, "statusline.refresh")
			}
		case grammar.SetStatuslineRefresh:
			keys = append(keys, "statusline.refresh")
		case grammar.SetModel:
			keys = append(keys, "model")
		case grammar.SetAgent:
			keys = append(keys, "agent")
		case grammar.SetModelPicker:
			keys = append(keys, "model_picker.mode")
		case grammar.Launcher, grammar.NoLauncher:
			keys = append(keys, "launcher")
		}
	}
	return keys
}

// ifUnsetValues is what a SET IF UNSET list writes, key by key, as
// propertyReader reads it back.
func ifUnsetValues(group []grammar.Clause) map[string]string {
	out := map[string]string{}
	for _, c := range group {
		switch c.Kind {
		case grammar.SetProperties:
			for _, v := range c.Settings {
				out[v.Key] = v.Value
			}
		case grammar.SetSandboxKeys:
			for _, v := range c.Settings {
				out["sandbox."+v.Key] = v.Value
			}
		case grammar.SetStatusline:
			out["statusline.command"] = c.Arg
			if c.Refresh > 0 {
				out["statusline.refresh"] = strconv.Itoa(c.Refresh)
			}
		case grammar.SetStatuslineRefresh:
			out["statusline.refresh"] = strconv.Itoa(c.Refresh)
		case grammar.SetModel:
			out["model"] = c.Arg
		case grammar.SetAgent:
			out["agent"] = c.Arg
		case grammar.SetModelPicker:
			out["model_picker.mode"] = strings.ToLower(c.Arg)
		case grammar.Launcher:
			out["launcher"] = c.Arg
		case grammar.NoLauncher:
			out["launcher"] = ""
		}
	}
	return out
}

// propertyDefault is the value DELETE gives a key, when it has one: the
// playbook's own name for the launcher, the first value of an enumerated
// key, false for a switch. A key with none (model, sandbox.host) is unset
// when it has no value at all.
func propertyDefault(name, key string) (string, bool) {
	switch key {
	case "launcher":
		return name, true
	case "sandbox.always", "sandbox.share_skills":
		return "false", true
	}
	if d := grammar.PlaybookPropertyDefault(key); d != "" && key != "sandbox.secrets" && key != "model_picker.mode" {
		return d, true
	}
	return "", false
}

// propertyReader reads a playbook's properties as the run sees them:
// settings.json as earlier statements of a dry run left it, the login and
// the sandbox switch likewise, the rest from disk. ok is false for a key
// with no value.
func (r *stmtRun) propertyReader(name string, pb *playbook.Playbook) (func(key string) (string, bool), error) {
	var m *manifest.Manifest
	cfg := ""
	if pb != nil {
		m, cfg = pb.Manifest, pb.Path
	}
	root := settings.NewObject()
	switch {
	case r.dry != nil && r.dry.settings[name] != nil:
		o, err := settings.ParseObject(r.dry.settings[name])
		if err != nil {
			return nil, err
		}
		root = o
	case cfg != "":
		sf, err := settings.Load(cfg)
		if err != nil {
			return nil, err
		}
		root = sf.Root
	}
	return func(key string) (string, bool) {
		switch key {
		case "launcher":
			if !launcherOpsAllowed() {
				return name, true
			}
			return r.launcherOf(name, pb), true
		case "login":
			if r.isolated(name, m) || r.sandboxed(name, m) {
				return "isolated", true
			}
			return "shared", true
		case "memory":
			v, _ := memoryState(root)
			return v, true
		case "model":
			var s string
			ok, err := root.Get(keyModel, &s)
			return s, ok && err == nil
		case "agent":
			var s string
			ok, err := root.Get(keyAgent, &s)
			return s, ok && err == nil
		case "statusline.command":
			if _, sl, _ := settingsExtras(root); sl != nil {
				return *sl, true
			}
			return "", false
		case "statusline.refresh":
			if n := statuslineRefresh(root); n != nil {
				return strconv.Itoa(*n), true
			}
			return "", false
		case "model_picker.mode":
			mp, err := root.Object(keyModelPicker)
			if err != nil || !mp.Has("replaceBuiltInOptions") {
				return "", false
			}
			var only bool
			_, _ = mp.Get("replaceBuiltInOptions", &only)
			if only {
				return "only", true
			}
			return "append", true
		case "sandbox.always":
			return strconv.FormatBool(r.sandboxed(name, m)), true
		}
		if k, ok := strings.CutPrefix(key, "sandbox."); ok && m != nil {
			for _, kv := range m.Sandbox.Settings() {
				if kv[0] == k {
					return kv[1], true
				}
			}
		}
		return "", false
	}, nil
}

// livePropertyValues reads the given keys of a playbook on disk, for an
// update deciding whether a SET IF UNSET it once applied still holds its
// value. A playbook that is gone, or cannot be read, reads as nothing.
func livePropertyValues(name string, keys []string) map[string]string {
	out := map[string]string{}
	pb, err := playbook.Find(config.ResolvePlaybooksDir(), name)
	if err != nil || pb == nil {
		return out
	}
	live, err := (&stmtRun{}).propertyReader(name, pb)
	if err != nil {
		return out
	}
	for _, k := range keys {
		if v, ok := live(k); ok {
			out[k] = v
		}
	}
	return out
}

// Package tui is `cpb tui`: a terminal UI over cpb's grammar (v3.25.0).
//
// It is a thin front-end. Every read is a cpb statement's --json output,
// run as a subprocess of cpb itself (see Runner); the package imports
// nothing of cpb's engine, so what it shows is exactly what the command
// line prints, and it can do nothing the grammar cannot. v1 only reads:
// it browses, shows SHOW CREATE, copies statements, exports a playbook's
// SHOW CREATE as a .cpb file, and copies the command that resumes a session.
//
// Secret values never reach it: cpb's --json prints references as
// references and plaintext credentials redacted, and SHOW CREATE is always
// run with --skip-secrets.
package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Runner runs one cpb command and returns its stdout. A non-zero exit is an
// error carrying cpb's stderr, which is where its refusals are.
type Runner interface {
	Run(args ...string) ([]byte, error)
}

// ExecRunner runs cpb's own binary. Prefix goes before every command: the
// --playbooks-dir the TUI itself was started with, so both look at the
// same registry.
type ExecRunner struct {
	Bin    string
	Prefix []string
}

func (r ExecRunner) Run(args ...string) ([]byte, error) {
	c := exec.Command(r.Bin, append(append([]string{}, r.Prefix...), args...)...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		msg := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(stderr.String()), "Error: "))
		if msg == "" {
			msg = err.Error()
		}
		return out, errors.New(msg)
	}
	return out, nil
}

// The shapes below are the subset of cpb's --json objects the TUI shows
// (SPEC.md, "Output" and "Sessions"). Fields it does
// not show are not decoded.

type Var struct {
	Key       string  `json:"key"`
	Value     *string `json:"value"`
	Ref       *string `json:"ref"`
	Redacted  bool    `json:"redacted"`
	Plaintext bool    `json:"plaintext"`
	Blocked   bool    `json:"blocked"`
	Layer     *struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"layer"`
}

// Shown is a variable as the TUI prints it: a reference as a reference, a
// redacted credential as "(redacted…)", never a secret value (cpb has
// already withheld it).
func (v Var) Shown() string {
	switch {
	case v.Blocked:
		return "blocked"
	case v.Ref != nil:
		return "FROM '" + *v.Ref + "'"
	case v.Redacted && v.Plaintext:
		return "(redacted, plaintext)"
	case v.Redacted:
		return "(redacted)"
	case v.Value != nil:
		return *v.Value
	}
	return "-"
}

// LayerName is where an effective variable comes from, as EXPLAIN says.
func (v Var) LayerName() string {
	if v.Layer == nil {
		return "-"
	}
	switch v.Layer.Kind {
	case "env", "defaults":
		return v.Layer.Kind + " " + v.Layer.Name
	case "playbook":
		return "playbook"
	}
	return v.Layer.Kind + " " + v.Layer.Name
}

type Playbook struct {
	Name    string  `json:"name"`
	Version *string `json:"version"`
	Path    string  `json:"path"`
	Source  *struct {
		URL    string  `json:"url"`
		Branch *string `json:"branch"`
	} `json:"source"`
	Linked   *string  `json:"linked"`
	Launcher *string  `json:"launcher"`
	Envs     []string `json:"envs"`
	Vars     []Var    `json:"vars"`
	Sandbox  struct {
		Always bool `json:"always"`
	} `json:"sandbox"`
	IsolatedLogin bool `json:"isolated_login"`
	Marketplaces  []struct {
		Name string `json:"name"`
	} `json:"marketplaces"`
	Plugins []struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	} `json:"plugins"`
	Agent      *string `json:"agent"`
	MCPServers []struct {
		Name      string  `json:"name"`
		Transport string  `json:"transport"`
		Command   *string `json:"command"`
		URL       *string `json:"url"`
		Env       []Var   `json:"env"`
		Headers   []Var   `json:"headers"`
	} `json:"mcp_servers"`
	Tools struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	} `json:"tools"`
	Skills []struct {
		Name   string `json:"name"`
		Source string `json:"source"`
		Mode   string `json:"mode"`
	} `json:"skills"`
	Statusline        *string `json:"statusline"`
	StatuslineRefresh *int    `json:"statusline_refresh"`
	StatuslineHistory []struct {
		Command string `json:"command"`
	} `json:"statusline_history"`
	Model       *string `json:"model"`
	ModelPicker *struct {
		Mode    string `json:"mode"`
		Options []struct {
			Model string  `json:"model"`
			Label *string `json:"label"`
		} `json:"options"`
	} `json:"model_picker"`
}

// Login is the kind of login the playbook has: never a value.
func (p Playbook) Login() string {
	switch {
	case p.Sandbox.Always:
		return "sandbox"
	case p.IsolatedLogin:
		return "isolated"
	}
	return "shared"
}

type Session struct {
	Playbook   string  `json:"playbook"`
	ConfigDir  string  `json:"config_dir"`
	PID        int     `json:"pid"`
	SessionID  string  `json:"session_id"`
	Cwd        string  `json:"cwd"`
	Kind       string  `json:"kind"`
	Status     *string `json:"status"`
	Name       *string `json:"name"`
	StartedAt  string  `json:"started_at"`
	LastActive *string `json:"last_active"`
	Model      *string `json:"model"`
	Launcher   *string `json:"launcher"`
	Resume     string  `json:"resume"`
	TTY        *string `json:"tty"` // v3.25.0; nil from an older cpb
}

type EnvSet struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Vars        []Var    `json:"vars"`
	UsedBy      []string `json:"used_by"`
	Default     bool     `json:"default"`
}

type Defaults struct {
	Envs         []string `json:"envs"`
	SecretHelper *struct {
		Command string `json:"command"`
		From    string `json:"from"`
	} `json:"secret_helper"`
}

type Explain struct {
	Vars []Var `json:"vars"`
}

// State is everything the TUI shows, as read from cpb.
type State struct {
	Playbooks []Playbook
	Sessions  []Session
	Envs      []EnvSet
	Defaults  Defaults
}

// PartialRead is a list read that printed the rows it could read and left
// out playbooks whose manifests cannot be read, exiting 1: the rows are
// kept, and the message (cpb's stderr) says which were left out.
type PartialRead struct{ Msg string }

func (p *PartialRead) Error() string { return p.Msg }

func readJSON(r Runner, v any, args ...string) error {
	out, err := r.Run(args...)
	if err != nil {
		if len(bytes.TrimSpace(out)) > 0 && json.Unmarshal(out, v) == nil {
			return &PartialRead{Msg: strings.Join(strings.Fields(strings.ReplaceAll(err.Error(), "\n", "; ")), " ")}
		}
		return fmt.Errorf("cpb %s: %w", strings.Join(args, " "), err)
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("cpb %s: %v", strings.Join(args, " "), err)
	}
	return nil
}

// The statements behind each read, shown in the status bar.
var (
	readPlaybooks = []string{"SHOW", "PLAYBOOKS", "--json"}
	readSessions  = []string{"SHOW", "SESSIONS", "--json"}
	readEnvs      = []string{"SHOW", "ENVS", "--json"}
	readDefaults  = []string{"SHOW", "DEFAULTS", "--json"}
)

// loadState reads everything the screens show. A read that left playbooks
// out keeps its rows; the state comes back with that PartialRead.
func loadState(r Runner) (State, error) {
	var s State
	var partial *PartialRead
	read := func(v any, args ...string) error {
		err := readJSON(r, v, args...)
		var p *PartialRead
		if errors.As(err, &p) {
			if partial == nil {
				partial = p
			}
			return nil
		}
		return err
	}
	if err := read(&s.Playbooks, readPlaybooks...); err != nil {
		return s, err
	}
	if err := read(&s.Sessions, readSessions...); err != nil {
		return s, err
	}
	if err := read(&s.Envs, readEnvs...); err != nil {
		return s, err
	}
	if err := read(&s.Defaults, readDefaults...); err != nil {
		return s, err
	}
	if partial != nil {
		return s, partial
	}
	return s, nil
}

func loadSessions(r Runner) ([]Session, error) {
	var s []Session
	return s, readJSON(r, &s, readSessions...)
}

func loadExplain(r Runner, name string) (Explain, error) {
	var e Explain
	return e, readJSON(r, &e, "EXPLAIN", "PLAYBOOK", name, "--json")
}

// showCreate is SHOW CREATE for one object, always without secrets.
func showCreate(r Runner, object, name string) (string, error) {
	out, err := r.Run("SHOW", "CREATE", object, name, "--skip-secrets")
	if err != nil {
		return "", fmt.Errorf("cpb SHOW CREATE %s %s: %w", object, name, err)
	}
	return string(out), nil
}

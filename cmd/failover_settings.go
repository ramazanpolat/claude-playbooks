package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
	"github.com/ramazanpolat/claude-playbooks/internal/tomlfile"
)

// Where failover is set (SPEC.md, "Account failover"):
//
//	ALTER DEFAULTS SET failover = 'on'                        every playbook that inherits
//	ALTER DEFAULTS SET failover_accounts = ['a', 'b (any provider)']
//	ALTER DEFAULTS SET failover_max_context = 400000           optional; no cap by default
//	ALTER PLAYBOOK <p> SET failover = 'on' | 'off'             'inherited' by default
//	cpb run --failover | --no-failover <p>                     one launch
//
// The DEFAULTS settings live in <env sets dir>/.failover, TOML (0600). The
// shipped default is off.

const failoverDefaultsFile = ".failover"

// anyProviderMark is what an entry of failover_accounts says to take a
// session from an account of another kind.
const anyProviderMark = " (any provider)"

// failoverDefaults is DEFAULTS' failover settings.
type failoverDefaults struct {
	Failover   string   `toml:"failover"`
	Accounts   []string `toml:"accounts"`
	MaxContext int64    `toml:"max_context"`
}

func failoverDefaultsPath(root string) string {
	return filepath.Join(envset.Dir(root), failoverDefaultsFile)
}

// readFailoverDefaults reads DEFAULTS' failover settings; none is the
// zero value.
func readFailoverDefaults(root string) (failoverDefaults, error) {
	var d failoverDefaults
	path := failoverDefaultsPath(root)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	if err := tomlfile.Decode(path, b, &d); err != nil {
		return d, err
	}
	if d.Failover != "" && d.Failover != "on" && d.Failover != "off" {
		return d, fmt.Errorf("%s: failover is 'on' or 'off', not %q", path, d.Failover)
	}
	for _, a := range d.Accounts {
		if _, err := parseFailoverTarget(a); err != nil {
			return d, fmt.Errorf("%s: %w", path, err)
		}
	}
	if d.MaxContext < 0 {
		return d, fmt.Errorf("%s: max_context is a number of tokens, 0 or more", path)
	}
	return d, nil
}

// writeFailoverDefaults stores d, or removes the file when d holds nothing.
func writeFailoverDefaults(root string, d failoverDefaults) error {
	path := failoverDefaultsPath(root)
	if d.Failover == "" && len(d.Accounts) == 0 && d.MaxContext == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	var b strings.Builder
	b.WriteString("# DEFAULTS' account failover; written by cpb ALTER DEFAULTS SET failover…\n")
	if d.Failover != "" {
		b.WriteString("failover = " + strconv.Quote(d.Failover) + "\n")
	}
	if len(d.Accounts) > 0 {
		q := make([]string, len(d.Accounts))
		for i, a := range d.Accounts {
			q[i] = strconv.Quote(a)
		}
		b.WriteString("accounts = [" + strings.Join(q, ", ") + "]\n")
	}
	if d.MaxContext > 0 {
		b.WriteString("max_context = " + strconv.FormatInt(d.MaxContext, 10) + "\n")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return manifest.WritePrivate(path, []byte(b.String()), 0o600)
}

// parseFailoverTarget reads one entry of failover_accounts: an account
// name, or one followed by " (any provider)".
func parseFailoverTarget(s string) (failoverTarget, error) {
	name, any := strings.CutSuffix(strings.TrimSpace(s), anyProviderMark)
	name = strings.TrimSpace(name)
	if name != account.Machine {
		if err := account.ValidateName(name); err != nil {
			return failoverTarget{}, fmt.Errorf("failover_accounts: %w", err)
		}
	}
	return failoverTarget{name: name, anyProvider: any}, nil
}

// accountLookup finds an account by name: account.Must, or a statement
// run's own, which sees what an earlier statement of a dry run created.
type accountLookup func(dir, name string) (*account.Account, error)

// checkFailoverAccounts refuses a list naming an account that does not
// exist, or one account twice. require is nil for account.Must.
func checkFailoverAccounts(root string, list []string, require accountLookup) error {
	if require == nil {
		require = account.Must
	}
	seen := map[string]bool{}
	for _, s := range list {
		t, err := parseFailoverTarget(s)
		if err != nil {
			return err
		}
		if seen[t.name] {
			return fmt.Errorf("failover_accounts names %s twice", t.name)
		}
		seen[t.name] = true
		if t.name == account.Machine {
			continue
		}
		if _, err := require(account.Dir(root), t.name); err != nil {
			return fmt.Errorf("failover_accounts: %w", err)
		}
	}
	return nil
}

// Playbook failover values; the first is the default.
const (
	failoverInherited = "inherited"
	failoverOn        = "on"
	failoverOff       = "off"
)

// failoverConfigFor is what a launch of a playbook (manifest m) does at a
// limit: the launch flag wins, then the playbook's own failover, then
// DEFAULTS'. The accounts and the cap are DEFAULTS'.
func failoverConfigFor(root string, m *manifest.Manifest, flagOn, flagOff bool) (failoverConfig, error) {
	var cfg failoverConfig
	if flagOff {
		// Off for this launch: nothing else is read, so a setting that
		// cannot be read does not stop it.
		return cfg, nil
	}
	if m != nil {
		switch m.Failover {
		case "", failoverInherited, failoverOn, failoverOff:
		default:
			return cfg, fmt.Errorf("the playbook's %s has failover = %q; it takes 'inherited', 'on' or 'off' (ALTER PLAYBOOK … SET failover = '…')", manifest.FileName, m.Failover)
		}
	}
	d, err := readFailoverDefaults(root)
	if err != nil {
		return cfg, err
	}
	pbSetting := ""
	if m != nil {
		pbSetting = m.Failover
	}
	switch {
	case flagOn:
		cfg.on = true
	case flagOff:
		cfg.on = false
	case pbSetting == failoverOn:
		cfg.on = true
	case pbSetting == failoverOff:
		cfg.on = false
	default:
		cfg.on = d.Failover == failoverOn
	}
	for _, a := range d.Accounts {
		t, _ := parseFailoverTarget(a)
		cfg.accounts = append(cfg.accounts, t)
	}
	cfg.maxContext = d.MaxContext
	return cfg, nil
}

// applyFailoverDefaults is DEFAULTS' failover settings after SET and RESET
// clauses of failover, failover_accounts and failover_max_context, and the
// lines that say what changed (none when nothing does). The accounts are
// checked: each exists, none twice.
func applyFailoverDefaults(root string, cur failoverDefaults, clauses []grammar.Clause, require accountLookup) (failoverDefaults, []string, error) {
	next := cur
	next.Accounts = append([]string(nil), cur.Accounts...)
	for _, c := range clauses {
		reset := c.Kind == grammar.ResetProperties
		for _, v := range c.Settings {
			switch v.Key {
			case "failover":
				next.Failover = v.Value
				if reset {
					next.Failover = ""
				}
			case "failover_accounts":
				next.Accounts = nil
				if !reset {
					for _, item := range strings.Split(v.Value, ",") {
						if item = strings.TrimSpace(item); item != "" {
							next.Accounts = append(next.Accounts, item)
						}
					}
				}
			case "failover_max_context":
				next.MaxContext = 0
				if !reset {
					next.MaxContext, _ = strconv.ParseInt(v.Value, 10, 64)
				}
			case "label_vars":
				// label_vars.go: a DEFAULTS property of its own file.
			default:
				return cur, nil, fmt.Errorf("DEFAULTS has no property %s", v.Key)
			}
		}
	}
	if err := checkFailoverAccounts(root, next.Accounts, require); err != nil {
		return cur, nil, err
	}
	var lines []string
	if next.Failover != cur.Failover {
		lines = append(lines, "failover  "+failoverWord(next.Failover))
	}
	if strings.Join(next.Accounts, "\x00") != strings.Join(cur.Accounts, "\x00") {
		lines = append(lines, "failover accounts  "+listOrNone(next.Accounts))
	}
	if next.MaxContext != cur.MaxContext {
		cap := "(no cap)"
		if next.MaxContext > 0 {
			cap = groupDigits(next.MaxContext) + " tokens"
		}
		lines = append(lines, "failover max context  "+cap)
	}
	return next, lines, nil
}

func failoverWord(v string) string {
	if v == "" {
		return "off"
	}
	return v
}

// failoverMeaning is what a playbook's failover value means, for the line
// a statement prints.
func failoverMeaning(v string) string {
	switch v {
	case failoverOn:
		return ": at a usage limit, a launch resumes its session on the next account of DEFAULTS' failover_accounts"
	case failoverOff:
		return ": a launch never moves its session to another account"
	}
	return ": DEFAULTS' failover decides"
}

// withFailoverDefaults fills DEFAULTS' failover fields of v.
func withFailoverDefaults(v *defaultsJSON, root string) error {
	d, err := readFailoverDefaults(root)
	if err != nil {
		return fmt.Errorf("DEFAULTS' failover settings cannot be read: %w", err)
	}
	v.Failover = failoverWord(d.Failover)
	v.FailoverAccounts = append([]string{}, d.Accounts...)
	if d.MaxContext > 0 {
		n := d.MaxContext
		v.FailoverMaxContext = &n
	}
	return nil
}

// failoverDefaultsLine is SHOW DEFAULTS' Failover line.
func failoverDefaultsLine(v defaultsJSON) string {
	line := v.Failover
	if len(v.FailoverAccounts) > 0 {
		line += "; accounts " + strings.Join(v.FailoverAccounts, ", ")
	} else if v.Failover == failoverOn {
		line += "; no accounts listed (SET failover_accounts = [...])"
	}
	if v.FailoverMaxContext != nil {
		line += "; at most " + groupDigits(*v.FailoverMaxContext) + " tokens of context"
	}
	return line
}

// failoverExplainJSON is EXPLAIN's failover: whether a launch with no flags
// moves its session at a limit, what decides it, and to which accounts.
type failoverExplainJSON struct {
	On         bool     `json:"on"`
	From       string   `json:"from"` // "playbook", "DEFAULTS", "default", or "unreadable"
	Accounts   []string `json:"accounts"`
	MaxContext *int64   `json:"max_context"`
	Error      *string  `json:"error"`
	// NoSandbox is, for an always-sandboxed playbook, what a --no-sandbox
	// launch of it does; null otherwise.
	NoSandbox *failoverExplainOn `json:"no_sandbox"`
}

// failoverExplainOn is whether failover is on, and what decides it.
type failoverExplainOn struct {
	On   bool   `json:"on"`
	From string `json:"from"`
}

func explainFailover(pb *playbook.Playbook) failoverExplainJSON {
	root := config.ResolvePlaybooksDir()
	v := failoverExplainJSON{Accounts: []string{}, From: "default"}
	d, err := readFailoverDefaults(root)
	if err != nil {
		msg := err.Error()
		v.From, v.Error = "unreadable", &msg
		return v
	}
	v.Accounts = append(v.Accounts, d.Accounts...)
	if d.MaxContext > 0 {
		n := d.MaxContext
		v.MaxContext = &n
	}
	switch {
	case pb.Manifest != nil && (pb.Manifest.Failover == failoverOn || pb.Manifest.Failover == failoverOff):
		v.On, v.From = pb.Manifest.Failover == failoverOn, "playbook"
	case d.Failover != "":
		v.On, v.From = d.Failover == failoverOn, "DEFAULTS"
	}
	if pb.Manifest != nil && pb.Manifest.Sandbox != nil && pb.Manifest.Sandbox.Always {
		v.NoSandbox = &failoverExplainOn{On: v.On, From: v.From}
		v.On, v.From = false, "sandbox"
	}
	return v
}

func explainFailoverLine(v failoverExplainJSON) string {
	switch {
	case v.Error != nil:
		return "Failover: cannot be read (" + *v.Error + ")"
	case v.NoSandbox != nil:
		// An always-sandboxed playbook: off as launched, and what a
		// --no-sandbox launch of it does.
		return "Failover: off (" + failoverFromWords(v.From) + "); with --no-sandbox, " + failoverOnWords(v, v.NoSandbox.On, v.NoSandbox.From)
	}
	return "Failover: " + failoverOnWords(v, v.On, v.From)
}

// failoverOnWords is "on (…); at a usage limit, …" or "off (…)".
func failoverOnWords(v failoverExplainJSON, on bool, from string) string {
	if !on {
		return "off (" + failoverFromWords(from) + ")"
	}
	line := "on (" + failoverFromWords(from) + "); at a usage limit, the session moves to " + listOrNone(v.Accounts)
	if len(v.Accounts) == 0 {
		line = "on (" + failoverFromWords(from) + "), but no account is listed (ALTER DEFAULTS SET failover_accounts = [...])"
	}
	if v.MaxContext != nil {
		line += "; at most " + groupDigits(*v.MaxContext) + " tokens of context"
	}
	return line
}

func failoverFromWords(from string) string {
	switch from {
	case "playbook":
		return "the playbook's failover"
	case "DEFAULTS":
		return "DEFAULTS"
	case "sandbox":
		return "a sandboxed launch's session runs where cpb cannot watch it"
	}
	return "the default"
}

// errFailoverSandbox refuses --failover on a sandboxed launch: its session
// runs where the launch cannot watch it.
var errFailoverSandbox = errors.New("--failover is for a launch on this machine: a sandboxed launch's session runs where cpb cannot watch it")

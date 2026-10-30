package cmd

import (
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// credentialVar is the variable an account's secret credential becomes at
// launch: the key the secret helper is asked about.
var credentialVar = map[string]string{
	account.AuthToken:   "ANTHROPIC_AUTH_TOKEN",
	account.APIKey:      "ANTHROPIC_API_KEY",
	account.ClaudeToken: "CLAUDE_CODE_OAUTH_TOKEN",
}

// accountStatement runs CREATE, ALTER and DROP ACCOUNT.
func accountStatement(r *stmtRun, st *grammar.Stmt) error {
	playbooksDir := config.ResolvePlaybooksDir()
	dir := account.Dir(playbooksDir)

	unlock, err := r.lockRegistry()
	if err != nil {
		return err
	}
	defer unlock()

	a, err := r.account(dir, st.Name)
	if err != nil {
		return err
	}
	if st.Verb == grammar.Drop {
		return r.dropAccount(playbooksDir, dir, st, a)
	}
	// CREATE … IF NOT EXISTS on an existing account writes nothing, so its
	// references are not checked: the helper is not even asked.
	if st.Verb == grammar.Create && a != nil && st.IfNotExists {
		r.outcome = outUnchanged
		r.say("ACCOUNT "+st.Name+" already exists; unchanged", nil)
		return nil
	}
	if err := r.checkRefs(accountRefClauses(st.Clauses)); err != nil {
		return err
	}
	var old *account.Account
	switch st.Verb {
	case grammar.Create:
		if a != nil && !st.OrReplace {
			return fmt.Errorf("account %q already exists: change it with ALTER ACCOUNT %s, or use CREATE OR REPLACE ACCOUNT / CREATE ACCOUNT IF NOT EXISTS", st.Name, st.Name)
		}
		old = a
		a = &account.Account{Name: st.Name}
	case grammar.Alter:
		if a == nil {
			return fmt.Errorf("no account %q: create it with CREATE ACCOUNT %s SET provider = '<provider>', …", st.Name, st.Name)
		}
		c := *a
		c.Headers = maps.Clone(a.Headers) // the copy may not share what the statement changes
		old = &c
	}
	lines := applyAccountClauses(a, st.Clauses)
	if err := a.Validate(); err != nil {
		return fmt.Errorf("account %s: %w", st.Name, err)
	}
	// A listed account cannot be the DEFAULTS account: it runs only for
	// playbooks that name it.
	if a.Listed() {
		def, err := r.defaultAccount(dir)
		if err == nil && def == st.Name {
			return fmt.Errorf("account %q is the DEFAULTS account, which any playbook may run on: it cannot be access = 'listed' (ALTER DEFAULTS USE ACCOUNT '' first)", st.Name)
		}
	}
	switch {
	case old == nil:
		r.outcome = outCreated
	case reflect.DeepEqual(*old, *a):
		r.outcome = outUnchanged
		r.say("ACCOUNT "+st.Name+" unchanged", nil)
		return nil
	default:
		r.outcome = outChanged
	}
	if r.dryRun {
		r.recordAccount(st.Name, a)
		return nil
	}
	if err := account.Write(dir, a); err != nil {
		return fmt.Errorf("cannot write account: %w", err)
	}
	head := "Altered ACCOUNT " + st.Name
	switch {
	case r.outcome == outCreated:
		head = "Created ACCOUNT " + st.Name
	case st.Verb == grammar.Create:
		head = "Replaced ACCOUNT " + st.Name
	}
	r.say(head, lines)
	return nil
}

// accountRefClauses turns an account statement's references into the SET
// FROM clauses the secret helper check reads, under the variable each one
// becomes at launch.
func accountRefClauses(clauses []grammar.Clause) []grammar.Clause {
	var out []grammar.Clause
	for _, c := range clauses {
		for _, v := range c.Settings {
			if v.Ref != "" {
				k := credentialVar[v.Key]
				if strings.HasPrefix(strings.ToLower(v.Key), account.HeaderKeyPrefix) {
					k = account.HeaderVar(0)
				}
				out = append(out, grammar.Clause{Kind: grammar.SetRef, Vars: []grammar.Var{{Key: k, Ref: v.Ref}}})
			}
		}
	}
	return out
}

// applyAccountClauses applies SET and RESET to an account and returns one
// report line per key. A credential's value is never reported; a reference
// is not a secret and is.
func applyAccountClauses(a *account.Account, clauses []grammar.Clause) []string {
	var lines []string
	oldBase, oldProvider, trustStated := a.BaseURL, a.Provider, false
	headersStated := map[string]bool{}
	for _, c := range clauses {
		for _, v := range c.Settings {
			if v.Key == "claude_login_trusted" {
				trustStated = true
			}
			if name, ok := headerName(v.Key); ok && c.Kind == grammar.SetAccount {
				headersStated[strings.ToLower(name)] = true
			}
		}
	}
	for _, c := range clauses {
		for _, v := range c.Settings {
			if name, ok := headerName(v.Key); ok {
				if c.Kind == grammar.ResetAccount {
					if a.ResetHeader(name) {
						lines = append(lines, account.HeaderKeyPrefix+name+"  (reset)")
					} else {
						lines = append(lines, account.HeaderKeyPrefix+name+"  (not set)")
					}
				} else {
					a.SetHeader(name, v.Ref)
					lines = append(lines, account.HeaderKeyPrefix+name+"  <from "+v.Ref+">")
				}
				continue
			}
			if c.Kind == grammar.ResetAccount {
				switch v.Key {
				case "base_url":
					a.BaseURL = ""
				case "identity":
					a.Identity = ""
				case "access":
					a.Access = ""
				case "claude_login_trusted":
					a.ClaudeLoginTrusted = false
				case "description":
					a.Description = ""
				default:
					a.SetModel(v.Key, "")
				}
				lines = append(lines, v.Key+"  (reset)")
				continue
			}
			switch v.Key {
			case "provider":
				a.Provider = v.Value
			case "base_url":
				a.BaseURL = v.Value
			case "identity":
				a.Identity = v.Value
			case "access":
				a.Access = v.Value
				if v.Value == account.AccessAny {
					a.Access = ""
				}
			case "claude_login_trusted":
				a.ClaudeLoginTrusted = v.Value == "true"
			case "description":
				a.Description = v.Value
			case "auth":
				a.Auth = account.Auth{Kind: v.Value}
			case account.Helper:
				a.Auth = account.Auth{Kind: account.Helper, Helper: v.Value}
			case account.AuthToken, account.APIKey, account.ClaudeToken:
				a.Auth = account.Auth{Kind: v.Key, Ref: v.Ref, Value: v.Value}
				if v.Ref != "" {
					lines = append(lines, v.Key+"  <from "+v.Ref+">")
				} else {
					lines = append(lines, v.Key+"  <plaintext>")
				}
				continue
			default:
				a.SetModel(v.Key, v.Value)
			}
			lines = append(lines, v.Key+"  "+v.Value)
		}
	}
	// Trust is in one gateway: an address that moves loses it unless the
	// same statement says it again.
	if a.BaseURL != oldBase && a.ClaudeLoginTrusted && !trustStated {
		a.ClaudeLoginTrusted = false
		lines = append(lines, "claude_login_trusted  false (base_url moved; SET claude_login_trusted = true again for the new address)")
	}
	// Headers are for one gateway too: an account that moves keeps only
	// the headers its statement states again.
	if a.BaseURL != oldBase || a.Provider != oldProvider {
		for _, n := range a.HeaderNames() {
			if !headersStated[strings.ToLower(n)] {
				a.ResetHeader(n)
				lines = append(lines, account.HeaderKeyPrefix+n+"  (removed: the account moved; SET "+account.HeaderKeyPrefix+n+" FROM '<ref>' again for the new address)")
			}
		}
	}
	return lines
}

// headerName is the name in a header.<Name> key.
func headerName(key string) (string, bool) {
	if len(key) > len(account.HeaderKeyPrefix) && strings.EqualFold(key[:len(account.HeaderKeyPrefix)], account.HeaderKeyPrefix) {
		return key[len(account.HeaderKeyPrefix):], true
	}
	return "", false
}

func (r *stmtRun) dropAccount(playbooksDir, dir string, st *grammar.Stmt, a *account.Account) error {
	if a == nil {
		if st.IfExists {
			r.outcome = outUnchanged
			r.say("No account "+st.Name+"; nothing to drop", nil)
			return nil
		}
		return fmt.Errorf("no account %q", st.Name)
	}
	users, bad, err := r.accountUsers(playbooksDir)
	if err != nil {
		return err
	}
	if len(bad) > 0 {
		return fmt.Errorf("cannot drop account %q: a playbook that cannot be read may use it: %w", st.Name, &playbook.UnreadableError{List: bad})
	}
	if u := users[st.Name]; len(u) > 0 {
		return fmt.Errorf("account %q is used by %s: take it off their lists first (ALTER PLAYBOOK <playbook> USE ACCOUNT …)", st.Name, strings.Join(u, ", "))
	}
	def, err := r.defaultAccount(dir)
	if err != nil {
		return fmt.Errorf("cannot read the DEFAULTS account: %w", err)
	}
	if def == st.Name {
		return fmt.Errorf("account %q is the DEFAULTS account: ALTER DEFAULTS USE ACCOUNT '' first", st.Name)
	}
	r.outcome = outDropped
	if r.dryRun {
		r.recordAccount(st.Name, nil)
		return nil
	}
	if err := account.Delete(dir, st.Name); err != nil {
		return err
	}
	fmt.Printf("Dropped ACCOUNT %s\n", st.Name)
	return nil
}

// account reads an account as the run sees it: nil when it does not exist.
func (r *stmtRun) account(dir, name string) (*account.Account, error) {
	if r.dry != nil {
		if a, ok := r.dry.accounts[name]; ok {
			if a == nil {
				return nil, nil
			}
			c := *a
			return &c, nil
		}
	}
	return account.Read(dir, name)
}

func (r *stmtRun) recordAccount(name string, a *account.Account) {
	if r.dry == nil {
		return
	}
	if a != nil {
		c := *a
		a = &c
	}
	r.dry.accounts[name] = a
}

// defaultAccount reads the DEFAULTS account as the run sees it.
func (r *stmtRun) defaultAccount(dir string) (string, error) {
	if r.dry != nil && r.dry.hasDefaultAccount {
		return r.dry.defaultAccount, nil
	}
	return account.Default(dir)
}

// requireAccount checks that a USE ACCOUNT name exists: the implicit
// account always does.
func (r *stmtRun) requireAccount(dir, name string) (*account.Account, error) {
	if name == account.Machine {
		return nil, nil
	}
	a, err := r.account(dir, name)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, &account.MissingError{Name: name}
	}
	return a, nil
}

// applyAccountList applies USE ACCOUNT to a playbook's list and returns the
// new list and its report line. Every account it names must exist.
func (r *stmtRun) applyAccountList(dir string, list []string, clauses []grammar.Clause) ([]string, []string, error) {
	for _, c := range clauses {
		if c.Kind != grammar.UseAccount {
			continue
		}
		for _, n := range c.Names {
			if _, err := r.requireAccount(dir, n); err != nil {
				return nil, nil, err
			}
		}
		if slices.Equal(list, c.Names) {
			return list, nil, nil
		}
		return slices.Clone(c.Names), []string{"accounts  " + listOrNone(c.Names)}, nil
	}
	return list, nil, nil
}

// accountUsers maps each account to the playbooks whose list names it, as
// the run sees them: by the root manifest and the governing one.
func (r *stmtRun) accountUsers(playbooksDir string) (map[string][]string, []playbook.Unreadable, error) {
	pbs, bad, err := playbook.Scan(playbooksDir)
	if err != nil {
		return nil, nil, err
	}
	users := map[string][]string{}
	add := func(pb, name string) {
		if !slices.Contains(users[name], pb) {
			users[name] = append(users[name], pb)
		}
	}
	for _, pb := range pbs {
		if known, exists := r.playbookState(pb.Name); known && !exists {
			continue
		}
		if r.dry != nil {
			if e, ok := r.dry.pbEnvs[pb.Name]; ok {
				if e != nil {
					for _, n := range e.Accounts {
						add(pb.Name, n)
					}
				}
				continue
			}
		}
		governing, _ := governingManifest(pb)
		for _, m := range []*manifest.Manifest{pb.Manifest, governing} {
			if m != nil && m.Env != nil {
				for _, n := range m.Env.Accounts {
					add(pb.Name, n)
				}
			}
		}
	}
	if r.dry != nil {
		for pb, e := range r.dry.pbEnvs {
			if known, exists := r.playbookState(pb); known && exists && e != nil {
				for _, n := range e.Accounts {
					add(pb, n)
				}
			}
		}
	}
	for name := range users {
		sort.Strings(users[name])
	}
	return users, bad, nil
}

// defaultsAccountClause applies ALTER DEFAULTS USE ACCOUNT: one account,
// or ” for none. It returns the name to record and its report line.
func (r *stmtRun) defaultsAccountClause(dir string, c grammar.Clause) (string, string, error) {
	switch len(c.Names) {
	case 0:
		return "", "account  (none)", nil
	case 1:
	default:
		return "", "", fmt.Errorf("DEFAULTS names one account: the account a playbook with none of its own runs on")
	}
	name := c.Names[0]
	if name == account.Machine {
		return "", "account  (none)", nil
	}
	a, err := r.requireAccount(dir, name)
	if err != nil {
		return "", "", err
	}
	if a.Listed() {
		return "", "", fmt.Errorf("account %q is access = 'listed': it runs only for playbooks that list it, so it cannot be the DEFAULTS account", name)
	}
	return name, "account  " + name, nil
}

// accountJSON is SHOW ACCOUNT's record: never a credential's value.
type accountJSON struct {
	Name               string            `json:"name"`
	Description        *string           `json:"description"`
	Provider           string            `json:"provider"`
	BaseURL            *string           `json:"base_url"`
	Identity           *string           `json:"identity"`
	Auth               string            `json:"auth"`
	AuthRef            *string           `json:"auth_ref"`
	AuthPlaintext      bool              `json:"auth_plaintext"`
	Helper             *string           `json:"helper"`
	Access             string            `json:"access"`
	ClaudeLoginTrusted bool              `json:"claude_login_trusted"`
	Models             map[string]string `json:"models"`
	// Headers are the names of the account's extra request headers, each
	// a reference; never a value (v4.3.0).
	Headers []string `json:"headers"`
	UsedBy  []string `json:"used_by"`
	Default bool     `json:"default"`
}

func describeAccount(a *account.Account, users []string, def bool) accountJSON {
	v := accountJSON{
		Name: a.Name, Description: optStr(a.Description), Provider: a.Provider,
		BaseURL: optStr(a.BaseURL), Identity: optStr(a.Identity),
		Auth: a.Auth.Kind, AuthRef: optStr(a.Auth.Ref), AuthPlaintext: a.Auth.Value != "",
		Helper: optStr(a.Auth.Helper), Access: account.AccessAny,
		ClaudeLoginTrusted: a.ClaudeLoginTrusted, Models: map[string]string{},
		Headers: nonNil(a.HeaderNames()), UsedBy: nonNil(users), Default: def,
	}
	if a.Listed() {
		v.Access = account.AccessListed
	}
	for _, k := range account.ModelKeys {
		if m := a.ModelOf(k); m != "" {
			v.Models[k] = m
		}
	}
	return v
}

// authSummary says how an account authenticates, for a human: never a
// value.
func authSummary(v accountJSON) string {
	switch {
	case v.AuthRef != nil:
		return v.Auth + " from " + *v.AuthRef
	case v.AuthPlaintext:
		return v.Auth + " (plaintext)"
	case v.Helper != nil:
		return "helper " + *v.Helper
	}
	return v.Auth
}

func showAccounts(playbooksDir string, st *grammar.Stmt) error {
	dir := account.Dir(playbooksDir)
	var list []*account.Account
	if st.Object == grammar.Account {
		a, err := account.Read(dir, st.Name)
		if err != nil {
			return err
		}
		if a == nil {
			return fmt.Errorf("no account %q", st.Name)
		}
		list = []*account.Account{a}
	} else {
		var err error
		if list, err = account.List(dir); err != nil {
			return err
		}
	}
	r := &stmtRun{}
	users, bad, err := r.accountUsers(playbooksDir)
	if err != nil {
		return err
	}
	def, derr := account.Default(dir)
	all := make([]accountJSON, 0, len(list))
	for _, a := range list {
		all = append(all, describeAccount(a, users[a.Name], def == a.Name))
	}
	if st.JSON {
		v := any(all)
		if st.Object == grammar.Account {
			v = all[0]
		}
		if err := printJSON(v); err != nil {
			return err
		}
		return leftOut(bad)
	}
	if derr != nil {
		fmt.Fprintf(os.Stderr, "Warning: the DEFAULTS account cannot be read (%v); every launch is refused until ALTER DEFAULTS USE ACCOUNT … replaces it.\n", derr)
	}
	if st.Object == grammar.Account {
		v := all[0]
		yes := "no"
		if v.Default {
			yes = "yes"
		}
		var models []string
		for _, k := range account.ModelKeys {
			if m, ok := v.Models[k]; ok {
				models = append(models, k+" = "+m)
			}
		}
		printLabels([][2]string{
			{"Name", v.Name},
			{"Description", deref(v.Description, "(none)")},
			{"Provider", v.Provider},
			{"Address", deref(v.BaseURL, "(the provider's own)")},
			{"Identity", deref(v.Identity, "(none)")},
			{"Credential", authSummary(v)},
			{"Access", v.Access},
			{"Claude login trusted", fmt.Sprint(v.ClaudeLoginTrusted)},
			{"Models", listOrNone(models)},
			{"Headers", listOrNone(headerSummary(list[0]))},
			{"Used by", listOrNone(v.UsedBy)},
			{"Default", yes},
		})
		return leftOut(bad)
	}
	t := newTable("NAME", "PROVIDER", "ADDRESS", "CREDENTIAL", "ACCESS", "USED BY").flexible(5)
	for _, v := range all {
		name := v.Name
		if v.Default {
			name += " *"
		}
		used := strings.Join(v.UsedBy, ", ")
		if used == "" {
			used = "-"
		}
		t.add(name, v.Provider, deref(v.BaseURL, "-"), authSummary(v), v.Access, used)
	}
	t.render(os.Stdout)
	return leftOut(bad)
}

// headerSummary is each header's name and its reference: a reference is not
// a secret.
func headerSummary(a *account.Account) []string {
	var out []string
	for _, n := range a.HeaderNames() {
		out = append(out, n+" from "+a.Headers[n])
	}
	return out
}

// createAccountBlock is SHOW CREATE ACCOUNT: the statement that states the
// account whole. A literal credential is never printed: the account is
// left out, a comment naming it, and counted as withheld, so SHOW CREATE
// refuses at the end without --skip-secrets, as for an env set's literal.
func createAccountBlock(a *account.Account) createBlock {
	if a.Auth.Value != "" {
		return createBlock{text: fmt.Sprintf("-- ACCOUNT %s withheld: its %s is plain text; restate it with a reference (ALTER ACCOUNT %s SET %s FROM '<ref>')", a.Name, a.Auth.Kind, a.Name, a.Auth.Kind), withheld: 1}
	}
	set := []grammar.Var{{Key: "provider", Value: a.Provider}}
	add := func(k, v string) {
		if v != "" {
			set = append(set, grammar.Var{Key: k, Value: v})
		}
	}
	add("base_url", a.BaseURL)
	switch a.Auth.Kind {
	case account.AuthToken, account.APIKey, account.ClaudeToken:
		set = append(set, grammar.Var{Key: a.Auth.Kind, Ref: a.Auth.Ref})
	case account.Helper:
		set = append(set, grammar.Var{Key: account.Helper, Value: a.Auth.Helper})
	default:
		set = append(set, grammar.Var{Key: "auth", Value: a.Auth.Kind})
	}
	add("identity", a.Identity)
	if a.Listed() {
		add("access", account.AccessListed)
	}
	if a.ClaudeLoginTrusted {
		add("claude_login_trusted", "true")
	}
	for _, k := range account.ModelKeys {
		add(k, a.ModelOf(k))
	}
	for _, n := range a.HeaderNames() {
		set = append(set, grammar.Var{Key: account.HeaderKeyPrefix + n, Ref: a.Headers[n]})
	}
	add("description", a.Description)
	st := &grammar.Stmt{Verb: grammar.Create, Object: grammar.Account, Name: a.Name, OrReplace: true,
		Clauses: []grammar.Clause{{Kind: grammar.SetAccount, Settings: set}}}
	return createBlock{text: st.Pretty() + ";"}
}

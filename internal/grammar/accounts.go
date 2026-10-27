package grammar

import (
	"slices"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// An account's keys (SPEC.md, "CREATE / ALTER / DROP ACCOUNT"): CREATE
// ACCOUNT … SET k = 'v', … states the record, ALTER ACCOUNT … SET changes
// keys and RESET puts the optional ones back. The secret credentials take a
// reference, auth_token FROM '<ref>', or a literal with AS PLAINTEXT.
var accountPropertyTable = []playbookProperty{
	{key: "provider", kind: kindEnum, values: account.Providers},
	{key: "base_url", kind: kindString, check: checkBaseURL, placeholder: "<url>"},
	{key: "auth", kind: kindEnum, values: account.AuthValues},
	{key: account.AuthToken, kind: kindString, check: checkCredentialLiteral, placeholder: "<ref>"},
	{key: account.APIKey, kind: kindString, check: checkCredentialLiteral, placeholder: "<ref>"},
	{key: account.ClaudeToken, kind: kindString, check: checkCredentialLiteral, placeholder: "<ref>"},
	{key: account.Helper, kind: kindString, check: checkKeyHelper, placeholder: "<command>"},
	{key: "identity", kind: kindString, check: checkOneLine("identity"), placeholder: "<text>"},
	{key: "access", kind: kindEnum, values: []string{account.AccessAny, account.AccessListed}},
	{key: "claude_login_trusted", kind: kindBool},
	{key: "model", kind: kindString, check: checkAccountModel("model"), placeholder: "<model>"},
	{key: "opus_model", kind: kindString, check: checkAccountModel("opus_model"), placeholder: "<model>"},
	{key: "sonnet_model", kind: kindString, check: checkAccountModel("sonnet_model"), placeholder: "<model>"},
	{key: "haiku_model", kind: kindString, check: checkAccountModel("haiku_model"), placeholder: "<model>"},
	{key: "fable_model", kind: kindString, check: checkAccountModel("fable_model"), placeholder: "<model>"},
	{key: "description", kind: kindString, check: checkOneLine("description"), placeholder: "<text>"},
}

// accountCredentialKeys are the keys that state an account's one
// credential: a new one replaces the old.
var accountCredentialKeys = []string{"auth", account.AuthToken, account.APIKey, account.ClaudeToken, account.Helper}

// accountResettable are the keys RESET takes: the optional ones. Provider
// and the credential are the record itself.
var accountResettable = []string{"base_url", "identity", "access", "claude_login_trusted",
	"model", "opus_model", "sonnet_model", "haiku_model", "fable_model", "description"}

func checkBaseURL(v string) string {
	if _, err := account.ParseBaseURL(v); err != nil {
		return err.Error()
	}
	return ""
}

func checkCredentialLiteral(v string) string {
	if v == "" || strings.ContainsAny(v, "\r\n\x00") {
		return "a credential takes a reference, <key> FROM '<ref>', or one line AS PLAINTEXT"
	}
	return ""
}

func checkKeyHelper(v string) string {
	if strings.TrimSpace(v) == "" || strings.ContainsAny(v, "\r\n\x00") {
		return "helper takes one command line that prints the key: helper = '<command>'"
	}
	return ""
}

func checkOneLine(key string) func(string) string {
	return func(v string) string {
		if v == "" || strings.ContainsAny(v, "\r\n\x00") {
			return key + " takes one line of text (RESET " + key + " removes it)"
		}
		return ""
	}
}

func checkAccountModel(key string) func(string) string {
	return func(v string) string {
		if v == "" || strings.ContainsAny(v, " \t\r\n") {
			return key + " takes a model id, one word (RESET " + key + " removes it)"
		}
		return ""
	}
}

// accountClause reads one clause of CREATE or ALTER ACCOUNT.
func (p *parser) accountClause() (*Clause, *Error) {
	c := &Clause{Pos: p.pos()}
	switch p.kw(accountStarters...) {
	case "":
		return nil, p.unexpected()
	case "SET":
		c.Kind = SetAccount
		if err := p.playbookProperties(c, "SET", true); err != nil {
			return nil, err
		}
		if p.kw("AS") != "" {
			if p.kw("PLAINTEXT") == "" {
				return nil, p.fail("expected PLAINTEXT after AS")
			}
			c.Plaintext = true
		}
		// A credential is never stored as plain text by accident: it takes
		// a reference, or AS PLAINTEXT saying so. The error names the key
		// only.
		for _, v := range c.Settings {
			if slices.Contains(account.SecretKinds, v.Key) && v.Ref == "" && !c.Plaintext {
				return nil, errAt(c.Pos, v.Key+" holds a secret: "+v.Key+" FROM '<ref>' (needs a secret helper), or add AS PLAINTEXT after the list to store the literal knowingly")
			}
		}
		if c.Plaintext && !slices.ContainsFunc(c.Settings, func(v Var) bool {
			return slices.Contains(account.SecretKinds, v.Key) && v.Ref == ""
		}) {
			return nil, errAt(c.Pos, "AS PLAINTEXT applies to a literal auth_token, api_key or claude_token, and this SET has none")
		}
		return c, nil
	case "RESET":
		c.Kind = ResetAccount
		if err := p.playbookProperties(c, "RESET", false); err != nil {
			return nil, err
		}
		for _, v := range c.Settings {
			switch {
			case v.Key == "provider":
				return nil, errAt(c.Pos, "provider is the account itself: SET provider = '…' with its credential, or DROP ACCOUNT")
			case slices.Contains(accountCredentialKeys, v.Key):
				return nil, errAt(c.Pos, "an account always has one credential: SET another one to replace it ("+v.Key+" is not reset)")
			}
		}
		return c, nil
	}
	return nil, nil
}

// accountRef reads <credential> FROM '<ref>' inside an account's SET list,
// when the words at the cursor are one; it reports whether they were.
func (p *parser) accountRef(c *Clause) (bool, *Error) {
	if !p.account || p.atEnd() {
		return false, nil
	}
	t := p.toks[p.i]
	k := strings.TrimSuffix(t.Text, ",")
	if t.Quoted || !slices.Contains(account.SecretKinds, k) || p.i+1 >= len(p.toks) {
		return false, nil
	}
	if f := p.toks[p.i+1]; f.Quoted || !strings.EqualFold(f.Text, "FROM") {
		return false, nil
	}
	p.i += 2
	p.quiet = true
	if p.atEnd() {
		p.note("'<ref>'")
		return true, p.fail(k + " FROM needs '<ref>'")
	}
	r := p.toks[p.i]
	ref := r.Text
	if !r.Quoted || r.Comma {
		ref = strings.TrimSuffix(ref, ",")
	}
	if !manifest.LooksLikeRef(ref) {
		return true, errAt(r.Pos, "not a secret reference: use a scheme form such as 'keychain:<service>' or 'op://<vault>/<item>/<field>' (the value itself is never stored)")
	}
	p.i++
	c.Settings = append(c.Settings, Var{Key: k, Ref: ref})
	return true, nil
}

// accountList reads the rest of USE ACCOUNT <account>, … (USE ACCOUNT ”
// states the empty list).
func (p *parser) accountList(c *Clause) *Error {
	c.Kind = UseAccount
	if !p.atEnd() && (p.toks[p.i].Text == "" || (!p.toks[p.i].Quoted && p.toks[p.i].Text == "''")) {
		p.i++
		p.quiet = true
		if !p.atEnd() && !p.isStarter() {
			return p.fail("USE ACCOUNT '' is the empty list and takes no other account")
		}
		c.Names = nil
		return nil
	}
	names, err := p.list("USE ACCOUNT", "<account>", func(t Token) *Error { return checkName(Account, t, false) })
	c.Names = names
	return err
}

// useList reads the object after USE in a playbook or DEFAULTS clause: an
// env set list or an account list.
func (p *parser) useList(c *Clause) *Error {
	switch p.kw("ENV", "ACCOUNT") {
	case "ENV":
		return p.envListRest(c, "USE")
	case "ACCOUNT":
		return p.accountList(c)
	}
	return p.fail("USE takes ENV or ACCOUNT: USE ENV <env> …, or USE ACCOUNT <account>, …")
}

// validateAccount checks an account statement whole: what one clause at a
// time cannot see.
func validateAccount(s *Stmt) *Error {
	var credentials []string
	var moves []string
	provider := false
	for _, c := range s.Clauses {
		if c.Kind != SetAccount {
			continue
		}
		for _, v := range c.Settings {
			switch {
			case slices.Contains(accountCredentialKeys, v.Key):
				credentials = append(credentials, v.Key)
			case v.Key == "provider":
				provider = true
				moves = append(moves, v.Key)
			case v.Key == "base_url":
				moves = append(moves, v.Key)
			}
		}
	}
	if len(credentials) > 1 {
		return errAt(s.Pos, "an account has one credential, and this statement states "+strings.Join(credentials, " and "))
	}
	switch s.Verb {
	case Create:
		for _, c := range s.Clauses {
			if c.Kind == ResetAccount {
				return errAt(c.Pos, "CREATE states a new account whole; RESET is for ALTER ACCOUNT")
			}
		}
		if !provider {
			return errAt(s.Pos, "CREATE ACCOUNT needs provider = '"+strings.Join(account.Providers, "' | '")+"'")
		}
		if len(credentials) == 0 {
			return errAt(s.Pos, "CREATE ACCOUNT needs its credential: auth_token FROM '<ref>', api_key FROM '<ref>', claude_token FROM '<ref>', helper = '<command>', or auth = 'claude_login' | 'cloud' | 'none'")
		}
	case Alter:
		// Moving an account moves its credential: an address changed
		// without the credential is the key left behind.
		if len(moves) > 0 && len(credentials) == 0 {
			return errAt(s.Pos, "changing "+strings.Join(moves, " and ")+" moves where this account's credential goes: state the credential in the same statement (…, auth_token FROM '<ref>'), even when it stays the same")
		}
	}
	return nil
}

// accountWords renders an account clause.
func (c *Clause) accountWords() []string {
	if c.Kind == ResetAccount {
		return append([]string{"RESET"}, propertyWords(c, false)...)
	}
	w := []string{"SET"}
	for i, v := range c.Settings {
		last := i == len(c.Settings)-1
		var pw []string
		if v.Ref != "" {
			pw = []string{v.Key, "FROM", quote(v.Ref)}
		} else {
			pw = []string{v.Key, "=", quote(v.Value)}
		}
		if !last {
			pw[len(pw)-1] += ","
		}
		w = append(w, pw...)
	}
	if c.Plaintext {
		w = append(w, "AS", "PLAINTEXT")
	}
	return w
}

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// SHOW CREATE writes the statements that rebuild the state (docs/cli-grammar.md,
// "setup.cpb"). Every form it writes is safe to repeat, so APPLY of its own
// output changes nothing. Plain text never travels in the output: a
// credential-looking literal becomes a comment with the value withheld and
// the statement that would store it by reference, and SHOW CREATE then
// exits non-zero unless --skip-secrets.

// createBlock is the text for one object, and how many literals it withheld.
type createBlock struct {
	text     string
	withheld int
}

func showCreate(st *grammar.Stmt) error {
	playbooksDir := config.ResolvePlaybooksDir()
	dir := envprofile.Dir(playbooksDir)
	var blocks []createBlock
	switch st.Object {
	case grammar.Env:
		p, err := envprofile.Read(dir, st.Name)
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("no env set %q", st.Name)
		}
		blocks = append(blocks, createEnvBlock(p))
	case grammar.Playbook:
		pb, err := playbook.Require(playbooksDir, st.Name)
		if err != nil {
			return err
		}
		blocks = append(blocks, createPlaybookBlock(pb))
	default: // ALL: env sets, DEFAULTS, playbooks, so each statement finds what it names
		profiles, err := envprofile.List(dir)
		if err != nil {
			return err
		}
		for _, p := range profiles {
			blocks = append(blocks, createEnvBlock(p))
		}
		b, err := createDefaultsBlock(dir)
		if err != nil {
			return err
		}
		if b.text != "" {
			blocks = append(blocks, b)
		}
		pbs, err := playbook.Discover(playbooksDir)
		if err != nil {
			return err
		}
		for _, pb := range pbs {
			blocks = append(blocks, createPlaybookBlock(pb))
		}
	}
	withheld := 0
	texts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		texts = append(texts, b.text)
		withheld += b.withheld
	}
	fmt.Println(strings.Join(texts, "\n\n"))
	if withheld > 0 && !st.SkipSecrets {
		return fmt.Errorf("%d credential-looking literal(s) were withheld (the commented lines): store each with SET … FROM '<ref>', or pass --skip-secrets to accept the output without them", withheld)
	}
	return nil
}

// withheldLiteral reports whether a literal must not go into a setup file:
// the same test SHOW uses to redact it.
func withheldLiteral(key, value string) bool {
	return (manifest.LooksLikeSecretKey(key) && !manifest.PlainSetting(value)) || redactURLCredentials(value) != value
}

// varClauses turns one layer into SET, SET … FROM and BLOCK clauses, and
// comments for the literals it withholds. fix names the statement that
// would store a withheld key by reference.
func varClauses(set, refs map[string]string, unset []string, fix func(key string) string) ([]grammar.Clause, []string) {
	var clauses []grammar.Clause
	var comments []string
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lit := grammar.Clause{Kind: grammar.SetVar}
	for _, k := range keys {
		if withheldLiteral(k, set[k]) {
			comments = append(comments,
				fmt.Sprintf("-- SET %s=<withheld> AS PLAINTEXT  (a credential-looking literal never goes into a file)", k),
				"-- to keep it here, store it by reference: "+fix(k))
			continue
		}
		lit.Vars = append(lit.Vars, grammar.Var{Key: k, Value: set[k]})
	}
	if len(lit.Vars) > 0 {
		clauses = append(clauses, lit)
	}
	refKeys := make([]string, 0, len(refs))
	for k := range refs {
		refKeys = append(refKeys, k)
	}
	sort.Strings(refKeys)
	for _, k := range refKeys {
		clauses = append(clauses, grammar.Clause{Kind: grammar.SetRef, Vars: []grammar.Var{{Key: k, Ref: refs[k]}}})
	}
	if len(unset) > 0 {
		blocked := append([]string(nil), unset...)
		sort.Strings(blocked)
		clauses = append(clauses, grammar.Clause{Kind: grammar.BlockVar, Keys: blocked})
	}
	return clauses, comments
}

func createEnvBlock(p *envprofile.Profile) createBlock {
	st := &grammar.Stmt{Verb: grammar.Create, Object: grammar.Env, Name: p.Name, OrReplace: true}
	if p.Description != "" {
		st.Clauses = append(st.Clauses, grammar.Clause{Kind: grammar.Describe, Arg: p.Description})
	}
	clauses, comments := varClauses(p.Set, p.Refs, p.Unset, func(k string) string {
		return fmt.Sprintf("ALTER ENV %s SET %s FROM '<ref>'", p.Name, k)
	})
	st.Clauses = append(st.Clauses, clauses...)
	return createBlock{text: joinComments(comments, st.Pretty()+";"), withheld: len(comments) / 2}
}

func createDefaultsBlock(dir string) (createBlock, error) {
	names, err := envprofile.Defaults(dir)
	if err != nil {
		return createBlock{}, fmt.Errorf("DEFAULTS cannot be read: %w", err)
	}
	st := &grammar.Stmt{Verb: grammar.Alter, Object: grammar.Defaults}
	if len(names) > 0 {
		st.Clauses = append(st.Clauses, grammar.Clause{Kind: grammar.UseEnv, Names: names})
	}
	// Only the stored setting: CPB_SECRET_HELPER belongs to one process.
	if data, err := os.ReadFile(filepath.Join(dir, envprofile.SecretHelperFile)); err == nil {
		if cmd := strings.TrimSpace(string(data)); cmd != "" {
			st.Clauses = append(st.Clauses, grammar.Clause{Kind: grammar.SetHelper, Arg: cmd})
		}
	}
	if len(st.Clauses) == 0 {
		return createBlock{}, nil
	}
	return createBlock{text: st.Pretty() + ";"}, nil
}

func createPlaybookBlock(pb *playbook.Playbook) createBlock {
	st := &grammar.Stmt{Verb: grammar.Create, Object: grammar.Playbook, Name: pb.Name, IfNotExists: true}
	v := describePlaybook(pb)
	m := pb.Manifest
	switch {
	case v.Linked != nil:
		st.Clauses = append(st.Clauses, grammar.Clause{Kind: grammar.Link, Arg: *v.Linked})
	case v.Source != nil:
		st.Clauses = append(st.Clauses, grammar.Clause{Kind: grammar.From, Arg: v.Source.URL})
		if v.Source.Branch != nil {
			st.Clauses = append(st.Clauses, grammar.Clause{Kind: grammar.Branch, Arg: *v.Source.Branch})
		}
		if v.Source.Subdir != nil {
			st.Clauses = append(st.Clauses, grammar.Clause{Kind: grammar.Subdir, Arg: *v.Source.Subdir})
		}
	}
	if v.Launcher != nil {
		st.Clauses = append(st.Clauses, grammar.Clause{Kind: grammar.Alias, Arg: *v.Launcher})
	} else {
		st.Clauses = append(st.Clauses, grammar.Clause{Kind: grammar.NoAlias})
	}
	if v.Sandbox && v.Linked == nil {
		st.Clauses = append(st.Clauses, grammar.Clause{Kind: grammar.Sandbox})
	}
	text := st.Pretty() + ";"
	if m == nil || m.Env.Empty() {
		return createBlock{text: text}
	}
	if v.Linked != nil {
		return createBlock{text: text + "\n-- the environment of a linked playbook lives in the target's " + manifest.FileName}
	}
	alter := &grammar.Stmt{Verb: grammar.Alter, Object: grammar.Playbook, Name: pb.Name}
	if len(m.Env.Profiles) > 0 {
		alter.Clauses = append(alter.Clauses, grammar.Clause{Kind: grammar.UseEnv, Names: m.Env.Profiles})
	}
	clauses, comments := varClauses(m.Env.Set, m.Env.Refs, m.Env.Unset, func(k string) string {
		return fmt.Sprintf("ALTER PLAYBOOK %s SET VAR %s FROM '<ref>'", pb.Name, k)
	})
	alter.Clauses = append(alter.Clauses, clauses...)
	if len(alter.Clauses) > 0 {
		text += "\n\n" + alter.Pretty() + ";"
	}
	return createBlock{text: joinComments(comments, text), withheld: len(comments) / 2}
}

func joinComments(comments []string, text string) string {
	if len(comments) == 0 {
		return text
	}
	return strings.Join(comments, "\n") + "\n" + text
}

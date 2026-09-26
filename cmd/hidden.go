package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
)

// The pre-grammar commands (env, env-profile, create <name>, link, delete,
// rename, alias, dealias, list, info) are a hidden fallback
// (docs/reference/cli-grammar.md, "Pre-grammar commands: a hidden
// fallback"): hidden from help and completion, on their own code paths, and
// unchanged. The one addition is a hint on stderr, only when stderr is a
// terminal, naming the statement that does the same. Nothing reaches
// stdout, so scripts see no change.

func init() {
	// Statements never reach cobra, so its help names them itself.
	rootCmd.Long = `Manage isolated Claude Code instances.

State is changed and read with statements:

  cpb CREATE | ALTER | DROP   PLAYBOOK | ENV | DEFAULTS  <name> <clause> ...
  cpb SHOW [PLAYBOOKS | ENVS | DEFAULTS | PLAYBOOK <name> | ENV <name>] [--json]
  cpb SHOW CREATE { PLAYBOOK <name> | ENV <name> | ALL }
  cpb EXPLAIN PLAYBOOK <name> [--json]
  cpb APPLY <file> [<file> ...] [--dry-run] [--yes]

The grammar: https://github.com/ramazanpolat/claude-playbooks/blob/main/docs/reference/cli-grammar.md`
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if cmd.Hidden && isTerminal(os.Stderr) {
			fmt.Fprintf(os.Stderr, "(hidden command; the grammar form is: %s)\n", grammarForm(cmd, args))
		}
	}
}

// grammarForm is the statement for a hidden command's arguments, from the
// spec's migration table. Values are never echoed (K=<value>): what was
// typed may be a secret. Where no single statement fits, it names the
// reference.
func grammarForm(cmd *cobra.Command, args []string) string {
	// An operand the command line cannot carry as a statement word (a
	// keyword, a --flag-like word, shell metacharacters) has no one-line
	// form: the hint names the reference instead.
	sub := cmd.Name() == "env" || cmd.Name() == "env-profile"
	for i, a := range args {
		switch {
		case sub && i == 1: // the old subcommand word (set, use, ...)
		case strings.Contains(a, "="): // K=V: its value is masked where written
		case sub && i >= 2 && args[1] == "describe": // the text is never shown
		case !plainOperand(a):
			return hintReference
		}
	}
	flag := func(name string) string {
		if f := cmd.Flags().Lookup(name); f != nil && f.Changed {
			return f.Value.String()
		}
		return ""
	}
	launcher := func() string {
		if a := flag("alias"); a != "" {
			return " ALIAS " + a
		}
		if flag("no-alias") == "true" {
			return " NO ALIAS"
		}
		return ""
	}
	arg := func(i int, placeholder string) string {
		if i < len(args) {
			return args[i]
		}
		return placeholder
	}
	switch cmd.Name() {
	case "create":
		s := "cpb CREATE PLAYBOOK " + arg(0, "<name>") + launcher()
		if flag("sandbox") == "true" {
			s += " SANDBOX"
		}
		return s
	case "link":
		target := arg(0, "<target>")
		name := flag("name")
		if name == "" && len(args) > 0 {
			// As link derives it: from the absolute target (`link .`).
			if abs, err := filepath.Abs(target); err == nil {
				name = filepath.Base(abs)
			}
		}
		if name == "" || !plainOperand(name) {
			return hintReference
		}
		return "cpb CREATE PLAYBOOK " + name + " LINK " + target + launcher()
	case "delete":
		s := "cpb DROP PLAYBOOK " + arg(0, "<name>")
		if flag("yes") == "true" {
			s += " --yes"
		}
		return s
	case "rename":
		return "cpb ALTER PLAYBOOK " + arg(0, "<old>") + " RENAME TO " + arg(1, "<new>") + launcher()
	case "alias":
		switch {
		case flag("remove") == "true":
			return "cpb ALTER PLAYBOOK " + arg(0, "<name>") + " NO ALIAS"
		case len(args) == 2:
			return "cpb ALTER PLAYBOOK " + args[0] + " ALIAS " + args[1]
		case len(args) == 1:
			return "cpb SHOW PLAYBOOK " + args[0]
		}
		return "cpb SHOW PLAYBOOKS"
	case "dealias":
		return "cpb ALTER PLAYBOOK " + arg(0, "<name>") + " NO ALIAS"
	case "list":
		return "cpb SHOW PLAYBOOKS"
	case "info":
		return "cpb SHOW PLAYBOOK " + arg(0, "<name>")
	case "env":
		if len(args) == 0 {
			return hintReference // the listing has no one statement
		}
		if len(args) == 1 {
			return "cpb EXPLAIN PLAYBOOK " + args[0]
		}
		head := "cpb ALTER PLAYBOOK " + args[0]
		rest := args[2:]
		switch args[1] {
		case "set":
			return head + " SET VAR " + maskedPairs(rest)
		case "unset":
			return head + " BLOCK VAR " + maskedPairs(rest)
		case "clear":
			return head + " UNSET VAR " + maskedPairs(rest)
		case "use":
			var w []string
			for _, p := range rest {
				w = append(w, "ADD ENV "+p)
			}
			return head + " " + strings.Join(w, " ")
		case "unuse":
			return head + " DROP ENV " + strings.Join(rest, " ")
		}
	case "env-profile":
		if len(args) == 0 {
			return "cpb SHOW ENVS"
		}
		if len(args) == 1 {
			return "cpb SHOW ENV " + args[0]
		}
		p, rest := args[0], args[2:]
		switch args[1] {
		case "set":
			return "cpb ALTER ENV " + p + " SET " + maskedPairs(rest) + " (CREATE ENV when new)"
		case "unset":
			return "cpb ALTER ENV " + p + " BLOCK " + maskedPairs(rest)
		case "clear":
			return "cpb ALTER ENV " + p + " UNSET " + maskedPairs(rest)
		case "describe":
			return "cpb ALTER ENV " + p + " DESCRIBE '<text>'"
		case "default":
			return "cpb ALTER DEFAULTS USE ENV " + p
		case "undefault":
			return "cpb ALTER DEFAULTS DROP ENV " + p
		case "delete":
			return "cpb DROP ENV " + p
		}
	}
	return hintReference
}

const hintReference = `see docs/reference/cli-grammar.md, "Pre-grammar commands"`

// plainOperand reports whether a word can stand in a one-line statement as
// typed: not a keyword (a new name cannot be one on the command line), not
// flag-like, and free of anything the shell or the lexer treats specially.
// K=V words pass: their values are masked where they are written.
func plainOperand(w string) bool {
	if w == "" || grammar.IsKeyword(w) || strings.HasPrefix(w, "--") {
		return false
	}
	return !strings.ContainsAny(w, " \t\r\n'\"`$;&|<>()*?[]{}~!#\\")
}

// maskedPairs writes K=V words as K=<value>.
func maskedPairs(words []string) string {
	out := make([]string, 0, len(words))
	for _, w := range words {
		if k, _, ok := strings.Cut(w, "="); ok {
			w = k + "=<value>"
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

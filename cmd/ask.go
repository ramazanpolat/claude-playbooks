package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
)

// cpb ask "<request>": the request in plain words, answered with the cpb
// command or commands that do it (SPEC.md, "cpb ask"). The model proposes;
// cpb checks every step and shows it. cpb ask never runs a step: the user
// copies what they want to run.
var askCmd = &cobra.Command{
	Use:   `ask [<playbook>] [--json] [--no-history] [--model <model>] [--account <account>] "<request>"`,
	Short: "Ask in plain words for the cpb command that does it",
	Long: `ask answers a request in plain words with the cpb command or commands that do it.
It shows them; it never runs them.

  cpb ask "list my env sets"
  cpb ask "run playbook work on my router account"
  cpb ask work "how many of your sessions are live?"
  cpb ask --account work-claude "list my env sets"

Naming a playbook (or its launcher) first addresses the question to it: it
signs in the way a launch of that playbook would, and "you" means it. A
playbook on an Anthropic account that signs in with a Claude token asks with
that token. A playbook on an account that is not Anthropic's, or on one that
signs in with a key or a token of another kind, is refused.

It asks Claude through this machine's Claude Code login (claude -p), always on
Anthropic's API: no env set, DEFAULTS or account of yours applies to the question
unless --account names one.
What it sends, to Anthropic: the request, and the names of your playbooks,
launchers and env sets, with whether each routes away from Anthropic (a
playbook by the account it runs on). Never a
variable's value, a secret reference or a credential. The model's one tool is
cpb itself, read-only: SHOW and EXPLAIN, cut to names, routes, versions and
session counts.
CPB_ASK_LOGIN=token signs in with cpb's machine token (the long-lived token
every launch uses) instead of Claude Code's own login.

--account <account> signs the question in on one of your accounts instead,
as a launch on it would: an Anthropic account that signs in with a Claude
token (auth = claude_token) or a Claude login. Use it when your login has hit
its limit. With a playbook named, the account must be one it may run on.
Only the account's credential is used: its token reaches Claude Code's
environment alone, through your secret helper when it is a reference.

A request that holds a credential (KEY=value with a secret-looking key, a
token-shaped word, a URL with a password) is refused before anything is
sent: name the key instead, and leave the value out.

What it keeps, in <root>/.ask/: CLAUDE.md, your standing context in your own
words, which cpb only reads and sends with every ask; and history.md, one
record per answered ask, appended and never rewritten (delete it to forget).
Each ask sends the last 20 records' headings and the last record, and the
model can read further back. --no-history keeps one ask out of it.

A line marked ! says what a step removes, forgets or replaces, which
credential or login it touches, or what code it fetches: read it before you
run the step.

Exit codes: 0 shown; 1 refused, or no answer from Claude; 2 a usage error.`,
	Args: cobra.ArbitraryArgs,
	RunE: runAsk,
}

func init() {
	askCmd.Flags().Bool("json", false, "print the checked plan as JSON")
	askCmd.Flags().Bool("no-history", false, "neither read the ask history nor add this ask to it")
	askCmd.Flags().String("model", "", "the Claude model that answers (default: $CPB_ASK_MODEL, else "+defaultAskModel+")")
	askCmd.Flags().String("account", "", "sign in on this account: an Anthropic one with a Claude token or login (default: as the playbook would, else Claude Code's own login)")
	rootCmd.AddCommand(askCmd)
}

// askUsage reports a usage error: exit 2.
func askUsage(msg string) error {
	fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
	return &commandExitError{code: 2}
}

func runAsk(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	noHistory, _ := cmd.Flags().GetBool("no-history")
	model, _ := cmd.Flags().GetString("model")
	acctName, _ := cmd.Flags().GetString("account")
	root := config.ResolvePlaybooksDir()
	target, request, err := askSplitArgs(root, args)
	if err != nil {
		return err
	}
	if request == "" {
		return askUsage(`ask in plain words: cpb ask [<playbook>] "<request>"`)
	}
	if found := askRequestCredential(request); found != "" {
		return fmt.Errorf("the request looks like it holds a credential (%s): nothing was sent. Leave the value out and name it instead, e.g. <token>", found)
	}
	login, err := askLogin()
	if err != nil {
		return askUsage(err.Error())
	}
	signIn := askSignIn{mode: login}
	switch {
	case cmd.Flags().Changed("account"):
		// --account decides how the question signs in, with or without a
		// playbook named. Given empty (an unset variable) it is refused:
		// a login the person did not name never stands in for one they did.
		if signIn, err = askAccountSignIn(root, target, acctName); err != nil {
			return err
		}
	case target != nil:
		// The playbook decides how its question signs in; CPB_ASK_LOGIN is
		// for questions addressed to no playbook.
		if signIn, err = askTargetSignIn(root, target, ""); err != nil {
			return err
		}
	}
	if model == "" {
		model = os.Getenv("CPB_ASK_MODEL")
	}
	if model == "" {
		model = defaultAskModel
	}
	state, err := buildAskContext(root)
	if err != nil {
		return err
	}
	tag := ""
	if target != nil {
		state.Addressed = target.Name
		tag = target.Name
	}
	// The ask store: the user's standing context always, the history
	// unless --no-history.
	prelude := ""
	standing, err := askStanding(root)
	if err != nil {
		return err
	}
	if standing != "" {
		prelude += "Your standing context, the user's own words (.ask/CLAUDE.md):\n" + standing + "\n\n"
	}
	if !noHistory {
		past, err := askHistoryPrelude(root, tag)
		if err != nil {
			return err
		}
		if past != "" {
			prelude += past + "\n"
		}
	}
	plan, err := askViaClaude(model, signIn, root, state, prelude, request)
	if err != nil {
		return err
	}
	checkAskPlan(plan, root)
	if !noHistory {
		if id, err := appendAskRecord(root, tag, request, plan); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: this ask was not added to the history: %v\n", err)
		} else {
			plan.Record = id
		}
	}
	if asJSON {
		out, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	}
	cwd, _ := os.Getwd()
	printAskPlan(os.Stdout, plan, cwd)
	return nil
}

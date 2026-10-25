package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
)

// cpb ask "<request>": the request in plain words, answered with the cpb
// command or commands that do it (SPEC.md, "cpb ask"). The model proposes;
// cpb checks every step and shows it. cpb ask never runs a step: the user
// copies what they want to run.
var askCmd = &cobra.Command{
	Use:   `ask [--json] [--model <model>] "<request>"`,
	Short: "Ask in plain words for the cpb command that does it",
	Long: `ask answers a request in plain words with the cpb command or commands that do it.
It shows them; it never runs them.

  cpb ask "list my env sets"
  cpb ask "attach env set router to playbook work"

It asks Claude through this machine's Claude Code login (claude -p), always on
Anthropic's API: no env set, DEFAULTS or route of yours applies to the question.
What it sends, to Anthropic: the request, and the names of your playbooks,
launchers and env sets, with whether each routes away from Anthropic. Never a
variable's value, a secret reference or a credential. The model gets no tools.
CPB_ASK_LOGIN=token signs in with cpb's machine token (the long-lived token
every launch uses) instead of Claude Code's own login.

A line marked ! says what a step removes, forgets or replaces, which
credential or login it touches, or what code it fetches: read it before you
run the step.

Exit codes: 0 shown; 1 refused, or no answer from Claude; 2 a usage error.`,
	Args: cobra.ArbitraryArgs,
	RunE: runAsk,
}

func init() {
	askCmd.Flags().Bool("json", false, "print the checked plan as JSON")
	askCmd.Flags().String("model", "", "the Claude model that answers (default: $CPB_ASK_MODEL, else "+defaultAskModel+")")
	rootCmd.AddCommand(askCmd)
}

// askUsage reports a usage error: exit 2.
func askUsage(msg string) error {
	fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
	return &commandExitError{code: 2}
}

func runAsk(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	model, _ := cmd.Flags().GetString("model")
	request := strings.TrimSpace(strings.Join(args, " "))
	if request == "" {
		return askUsage(`ask in plain words: cpb ask "<request>"`)
	}
	login, err := askLogin()
	if err != nil {
		return askUsage(err.Error())
	}
	if model == "" {
		model = os.Getenv("CPB_ASK_MODEL")
	}
	if model == "" {
		model = defaultAskModel
	}
	root := config.ResolvePlaybooksDir()
	state, err := buildAskContext(root)
	if err != nil {
		return err
	}
	plan, err := askViaClaude(model, login, state, request)
	if err != nil {
		return err
	}
	checkAskPlan(plan, root)
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

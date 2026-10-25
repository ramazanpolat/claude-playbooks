package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
)

// cpb agent "<request>": the request in plain words, answered with the cpb
// command or commands that do it (SPEC.md, "cpb agent"). The model proposes;
// cpb checks every step, decides what always asks first, and runs nothing
// without --run and a yes.
var agentCmd = &cobra.Command{
	Use:   `agent [--run [--yes]] [--json] [--model <model>] "<request>"`,
	Short: "Ask in plain words for the cpb command that does it",
	Long: `agent answers a request in plain words with the cpb command or commands that do it.

  cpb agent "list my env sets"
  cpb agent --run "attach env set router to playbook work"

It asks Claude through this machine's Claude Code login (claude -p), always on
Anthropic's API: no env set, DEFAULTS or route of yours applies to the question.
What it sends, to Anthropic: the request, and the names of your playbooks,
launchers and env sets, with whether each routes away from Anthropic. Never a
variable's value, a secret reference or a credential. The model gets no tools.
CPB_AGENT_LOGIN=token signs in with cpb's machine token (the long-lived token
every launch uses) instead of Claude Code's own login.

Without --run it only shows the plan. --run shows it and asks before running
it. A step that removes, forgets or replaces something, touches a credential
or the login, or fetches code from a source always asks, --yes or not, and
never runs without a terminal.

Exit codes: 0 shown, or run with every step succeeding; 1 refused, declined,
or no answer from Claude; 2 a usage error. A failing step passes its own code.`,
	Args: cobra.ArbitraryArgs,
	RunE: runAgent,
}

func init() {
	agentCmd.Flags().Bool("run", false, "show the plan, then ask before running it")
	agentCmd.Flags().Bool("yes", false, "with --run: run a plan that has nothing that always asks, without asking")
	agentCmd.Flags().Bool("json", false, "print the checked plan as JSON (shows only)")
	agentCmd.Flags().String("model", "", "the Claude model that answers (default: $CPB_AGENT_MODEL, else "+defaultAgentModel+")")
	rootCmd.AddCommand(agentCmd)
}

// Seams for tests: the cpb that runs a step, whether a terminal can answer,
// and the answer.
var (
	agentSelf    = os.Executable
	agentTTY     = func() bool { return isTerminal(os.Stdin) && isTerminal(os.Stdout) }
	agentConfirm = confirm
)

// agentUsage reports a usage error: exit 2.
func agentUsage(msg string) error {
	fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
	return &commandExitError{code: 2}
}

// agentRefuse reports a refusal: exit 1.
func agentRefuse(msg string) error {
	fmt.Fprintln(os.Stderr, msg)
	return &commandExitError{code: 1}
}

func runAgent(cmd *cobra.Command, args []string) error {
	run, _ := cmd.Flags().GetBool("run")
	yes, _ := cmd.Flags().GetBool("yes")
	asJSON, _ := cmd.Flags().GetBool("json")
	model, _ := cmd.Flags().GetString("model")
	request := strings.TrimSpace(strings.Join(args, " "))
	switch {
	case request == "":
		return agentUsage(`ask in plain words: cpb agent "<request>"`)
	case yes && !run:
		return agentUsage("--yes answers --run's question: add --run")
	case asJSON && run:
		return agentUsage("--json only shows the plan: drop --run or --json")
	}
	login, err := agentLogin()
	if err != nil {
		return agentUsage(err.Error())
	}
	if model == "" {
		model = os.Getenv("CPB_AGENT_MODEL")
	}
	if model == "" {
		model = defaultAgentModel
	}
	root := config.ResolvePlaybooksDir()
	state, err := buildAgentContext(root)
	if err != nil {
		return err
	}
	plan, err := askAgent(model, login, state, request)
	if err != nil {
		return err
	}
	checkAgentPlan(plan, root)
	if asJSON {
		out, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	}
	cwd, _ := os.Getwd()
	if !run {
		printAgentPlan(os.Stdout, plan, cwd, "/tmp")
		return nil
	}
	return runAgentPlan(plan, yes, cwd)
}

// runAgentPlan shows the plan exactly as it will run, asks, and runs it.
func runAgentPlan(p *agentPlan, yes bool, cwd string) error {
	var guards []agentGuard
	away := false
	for _, s := range p.Steps {
		if s.Status != stepOK {
			printAgentPlan(os.Stdout, p, cwd, "/tmp")
			return agentRefuse("Nothing ran: a step above cannot run as proposed.")
		}
		guards = append(guards, s.Guards...)
		away = away || s.RouteAway
	}
	neutral := "/tmp"
	if away {
		dir, err := agentNeutralDir()
		if err != nil {
			return err
		}
		defer os.Remove(dir) // only if the session left it empty
		neutral = dir
	}
	printAgentPlan(os.Stdout, p, cwd, neutral)
	if len(p.Steps) == 0 {
		return nil
	}
	tty := agentTTY()
	switch {
	case len(guards) > 0 && !tty:
		return agentRefuse("Nothing ran: steps marked ! always ask first, and there is no terminal to ask on.")
	case !tty && !yes:
		return agentRefuse("Nothing ran: --run asks before running, and there is no terminal to ask on (--yes runs a plan with nothing marked !).")
	case len(guards) > 0 || !yes:
		fmt.Println()
		if !agentConfirm("Run it? [y/N] ") {
			return agentRefuse("Nothing ran.")
		}
	}
	self, err := agentSelf()
	if err != nil {
		return err
	}
	for i, s := range p.Steps {
		args := s.Args
		if config.PlaybooksDir != "" {
			args = append([]string{"--playbooks-dir", config.PlaybooksDir}, args...)
		}
		c := exec.Command(self, args...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if s.RouteAway {
			c.Dir = neutral
		}
		if err := runAttached(c); err != nil {
			ran := "no step ran before it"
			if i > 0 {
				ran = fmt.Sprintf("steps 1-%d ran", i)
				if i == 1 {
					ran = "step 1 ran"
				}
			}
			fmt.Fprintf(os.Stderr, "Step %d failed (%s); %s, and the rest did not.\n", i+1, s.Command, ran)
			return preserveExitCode(err)
		}
	}
	return nil
}

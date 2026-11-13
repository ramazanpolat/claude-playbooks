package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
)

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Act on the session this command runs in",
}

var (
	sessionLabelUnset   []string
	sessionLabelList    bool
	sessionLabelJSON    bool
	sessionLabelSession string
)

var sessionLabelCmd = &cobra.Command{
	Use:   "label [key=value...]",
	Short: "Set, remove or list the labels of the session this command runs in",
	Long: `Session labels are short key=value pairs a session carries, set from
inside it: by a hook (a SessionStart hook setting task=<name>) or a tool.
The session is found from this process's ancestry: the first ancestor that
is a claude session cpb launched. --session <id> names a live session
instead.

They reach the session's status line (the cpb key's labels.session),
SHOW SESSIONS and the usage log. A value is one line, at most 200 bytes,
and never an e-mail address or a credential.

Exit codes: 0 done; 1 refused; 3 not in a session cpb launched (a hook can
ignore exactly that).`,
	Args: cobra.ArbitraryArgs,
	RunE: runSessionLabel,
}

func init() {
	sessionLabelCmd.Flags().StringArrayVar(&sessionLabelUnset, "unset", nil, "remove a label (repeatable)")
	sessionLabelCmd.Flags().BoolVar(&sessionLabelList, "list", false, "print the session's labels")
	sessionLabelCmd.Flags().BoolVar(&sessionLabelJSON, "json", false, "with --list: one JSON object")
	sessionLabelCmd.Flags().StringVar(&sessionLabelSession, "session", "", "the session id, instead of the one this command runs in")
	sessionCmd.AddCommand(sessionLabelCmd)
	rootCmd.AddCommand(sessionCmd)
}

func runSessionLabel(cmd *cobra.Command, args []string) error {
	set := map[string]string{}
	for _, a := range args {
		k, v, err := parseTypedLabel(a)
		if err != nil {
			return err
		}
		set[k] = v
	}
	for _, k := range sessionLabelUnset {
		if p := labelKeyProblem(k); p != "" {
			return fmt.Errorf("--unset %q: %s", k, p)
		}
	}
	if len(set) == 0 && len(sessionLabelUnset) == 0 && !sessionLabelList {
		return errors.New("nothing to do: give key=value, --unset <key> or --list")
	}
	var f sessionFile
	var err error
	if sessionLabelSession != "" {
		f, err = liveSessionByID(sessionLabelSession)
	} else {
		f, err = callerSession()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "cpb session label: %v\n", err)
		return &commandExitError{code: exitNotASession}
	}
	root := config.ResolvePlaybooksDir()
	labels := readSessionLabels(root, f.SessionID)
	if len(set) > 0 || len(sessionLabelUnset) > 0 {
		if labels, err = updateSessionLabels(root, f.SessionID, set, sessionLabelUnset); err != nil {
			return err
		}
	}
	if !sessionLabelList {
		return nil
	}
	if sessionLabelJSON {
		if labels == nil {
			labels = map[string]string{}
		}
		b, _ := json.Marshal(struct {
			SessionID string            `json:"session_id"`
			Labels    map[string]string `json:"labels"`
		}{f.SessionID, labels})
		fmt.Println(string(b))
		return nil
	}
	for _, k := range sortedLabelKeys(labels) {
		fmt.Printf("%s=%s\n", k, labels[k])
	}
	return nil
}

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

var (
	authStatusJSON   bool
	authStatusClaude bool
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Inspect how playbooks authenticate",
	Long: `Read-only views of authentication state across playbooks. Nothing here
logs in, refreshes, or writes.`,
}

var authStatusCmd = &cobra.Command{
	Use:   "status [name...]",
	Short: "Show each playbook's authentication mode, stored login, expiry, and daemon hint",
	Long: `For every playbook (or the named ones), decide how a launch would
authenticate -- exactly as run does, without launching -- and report the
stored login next to it:

  MODE     token | own-token | own-login | shared-login | isolated | error
  STORE    what sits at .credentials.json: symlink -> target, file, or absent
  EXPIRES  the stored grant's expiry, "expired", or "-" when there is no grant
  DAEMON   Claude Code's daemon-auth-status.json, when it says re-auth is
           required for the CURRENT grant (an older marker is a healed one)
  NOTE     what needs attention, if anything

The global ~/.claude, owner of the shared store, is listed first. Nothing is
written, no credential value is read into the output, no network call is
made. --claude additionally runs 'claude auth status --json' per directory
(one process each) and adds its loggedIn and subscriptionType.`,
	ValidArgsFunction: autocompletePlaybookNames,
	RunE:              runAuthStatus,
}

func init() {
	authStatusCmd.Flags().BoolVar(&authStatusJSON, "json", false, "machine-readable output")
	authStatusCmd.Flags().BoolVar(&authStatusClaude, "claude", false, "also run 'claude auth status --json' for each directory")
	authCmd.AddCommand(authStatusCmd)
}

type authRow struct {
	auth.Report
	Claude *claudeAuth `json:"claude,omitempty"`
}

type claudeAuth struct {
	LoggedIn         bool   `json:"loggedIn"`
	SubscriptionType string `json:"subscriptionType,omitempty"`
	AuthMethod       string `json:"authMethod,omitempty"`
	Error            string `json:"error,omitempty"`
}

func runAuthStatus(cmd *cobra.Command, args []string) error {
	playbooksDir := config.ResolvePlaybooksDir()
	now := time.Now()
	var rows []authRow

	if len(args) == 0 {
		if global, err := auth.GlobalDir(); err == nil {
			rows = append(rows, authRow{Report: auth.InspectGlobal(global, now)})
		}
	}
	var pbs []*playbook.Playbook
	if len(args) == 0 {
		all, err := playbook.Discover(playbooksDir)
		if err != nil {
			return err
		}
		pbs = all
	} else {
		for _, name := range args {
			pb, err := playbook.Require(playbooksDir, name)
			if err != nil {
				return err
			}
			pbs = append(pbs, pb)
		}
	}
	for _, pb := range pbs {
		rows = append(rows, authRow{Report: auth.Inspect(pb.Name, pb.Path, now)})
	}
	if authStatusClaude {
		for i := range rows {
			rows[i].Claude = askClaude(rows[i].Dir)
		}
	}

	if authStatusJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	printAuthTable(rows, now)
	return nil
}

// askClaude runs `claude auth status --json` bound to dir. Errors are reported
// in the row, never fatal: a missing claude binary must not hide the rest.
func askClaude(dir string) *claudeAuth {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return &claudeAuth{Error: "claude not on PATH"}
	}
	c := exec.Command(claudePath, "auth", "status", "--json")
	c.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+dir)
	out, err := c.Output()
	if err != nil {
		return &claudeAuth{Error: strings.TrimSpace(err.Error())}
	}
	var got claudeAuth
	if err := json.Unmarshal(out, &got); err != nil {
		return &claudeAuth{Error: "unparsable output"}
	}
	return &got
}

func printAuthTable(rows []authRow, now time.Time) {
	headers := []string{"NAME", "MODE", "STORE", "EXPIRES", "DAEMON", "NOTE"}
	if len(rows) > 0 && rows[0].Claude != nil {
		headers = append(headers, "CLAUDE")
	}
	var table [][]string
	for _, r := range rows {
		store := string(r.Store)
		if r.Store == auth.StoreSymlink {
			store = "symlink -> " + shortenHome(r.StoreTarget)
		}
		if r.Store == auth.StoreFile && !r.HasGrant {
			store = "file (no grant)"
		}
		exp := "-"
		if r.HasGrant {
			switch {
			case r.ExpiresAt.IsZero():
				exp = "unknown"
			case r.Expired:
				exp = "expired"
			default:
				exp = "in " + formatDuration(r.ExpiresAt.Sub(now))
			}
		}
		daemon := "-"
		if r.DaemonStatus != "" {
			if r.ReauthRequired {
				daemon = "auth_required"
			} else {
				daemon = r.DaemonStatus + " (stale)"
			}
		}
		note := r.NeedsAttention()
		if r.Mode == auth.ModeError {
			note = r.ModeError
		}
		row := []string{r.Name, string(r.Mode), store, exp, daemon, note}
		if r.Claude != nil {
			c := "-"
			switch {
			case r.Claude.Error != "":
				c = "error: " + r.Claude.Error
			case r.Claude.LoggedIn:
				c = "logged in"
				if r.Claude.SubscriptionType != "" {
					c += ", " + r.Claude.SubscriptionType
				}
			default:
				c = "not logged in"
			}
			row = append(row, c)
		}
		table = append(table, row)
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range table {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	line := func(cells []string) {
		var b strings.Builder
		for i, c := range cells {
			if i > 0 {
				b.WriteString("  ")
			}
			if i == len(cells)-1 {
				b.WriteString(c)
			} else {
				fmt.Fprintf(&b, "%-*s", widths[i], c)
			}
		}
		fmt.Println(strings.TrimRight(b.String(), " "))
	}
	line(headers)
	for _, row := range table {
		line(row)
	}
	if tf := firstTokenFile(rows); tf != "" {
		fmt.Printf("\nLong-lived token file: %s (its own expiry is not recorded anywhere; a lapse shows up as 401s in token-mode playbooks)\n", shortenHome(tf))
	}
}

func firstTokenFile(rows []authRow) string {
	for _, r := range rows {
		if r.TokenFile != "" {
			return r.TokenFile
		}
	}
	return ""
}

func shortenHome(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Minute)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	switch {
	case h >= 48:
		return fmt.Sprintf("%dd%dh", h/24, h%24)
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

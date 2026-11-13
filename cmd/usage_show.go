package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// SHOW USAGE (SPEC.md, "SHOW USAGE"): per allowance, how much is used.

// usageWindowJSON is one window of a row.
type usageWindowJSON struct {
	UsedPercentage float64 `json:"used_percentage"`
	LeftPercentage float64 `json:"left_percentage"`
	ResetsAt       string  `json:"resets_at"`
	// Reset is true once resets_at has passed: the figure is the last one
	// of a window that has started again.
	Reset bool `json:"reset"`
}

type seenByJSON struct {
	SessionID string `json:"session_id"`
	Playbook  string `json:"playbook"`
}

// usageJSON is one row of SHOW USAGE --json and of the USAGE table.
type usageJSON struct {
	Account         string           `json:"account"`
	Accounts        []string         `json:"accounts"`
	Identity        *string          `json:"identity"`
	Playbooks       []string         `json:"playbooks"`
	LiveSessions    int              `json:"live_sessions"`
	SessionsAtLimit int              `json:"sessions_at_limit"`
	State           string           `json:"state"`
	FiveHour        *usageWindowJSON `json:"five_hour"`
	SevenDay        *usageWindowJSON `json:"seven_day"`
	SpendLimit      json.RawMessage  `json:"spend_limit"`
	SeenAt          *string          `json:"seen_at"`
	SeenBy          *seenByJSON      `json:"seen_by"`
}

func windowJSON(w *usageWindow, now time.Time) *usageWindowJSON {
	if w == nil {
		return nil
	}
	return &usageWindowJSON{UsedPercentage: w.UsedPercentage, LeftPercentage: 100 - w.UsedPercentage,
		ResetsAt: rfc3339(time.Unix(w.ResetsAt, 0)), Reset: w.ResetsAt <= now.Unix()}
}

// loginIdentityOf is the identity of a login key, read now: the machine's
// login from its own state file, a playbook's from the playbook's.
func loginIdentityOf(root string) func(string) *string {
	return func(key string) *string {
		if key == "machine" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil
			}
			return loginIdentity(home)
		}
		name, ok := strings.CutPrefix(key, "playbook:")
		if !ok {
			return nil
		}
		pb, err := playbook.Find(root, name)
		if err != nil || pb == nil {
			return nil
		}
		return loginIdentity(pb.Path)
	}
}

// usageRowsNow is SHOW USAGE's rows, and the playbooks left out because
// their manifests cannot be read.
func usageRowsNow(forAccount string) ([]usageJSON, []playbook.Unreadable, error) {
	root := config.ResolvePlaybooksDir()
	sessions, bad, err := liveSessions("")
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	rows := usageRows(root, readLatestFigures(root), usageRowsOpts{now: now, sessions: sessions, loginIdentity: loginIdentityOf(root)})
	out := []usageJSON{}
	for _, r := range rows {
		if forAccount != "" && !contains(r.accounts, forAccount) {
			continue
		}
		v := usageJSON{Account: r.name, Accounts: append([]string{}, r.accounts...), Identity: r.identity,
			Playbooks: append([]string{}, r.playbooks...), LiveSessions: r.live, SessionsAtLimit: r.atLimit, State: r.state(),
			FiveHour: windowJSON(r.fiveHour, now), SevenDay: windowJSON(r.sevenDay, now), SpendLimit: r.spendLimit}
		if !r.seenAt.IsZero() {
			s := rfc3339(r.seenAt)
			v.SeenAt = &s
			v.SeenBy = &seenByJSON{SessionID: r.seenBy.SessionID, Playbook: r.seenBy.Playbook}
		}
		out = append(out, v)
	}
	return out, bad, nil
}

func usageTableRows() ([]any, []playbook.Unreadable, error) {
	rows, bad, err := usageRowsNow("")
	if err != nil {
		return nil, nil, err
	}
	out := make([]any, len(rows))
	for i, r := range rows {
		out[i] = r
	}
	return out, bad, nil
}

// usedCell and resetCell are a window's human form.
func usedCell(w *usageWindowJSON) string {
	if w == nil {
		return "-"
	}
	return strconv.FormatFloat(w.UsedPercentage, 'f', -1, 64) + "%"
}

func resetCell(w *usageWindowJSON, now time.Time) string {
	if w == nil {
		return "-"
	}
	if w.Reset {
		return "reset"
	}
	t, err := time.Parse(time.RFC3339, w.ResetsAt)
	if err != nil {
		return "-"
	}
	d := t.Sub(now).Round(time.Minute)
	if d < 24*time.Hour {
		return "in " + strings.TrimSuffix(d.String(), "0s")
	}
	days := int(d.Hours()) / 24
	return "in " + strconv.Itoa(days) + "d " + strconv.Itoa(int(d.Hours())-days*24) + "h"
}

func showUsage(st *grammar.Stmt) error {
	rows, bad, err := usageRowsNow(st.Name)
	if err != nil {
		return err
	}
	if st.Name != "" && len(rows) == 0 {
		return fmt.Errorf("no account %s feeds any allowance cpb knows of (cpb SHOW ACCOUNTS lists the accounts)", st.Name)
	}
	if st.JSON {
		if err := printJSON(rows); err != nil {
			return err
		}
		return leftOut(bad)
	}
	if len(rows) == 0 {
		fmt.Println("No accounts and no usage recorded yet.")
		return leftOut(bad)
	}
	now := time.Now()
	t := newTable("ACCOUNT", "SESSIONS", "AT LIMIT", "5H USED", "5H RESETS", "7D USED", "7D RESETS", "SEEN")
	for _, r := range rows {
		name := r.Account
		if len(r.Accounts) > 1 {
			name += " (" + strings.Join(r.Accounts, ", ") + ")"
		}
		switch r.State {
		case "not_reported":
			t.add(name, strconv.Itoa(r.LiveSessions), strconv.Itoa(r.SessionsAtLimit), "not reported", "", "", "", "")
			continue
		case "not_seen":
			t.add(name, strconv.Itoa(r.LiveSessions), strconv.Itoa(r.SessionsAtLimit), "not seen", "", "", "", "")
			continue
		}
		t.add(name, strconv.Itoa(r.LiveSessions), strconv.Itoa(r.SessionsAtLimit), usedCell(r.FiveHour), resetCell(r.FiveHour, now),
			usedCell(r.SevenDay), resetCell(r.SevenDay, now), ageOf(r.SeenAt)+" ago")
	}
	t.render(os.Stdout)
	return leftOut(bad)
}

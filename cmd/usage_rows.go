package cmd

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
)

// Usage rows (SPEC.md, "SHOW USAGE"): one row per allowance cpb knows of,
// built from cpb's own files only: the stored accounts, the latest figure
// per usage key (usage_log.go) and, for SHOW USAGE, the live sessions. The
// status line block (statusline_step.go) is the same rows, names only.

// usageWindow is one window of a figure: as Claude Code reported it, its
// used percentage (a number, normally 0 to 100, never clamped) and when it
// resets. A window that has reset keeps its last figure.
type usageWindow struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"`
}

// usageRow is one row.
type usageRow struct {
	name       string // the account name the row shows: names only
	keys       []string
	accounts   []string
	identity   *string
	claude     bool // a Claude login or token: a figure is expected
	fiveHour   *usageWindow
	sevenDay   *usageWindow
	spendLimit json.RawMessage
	seenAt     time.Time
	seenBy     latestFigure
	self       bool
	live       int
	atLimit    int
	playbooks  []string
}

// state says what the row's figure is: reported, not_seen (a Claude login
// or token no session has reported for yet), or not_reported (a credential
// whose provider reports none: never shown as 0).
func (r usageRow) state() string {
	switch {
	case r.fiveHour != nil || r.sevenDay != nil || len(r.spendLimit) > 0:
		return "reported"
	case r.claude:
		return "not_seen"
	}
	return "not_reported"
}

// usageRowsOpts says what a caller adds to the rows: the key of the session
// that asks (the status line block marks it and puts it first), and the
// live sessions (SHOW USAGE).
type usageRowsOpts struct {
	now      time.Time
	selfKey  string
	sessions []sessionJSON
	// loginIdentity reads a login's identity at statement time (SHOW
	// USAGE); nil leaves the one recorded at launch.
	loginIdentity func(key string) *string
}

// blockName is a name the status line block can carry: lower case
// letters, digits and ._:@- , at most 64 characters, starting with a
// letter or digit. Anything else is lowered, its other characters made -,
// and cut.
var blockNameShape = regexp.MustCompile(`^[a-z0-9][a-z0-9._:@-]{0,63}$`)

func blockName(s string) string {
	if blockNameShape.MatchString(s) {
		return s
	}
	b := []byte(strings.ToLower(s))
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("._:@-", c) >= 0) {
			b[i] = '-'
		}
	}
	out := strings.TrimLeft(string(b), "._:@-")
	if len(out) > 64 {
		out = out[:64]
	}
	if out == "" {
		out = "account"
	}
	return out
}

// windowsOf reads the windows of a rate_limits object.
func windowsOf(raw json.RawMessage) (five, seven *usageWindow, spend json.RawMessage) {
	var rl map[string]json.RawMessage
	if !rawField(raw, &rl) {
		return nil, nil, nil
	}
	win := func(k string) *usageWindow {
		var w struct {
			Used   *float64 `json:"used_percentage"`
			Resets *float64 `json:"resets_at"`
		}
		if !rawField(rl[k], &w) || w.Used == nil || w.Resets == nil || math.IsNaN(*w.Used) || math.IsNaN(*w.Resets) ||
			*w.Resets < 0 || *w.Resets > 1e11 {
			return nil
		}
		return &usageWindow{UsedPercentage: math.Max(0, math.Min(*w.Used, 1000)), ResetsAt: int64(*w.Resets)}
	}
	return win("five_hour"), win("seven_day"), capRaw(rl["spend_limit"])
}

// usageRows builds the rows, by name.
func usageRows(root string, latest map[string]latestFigure, opts usageRowsOpts) []usageRow {
	byKey := map[string]*usageRow{}
	order := []string{}
	add := func(key string) *usageRow {
		if r, ok := byKey[key]; ok {
			return r
		}
		r := &usageRow{name: blockName(key), keys: []string{key}}
		byKey[key] = r
		order = append(order, key)
		return r
	}
	// Every stored account with a credential of its own is an allowance.
	stored, _ := account.List(account.Dir(root))
	for _, a := range stored {
		if a == nil || a.Auth.Kind == account.ClaudeLogin {
			continue
		}
		r := add(a.Name)
		r.accounts = []string{a.Name}
		r.claude = a.Auth.Kind == account.ClaudeToken
		if id := strings.TrimSpace(a.Identity); id != "" {
			r.identity = &id
		}
	}
	// Every key a session reported for, and every key a live session draws
	// on, in key order: a row's name never depends on map order.
	latestKeys := make([]string, 0, len(latest))
	for key := range latest {
		latestKeys = append(latestKeys, key)
	}
	sort.Strings(latestKeys)
	for _, key := range latestKeys {
		l := latest[key]
		r := add(key)
		if !contains(r.accounts, l.Account) {
			r.accounts = append(r.accounts, l.Account)
		}
		r.claude = r.claude || isLoginKey(key)
		r.fiveHour, r.sevenDay, r.spendLimit = windowsOf(l.RateLimits)
		if t, err := time.Parse(time.RFC3339Nano, l.SeenAt); err == nil {
			r.seenAt, r.seenBy = t, l
		}
	}
	for _, s := range opts.sessions {
		if s.key == "" {
			continue
		}
		r := add(s.key)
		r.claude = r.claude || isLoginKey(s.key)
		if s.Account != nil && !contains(r.accounts, *s.Account) {
			r.accounts = append(r.accounts, *s.Account)
		}
		r.live++
		if s.AtLimit != nil {
			r.atLimit++
		}
		if !contains(r.playbooks, s.Playbook) {
			r.playbooks = append(r.playbooks, s.Playbook)
		}
	}
	if opts.loginIdentity != nil {
		for key, r := range byKey {
			if isLoginKey(key) {
				if id := opts.loginIdentity(key); id != nil {
					r.identity = id
				}
			}
		}
	}
	// One row per real login: keys that name the same identity are one.
	var rows []usageRow
	byIdentity := map[string]int{}
	for _, key := range order {
		r := *byKey[key]
		r.self = key == opts.selfKey
		if r.identity != nil {
			if i, ok := byIdentity[*r.identity]; ok {
				rows[i] = mergeRows(rows[i], r)
				continue
			}
			byIdentity[*r.identity] = len(rows)
		}
		rows = append(rows, r)
	}
	for i := range rows {
		sort.Strings(rows[i].accounts)
		sort.Strings(rows[i].playbooks)
		if len(rows[i].accounts) > 0 && !isLoginKey(rows[i].keys[0]) {
			rows[i].name = blockName(rows[i].accounts[0])
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
	return rows
}

// mergeRows is two rows of one identity as one: the newer figure, every
// account, playbook and session.
func mergeRows(a, b usageRow) usageRow {
	if b.seenAt.After(a.seenAt) {
		a.fiveHour, a.sevenDay, a.spendLimit, a.seenAt, a.seenBy = b.fiveHour, b.sevenDay, b.spendLimit, b.seenAt, b.seenBy
	}
	a.keys = append(a.keys, b.keys...)
	for _, n := range b.accounts {
		if !contains(a.accounts, n) {
			a.accounts = append(a.accounts, n)
		}
	}
	for _, p := range b.playbooks {
		if !contains(a.playbooks, p) {
			a.playbooks = append(a.playbooks, p)
		}
	}
	a.self = a.self || b.self
	a.claude = a.claude || b.claude
	a.live += b.live
	a.atLimit += b.atLimit
	return a
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// isLoginKey reports a usage key of a Claude login: the machine's, or a
// playbook's own.
func isLoginKey(key string) bool {
	return key == account.Machine || strings.HasPrefix(key, "playbook:")
}

// usageBlockRow is one entry of the status line block's usage: names only.
type usageBlockRow struct {
	Account  string       `json:"account"`
	Self     bool         `json:"self"`
	State    string       `json:"state"`
	FiveHour *usageWindow `json:"five_hour"`
	SevenDay *usageWindow `json:"seven_day"`
	SeenAt   *int64       `json:"seen_at"`
}

func (r usageRow) block() usageBlockRow {
	b := usageBlockRow{Account: r.name, Self: r.self, State: r.state(), FiveHour: r.fiveHour, SevenDay: r.sevenDay}
	if !r.seenAt.IsZero() {
		t := r.seenAt.Unix()
		b.SeenAt = &t
	}
	return b
}

// fullKeys lists the usage keys whose figure says they are at a limit now:
// a window at 100 or more that resets later than now.
func fullKeys(latest map[string]latestFigure, now time.Time) map[string]bool {
	out := map[string]bool{}
	for key, l := range latest {
		five, seven, _ := windowsOf(l.RateLimits)
		for _, w := range []*usageWindow{five, seven} {
			if w != nil && w.UsedPercentage >= 100 && w.ResetsAt > now.Unix() {
				out[key] = true
			}
		}
	}
	return out
}

// accountLimitOf is the limit a latest figure says its key is at now: the
// window at 100 or more that resets soonest after now; nil when none is.
func accountLimitOf(l latestFigure, now time.Time) *sessionLimit {
	five, seven, _ := windowsOf(l.RateLimits)
	var best *sessionLimit
	var bestAt int64
	for _, c := range []struct {
		name string
		w    *usageWindow
	}{{"five_hour", five}, {"seven_day", seven}} {
		if c.w == nil || c.w.UsedPercentage < 100 || c.w.ResetsAt <= now.Unix() {
			continue
		}
		if best == nil || c.w.ResetsAt < bestAt {
			best, bestAt = &sessionLimit{Window: c.name, ResetsAt: rfc3339(time.Unix(c.w.ResetsAt, 0))}, c.w.ResetsAt
		}
	}
	return best
}

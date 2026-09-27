package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/settings"
)

// A status line host (SPC/1: statusmux) owns the one statusLine slot and
// composes the bar from panels. A recipe that sets its own status line over
// a host would unwire it silently, and its observers (a lease heartbeat)
// would stop. So SET STATUSLINE never replaces a host's command: the slot is
// left as it is, the statement warns, and UNSET STATUSLINE stays the
// explicit way to take the slot back.

// isHostCommand reports whether a statusLine command is a host's: its first
// word, after env, exec and VAR=value words, is a program named statusmux,
// and its second is render. The same rule statusmux itself uses.
func isHostCommand(cmd string) bool {
	fields := strings.Fields(cmd)
	i := 0
	for i < len(fields) {
		f := fields[i]
		if f == "exec" || f == "env" {
			i++
			continue
		}
		if eq := strings.IndexByte(f, '='); eq > 0 && !strings.ContainsAny(f[:eq], "\"'/$") {
			i++
			continue
		}
		break
	}
	if i+1 >= len(fields) {
		return false
	}
	bin := strings.Trim(fields[i], `"'`)
	return filepath.Base(bin) == "statusmux" && strings.Trim(fields[i+1], `"'`) == "render"
}

// statuslineHeld is the warning for a statement whose SET STATUSLINE a host
// keeps from applying; "" when none does.
func statuslineHeld(root *settings.Object, clauses []grammar.Clause, target string) string {
	sl, err := root.Object(keyStatusline)
	if err != nil {
		return ""
	}
	var typ, cmd string
	_, _ = sl.Get("type", &typ)
	_, _ = sl.Get("command", &cmd)
	if typ != "command" || !isHostCommand(cmd) {
		return ""
	}
	for _, c := range clauses {
		if c.Kind == grammar.SetStatusline && c.Arg != cmd {
			return fmt.Sprintf("%s: the status line is held by a host (statusmux); SET STATUSLINE left it as it is. Contribute this bar as a panel instead, or run UNSET STATUSLINE first to take the slot back", target)
		}
	}
	return ""
}

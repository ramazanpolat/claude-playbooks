package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/settings"
)

// The status line history: every status line a cpb statement replaces or
// removes, kept so SET STATUSLINE PREVIOUS can put it back. It lives in
// cpb's own state, <playbooks root>/.state/statusline-history.json, keyed by
// the config directory, so it works for playbooks and plain directories
// alike and never touches a playbook's own files. It is state, not
// configuration: SHOW CREATE never writes it.

// slHistoryMax entries are kept per directory; the oldest go first.
const slHistoryMax = 10

// slEntry is one replaced status line: the whole statusLine object, as it
// was written, and when it was replaced.
type slEntry struct {
	ReplacedAt string          `json:"replaced_at"`
	StatusLine json.RawMessage `json:"status_line"`
}

func slHistoryPath() string {
	return filepath.Join(config.ResolvePlaybooksDir(), ".state", "statusline-history.json")
}

func readSLHistoryFile() (map[string][]slEntry, error) {
	data, err := os.ReadFile(slHistoryPath())
	if errors.Is(err, os.ErrNotExist) {
		return map[string][]slEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	all := map[string][]slEntry{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &all); err != nil {
			return nil, fmt.Errorf("%s: %w", slHistoryPath(), err)
		}
	}
	return all, nil
}

// slHistory is a directory's history as the run sees it, newest first.
func (r *stmtRun) slHistory(key string) ([]slEntry, error) {
	if r.dry != nil {
		if h, ok := r.dry.slHistory[key]; ok {
			return slices.Clone(h), nil
		}
	}
	all, err := readSLHistoryFile()
	if err != nil {
		return nil, err
	}
	return all[key], nil
}

// commitSLHistory records a directory's history: kept in a dry run,
// written (0600, atomically) otherwise.
func (r *stmtRun) commitSLHistory(key string, h []slEntry) error {
	if r.dry != nil {
		r.dry.slHistory[key] = slices.Clone(h)
		return nil
	}
	all, err := readSLHistoryFile()
	if err != nil {
		return err
	}
	if len(h) == 0 {
		delete(all, key)
	} else {
		all[key] = h
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(slHistoryPath()), 0o700); err != nil {
		return err
	}
	return settings.WriteAtomic(slHistoryPath(), append(data, '\n'), 0o600)
}

// slPlan is what one statement does to a directory's status line history.
type slPlan struct {
	key     string
	restore *settings.Object // SET STATUSLINE PREVIOUS: the object put back
	base    []slEntry        // the history with that entry taken off
	after   []slEntry        // the history once the statement is done
	changed bool             // after differs from what was recorded
}

// planSLHistory resolves SET STATUSLINE PREVIOUS against a directory's
// history. It is refused when there is nothing to go back to.
func (r *stmtRun) planSLHistory(key, target string, clauses []grammar.Clause) (*slPlan, error) {
	h, err := r.slHistory(key)
	if err != nil {
		return nil, err
	}
	p := &slPlan{key: key, base: h, after: h}
	for _, c := range clauses {
		if c.Kind != grammar.SetStatuslinePrevious {
			continue
		}
		if len(h) == 0 {
			return nil, fmt.Errorf("SET STATUSLINE PREVIOUS: no earlier status line is recorded for %s (cpb records one each time a statement replaces or removes it)", target)
		}
		if p.restore, err = settings.ParseObject(h[0].StatusLine); err != nil {
			return nil, fmt.Errorf("SET STATUSLINE PREVIOUS: the recorded status line cannot be read: %w", err)
		}
		p.base = slices.Clone(h[1:])
	}
	return p, nil
}

// statuslineCommand is a statusLine object's command, "" without one.
func statuslineCommand(raw json.RawMessage) string {
	var sl struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(raw, &sl)
	return sl.Command
}

// applySettingsWithHistory is applySettings plus PREVIOUS and the history:
// the status line before the statement is pushed when the statement changes
// its command or removes it. record false re-applies (after plugin commands
// rewrote settings.json) without recomputing the history.
func applySettingsWithHistory(f *settings.File, clauses []grammar.Clause, p *slPlan, record bool) ([]string, bool, error) {
	before := slices.Clone(f.Root.Raw(keyStatusline))
	lines, changed, err := applySettings(f, clauses)
	if err != nil || p == nil {
		return lines, changed, err
	}
	if p.restore != nil {
		restored, _ := p.restore.MarshalJSON()
		if !bytes.Equal(compactJSON(before), compactJSON(restored)) {
			sub, _ := settings.ParseObject(restored)
			f.Root.SetObject(keyStatusline, sub)
			changed = true
			lines = append(lines, "statusline previous: "+statuslineCommand(restored))
		}
	}
	if !record {
		return lines, changed, nil
	}
	after := f.Root.Raw(keyStatusline)
	h := p.base
	if p.restore == nil {
		h = p.after
	}
	if len(before) > 0 && (len(after) == 0 || statuslineCommand(before) != statuslineCommand(after)) {
		h = append([]slEntry{{ReplacedAt: time.Now().UTC().Format(time.RFC3339), StatusLine: before}}, h...)
	}
	if len(h) > slHistoryMax {
		h = h[:slHistoryMax]
	}
	p.changed = changed && !slEqual(h, p.after)
	p.after = h
	return lines, changed, nil
}

func slEqual(a, b []slEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ReplacedAt != b[i].ReplacedAt || !bytes.Equal(compactJSON(a[i].StatusLine), compactJSON(b[i].StatusLine)) {
			return false
		}
	}
	return true
}

// slHistoryJSON is a directory's history for SHOW --json, newest first.
type slHistoryJSON struct {
	Command    string `json:"command"`
	Refresh    *int   `json:"refresh"`
	ReplacedAt string `json:"replaced_at"`
}

func describeSLHistory(dir string) []slHistoryJSON {
	out := []slHistoryJSON{}
	all, err := readSLHistoryFile()
	if err != nil {
		return out
	}
	key, _ := filepath.Abs(dir)
	for _, e := range all[key] {
		var sl struct {
			Command string `json:"command"`
			Refresh *int   `json:"refreshInterval"`
		}
		_ = json.Unmarshal(e.StatusLine, &sl)
		out = append(out, slHistoryJSON{Command: sl.Command, Refresh: sl.Refresh, ReplacedAt: e.ReplacedAt})
	}
	return out
}

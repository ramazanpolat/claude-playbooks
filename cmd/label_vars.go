package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
	"github.com/ramazanpolat/claude-playbooks/internal/tomlfile"
)

// DEFAULTS' label_vars live in <env sets dir>/.label_vars, TOML (0600):
// the names every launch copies into launch labels, before the
// playbook's own (SPEC.md, "Session labels").
const labelVarsDefaultsFile = ".label_vars"

type labelVarsDefaults struct {
	Names []string `toml:"names"`
}

func labelVarsDefaultsPath(root string) string {
	return filepath.Join(envset.Dir(root), labelVarsDefaultsFile)
}

// readLabelVarsDefaults reads DEFAULTS' label_vars; none is nil. A name a
// statement would refuse (a file edited by hand) is read, and left out at
// launch with a note.
func readLabelVarsDefaults(root string) ([]string, error) {
	path := labelVarsDefaultsPath(root)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var d labelVarsDefaults
	if err := tomlfile.Decode(path, b, &d); err != nil {
		return nil, err
	}
	return d.Names, nil
}

// writeLabelVarsDefaults stores names, or removes the file when there are
// none.
func writeLabelVarsDefaults(root string, names []string) error {
	path := labelVarsDefaultsPath(root)
	if len(names) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = strconv.Quote(n)
	}
	body := "# DEFAULTS' label_vars; written by cpb ALTER DEFAULTS SET label_vars…\nnames = [" + strings.Join(q, ", ") + "]\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return manifest.WritePrivate(path, []byte(body), 0o600)
}

// labelVarsAfter is a label_vars list after the SET and RESET clauses that
// name it, whether they do, and the list checked.
func labelVarsAfter(cur []string, clauses []grammar.Clause) ([]string, bool, error) {
	v, ok := grammar.PropertyValue(clauses, "label_vars")
	if !ok {
		return cur, false, nil
	}
	next := splitLabelVars(v)
	if err := checkLabelVars(next); err != nil {
		return cur, true, err
	}
	return next, true, nil
}

// labelVarsLine says what a label_vars list does.
func labelVarsLine(names []string) string {
	if len(names) == 0 {
		return "label_vars  (none): no launch label comes from the environment"
	}
	keys := make([]string, len(names))
	for i, n := range names {
		keys[i] = labelVarKey(n)
	}
	return "label_vars  " + strings.Join(names, ", ") + ": a launch copies their values into the launch labels " + strings.Join(keys, ", ")
}

// launchLabelVars is the names a launch copies: DEFAULTS', then those of
// the playbook (m, nil for a plain directory) DEFAULTS does not list.
func launchLabelVars(root string, m *manifest.Manifest) ([]string, error) {
	names, err := readLabelVarsDefaults(root)
	if err != nil {
		return nil, fmt.Errorf("DEFAULTS' label_vars cannot be read: %w", err)
	}
	if m != nil {
		for _, n := range m.LabelVars {
			if !containsFold(names, n) {
				names = append(names, n)
			}
		}
	}
	return names, nil
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// launchLabels is a launch's labels: the --label ones, then label_vars'
// values from the environment cpb was started with. What it leaves out is
// said on stderr in one line, by key, never by value.
func launchLabels(root string, m *manifest.Manifest, tokens []launchToken) (map[string]string, error) {
	names, err := launchLabelVars(root, m)
	if err != nil {
		return nil, err
	}
	labels, notes := withLabelVars(launchLabelsOf(tokens), names, os.LookupEnv)
	if len(notes) > 0 {
		fmt.Fprintf(os.Stderr, "cpb: label_vars left out: %s\n", strings.Join(notes, ", "))
	}
	return labels, nil
}

// withLabelVarsDefaults fills DEFAULTS' label_vars of v.
func withLabelVarsDefaults(v *defaultsJSON, root string) error {
	names, err := readLabelVarsDefaults(root)
	if err != nil {
		return fmt.Errorf("DEFAULTS' label_vars cannot be read: %w", err)
	}
	v.LabelVars = append([]string{}, names...)
	return nil
}

// explainLabelVars is the names a launch of pb copies into launch labels;
// none when DEFAULTS' cannot be read (EXPLAIN reads, it does not refuse).
func explainLabelVars(pb *playbook.Playbook) []string {
	names, err := launchLabelVars(config.ResolvePlaybooksDir(), pb.Manifest)
	if err != nil || names == nil {
		return []string{}
	}
	return names
}

// labelVarsShown is a label_vars list for a person: each name with the
// launch label it gives.
func labelVarsShown(names []string) string {
	if len(names) == 0 {
		return "none (label_vars)"
	}
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n + " as " + labelVarKey(n)
	}
	return strings.Join(parts, ", ")
}

// createLabelVars is the label_vars SHOW CREATE writes back: the names a
// statement takes, and a comment for a name it would refuse (a file edited
// by hand), left out so the statement applies.
func createLabelVars(names []string) ([]string, []string) {
	var ok, notes []string
	seen := map[string]bool{}
	for _, n := range names {
		if labelVarNameProblem(n) != "" || seen[labelVarKey(n)] || len(ok) == labelVarsMax {
			notes = append(notes, "-- label_vars: a name no statement could set is left out")
			continue
		}
		seen[labelVarKey(n)] = true
		ok = append(ok, n)
	}
	return ok, notes
}

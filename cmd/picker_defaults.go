package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/settings"
)

// The DEFAULTS model rows (SPEC.md, "Model picker"): ALTER DEFAULTS ADD
// MODEL keeps them in <playbooks root>/.model-picker, as the object a
// settings.json modelPicker is ({"options": [...]}), and every launch on
// Anthropic's own API offers them after the playbook's own rows. Nothing is
// written into a playbook: the launch passes the whole picker as its
// --settings, since a --settings picker replaces the one in settings.json
// rather than adding to it.
const defaultsPickerFile = ".model-picker"

func defaultsPickerPath(root string) string { return filepath.Join(root, defaultsPickerFile) }

// readDefaultsPicker reads the DEFAULTS picker object; nil when there is none.
func readDefaultsPicker(root string) (*settings.Object, error) {
	path := defaultsPickerPath(root)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	o, err := settings.ParseObject(data)
	if err != nil {
		return nil, fmt.Errorf("%s is not a JSON object: %w", path, err)
	}
	return o, nil
}

// defaultsPickerRows is the DEFAULTS model rows, in order.
func defaultsPickerRows(root string) ([]*settings.Object, error) {
	mp, err := readDefaultsPicker(root)
	if err != nil || mp == nil {
		return nil, err
	}
	return pickerRows(mp)
}

// writeDefaultsPicker stores the DEFAULTS picker (mode 0600, through a
// temporary file and a rename); a picker with no rows removes the file.
func writeDefaultsPicker(root string, mp *settings.Object) error {
	path := defaultsPickerPath(root)
	if mp == nil || !mp.Has("options") {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	compact, err := mp.MarshalJSON()
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	return manifest.WritePrivate(path, out.Bytes(), 0o600)
}

// applyDefaultsPicker applies ADD MODEL and DROP MODEL clauses to the
// DEFAULTS picker, as a playbook's picker takes them: a row is keyed by its
// model id, ADD changes a row in place, and DROP of a row that is not there
// is refused. It returns the picker after them (nil: no rows), the report
// lines, and whether anything changed.
func applyDefaultsPicker(mp *settings.Object, clauses []grammar.Clause) (*settings.Object, []string, bool, error) {
	root := settings.NewObject()
	if mp != nil {
		root.SetObject(keyModelPicker, mp)
	}
	var lines []string
	changed := false
	for _, c := range clauses {
		l, ch, err := applyPicker(root, c)
		if err != nil {
			if c.Kind == grammar.DropModel && len(c.Names) > 0 {
				return nil, nil, false, fmt.Errorf("DROP MODEL %s: DEFAULTS has no model row for it", c.Names[0])
			}
			return nil, nil, false, fmt.Errorf("DEFAULTS model rows: %w", err)
		}
		lines = append(lines, l...)
		changed = changed || ch
	}
	if !root.Has(keyModelPicker) {
		return nil, lines, changed, nil
	}
	out, err := root.Object(keyModelPicker)
	return out, lines, changed, err
}

// defaultsPickerJSON is the DEFAULTS model rows as SHOW DEFAULTS prints them.
func defaultsPickerJSON(mp *settings.Object) *pickerJSON {
	if mp == nil {
		return nil
	}
	root := settings.NewObject()
	root.SetObject(keyModelPicker, mp)
	p := readPicker(root)
	if p == nil || len(p.Options) == 0 {
		return nil
	}
	return p
}

// pickerAtLaunch is what a launch does with the DEFAULTS model rows: the
// whole picker it passes as --settings and the models that adds, or why it
// adds none. none is true when DEFAULTS has no model rows at all.
type pickerAtLaunch struct {
	picker *settings.Object
	added  []string
	skip   string
	none   bool
}

// Reasons a launch adds no DEFAULTS model row, as EXPLAIN names them.
const (
	pickerSkipExcluded   = "the playbook has model_picker_defaults = 'excluded'"
	pickerSkipRoute      = "the launch does not go to Anthropic's own API"
	pickerSkipOnly       = "the playbook's model picker is mode 'only'"
	pickerSkipSettingArg = "the launch passes its own --settings"
	pickerSkipAllThere   = "the playbook's own picker has every DEFAULTS row already"
	pickerSkipRemote     = "the launch runs on its sandbox host, whose cpb applies its own DEFAULTS"
)

// launchPicker decides, for a launch of configDir on the selected account,
// what the DEFAULTS model rows add. The launch and EXPLAIN both call it, so
// the two cannot disagree. Anything it cannot read leaves the launch as it
// would be without DEFAULTS rows: a skip, never a refusal.
func launchPicker(root, configDir string, sel account.Selection, rendered account.Rendered, claudeArgs []string) pickerAtLaunch {
	rows, err := defaultsPickerRows(root)
	if err != nil {
		return pickerAtLaunch{skip: "the DEFAULTS model rows cannot be read (" + err.Error() + ")"}
	}
	if len(rows) == 0 {
		return pickerAtLaunch{none: true}
	}
	if m, _ := manifest.Nearest(configDir); m.ExcludesModelPickerDefaults() {
		return pickerAtLaunch{skip: pickerSkipExcluded}
	}
	if !firstPartyRoute(sel, rendered) {
		return pickerAtLaunch{skip: pickerSkipRoute}
	}
	if hasSettingsArg(claudeArgs) {
		return pickerAtLaunch{skip: pickerSkipSettingArg}
	}
	sf, err := settings.Load(configDir)
	if err != nil {
		return pickerAtLaunch{skip: "the playbook's " + settings.FileName + " cannot be read"}
	}
	own, err := sf.Root.Object(keyModelPicker)
	if err != nil {
		return pickerAtLaunch{skip: "the playbook's " + keyModelPicker + " is not an object"}
	}
	var only bool
	if ok, _ := own.Get("replaceBuiltInOptions", &only); ok && only {
		return pickerAtLaunch{skip: pickerSkipOnly}
	}
	ownRows, err := pickerRows(own)
	if err != nil {
		return pickerAtLaunch{skip: "the playbook's " + keyModelPicker + ".options cannot be read"}
	}
	have := map[string]bool{}
	merged := make([]json.RawMessage, 0, len(ownRows)+len(rows))
	for _, o := range ownRows {
		have[strings.TrimSpace(rowModel(o))] = true
		b, _ := o.MarshalJSON()
		merged = append(merged, b)
	}
	var added []string
	for _, o := range rows {
		m := strings.TrimSpace(rowModel(o))
		if m == "" || have[m] {
			continue
		}
		have[m] = true
		b, _ := o.MarshalJSON()
		merged = append(merged, b)
		added = append(added, m)
	}
	if len(added) == 0 {
		return pickerAtLaunch{skip: pickerSkipAllThere}
	}
	_ = own.Set("options", merged)
	return pickerAtLaunch{picker: own, added: added}
}

// firstPartyRoute reports whether a launch goes to Anthropic's own API:
// an Anthropic account (the machine's, or one whose provider is anthropic)
// whose rendered layer sends it nowhere else. The account's layer decides
// the route (it blocks every other routing variable), as EXPLAIN's route
// does.
func firstPartyRoute(sel account.Selection, rendered account.Rendered) bool {
	if sel.Account != nil && sel.Account.Provider != account.Anthropic {
		return false
	}
	return routeEgress(layerOrigins(rendered.Layer)) == "anthropic"
}

// layerOrigins lists a layer's variables as origins, for routeEgress.
func layerOrigins(l *manifest.Env) []envset.Origin {
	if l == nil {
		return nil
	}
	var out []envset.Origin
	for k, v := range l.Set {
		out = append(out, envset.Origin{Key: k, Value: v})
	}
	for k, ref := range l.Refs {
		out = append(out, envset.Origin{Key: k, Ref: ref})
	}
	return out
}

// hasSettingsArg reports a --settings among claude's own arguments: Claude
// Code takes one, so a launch that passes its own gets nothing from cpb.
func hasSettingsArg(claudeArgs []string) bool {
	for _, a := range claudeArgs {
		if a == "--" {
			return false
		}
		if a == "--settings" || strings.HasPrefix(a, "--settings=") {
			return true
		}
	}
	return false
}

// launchSettings is the --settings a launch passes, holding the account's
// apiKeyHelper and the model picker with the DEFAULTS rows: one object,
// since Claude Code takes one --settings. nil when there is nothing to pass.
func launchSettings(keyHelper string, p pickerAtLaunch) []byte {
	if keyHelper == "" && p.picker == nil {
		return nil
	}
	if p.picker == nil {
		// The account's setting alone, exactly as before.
		s, _ := json.Marshal(map[string]string{"apiKeyHelper": keyHelper})
		return s
	}
	o := settings.NewObject()
	if keyHelper != "" {
		_ = o.Set("apiKeyHelper", keyHelper)
	}
	o.SetObject(keyModelPicker, p.picker)
	raw, _ := o.MarshalJSON()
	return compactJSON(raw)
}

// launchPickerLine is EXPLAIN's line for the DEFAULTS model rows; "" when
// DEFAULTS has none.
func launchPickerLine(p pickerAtLaunch) string {
	switch {
	case p.none:
		return ""
	case p.picker != nil:
		return "Model picker at launch: adds " + strings.Join(p.added, ", ") + " from DEFAULTS"
	default:
		return "Model picker at launch: adds nothing (" + p.skip + ")"
	}
}

// launchPickerJSONOf is EXPLAIN's JSON for the DEFAULTS model rows; nil
// when DEFAULTS has none.
type launchPickerJSON struct {
	Added []string `json:"added"`
	Skip  *string  `json:"skipped"`
}

func launchPickerJSONOf(p pickerAtLaunch) *launchPickerJSON {
	if p.none {
		return nil
	}
	v := &launchPickerJSON{Added: nonNil(p.added)}
	if p.picker == nil {
		v.Skip = strPtr(p.skip)
	}
	return v
}

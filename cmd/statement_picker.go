package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/settings"
)

// The model picker (SPEC.md, "Model picker"): the
// settings.json key modelPicker = {options: [{model, label, description,
// behavesAs}], replaceBuiltInOptions}. Rows are keyed by model id; a row no
// clause names is kept, whatever wrote it, and a key is written only when a
// clause gives it. Claude Code reads it from 2.1.242, behavesAs from 2.1.257.
const keyModelPicker = "modelPicker"

// pickerJSON is the picker as SHOW prints it; nil when there is none.
type pickerJSON struct {
	Mode    string          `json:"mode"` // "only" or "append"
	Options []pickerRowJSON `json:"options"`
}

type pickerRowJSON struct {
	Model       string  `json:"model"`
	Label       *string `json:"label"`
	Description *string `json:"description"`
	BehavesAs   *string `json:"behaves_as"`
}

// pickerRows reads modelPicker.options as ordered objects, so a field cpb
// does not know survives an edit.
func pickerRows(mp *settings.Object) ([]*settings.Object, error) {
	var raws []json.RawMessage
	if _, err := mp.Get("options", &raws); err != nil {
		return nil, fmt.Errorf("%s.options: %w", keyModelPicker, err)
	}
	rows := make([]*settings.Object, 0, len(raws))
	for i, r := range raws {
		o, err := settings.ParseObject(r)
		if err != nil {
			return nil, fmt.Errorf("%s.options[%d]: %w", keyModelPicker, i, err)
		}
		rows = append(rows, o)
	}
	return rows, nil
}

func rowModel(o *settings.Object) string {
	var m string
	_, _ = o.Get("model", &m)
	return m
}

// applyPicker applies one picker clause to a settings root.
func applyPicker(root *settings.Object, c grammar.Clause) ([]string, bool, error) {
	mp, err := root.Object(keyModelPicker)
	if err != nil {
		return nil, false, err
	}
	before, _ := mp.MarshalJSON()
	rows, err := pickerRows(mp)
	if err != nil {
		return nil, false, err
	}
	find := func(id string) int {
		for i, o := range rows {
			if rowModel(o) == id {
				return i
			}
		}
		return -1
	}
	var line string
	switch c.Kind {
	case grammar.AddModel:
		row := c.Row
		o := settings.NewObject()
		i := find(row.Model)
		if i >= 0 {
			o = rows[i]
		} else {
			_ = o.Set("model", row.Model)
		}
		for _, f := range []struct {
			key string
			v   *string
		}{{"label", row.Label}, {"description", row.Description}, {"behavesAs", row.BehavesAs}} {
			if f.v != nil {
				_ = o.Set(f.key, *f.v)
			}
		}
		if i < 0 {
			rows = append(rows, o)
		}
		line = "model     " + row.Model
	case grammar.DropModel:
		i := find(c.Names[0])
		if i < 0 {
			return nil, false, fmt.Errorf("DROP MODEL %s: the model picker has no row for it", c.Names[0])
		}
		rows = append(rows[:i], rows[i+1:]...)
		line = "dropped   model " + c.Names[0]
	case grammar.SetModelPicker:
		_ = mp.Set("replaceBuiltInOptions", c.Arg == "ONLY")
		line = "picker    " + strings.ToLower(c.Arg)
	case grammar.UnsetModelPickerMode:
		// Back to the default: Claude Code appends the rows.
		mp.Delete("replaceBuiltInOptions")
		line = "picker    mode unset (append)"
	}
	if len(rows) == 0 {
		mp.Delete("options")
	} else {
		raws := make([]json.RawMessage, 0, len(rows))
		for _, o := range rows {
			b, _ := o.MarshalJSON()
			raws = append(raws, b)
		}
		_ = mp.Set("options", raws)
	}
	after, _ := mp.MarshalJSON()
	if bytes.Equal(compactJSON(before), compactJSON(after)) {
		return nil, false, nil
	}
	if len(mp.Keys()) == 0 {
		root.Delete(keyModelPicker) // nothing left of it
	} else {
		root.SetObject(keyModelPicker, mp)
	}
	return []string{line}, true, nil
}

// readPicker is the picker as SHOW prints it.
func readPicker(root *settings.Object) *pickerJSON {
	if !root.Has(keyModelPicker) {
		return nil
	}
	mp, err := root.Object(keyModelPicker)
	if err != nil {
		return nil
	}
	p := &pickerJSON{Mode: "append", Options: []pickerRowJSON{}}
	var only bool
	if ok, _ := mp.Get("replaceBuiltInOptions", &only); ok && only {
		p.Mode = "only"
	}
	rows, err := pickerRows(mp)
	if err != nil {
		return p
	}
	for _, o := range rows {
		r := pickerRowJSON{Model: rowModel(o)}
		for _, f := range []struct {
			key string
			dst **string
		}{{"label", &r.Label}, {"description", &r.Description}, {"behavesAs", &r.BehavesAs}} {
			var v string
			if ok, err := o.Get(f.key, &v); ok && err == nil {
				*f.dst = &v
			}
		}
		p.Options = append(p.Options, r)
	}
	return p
}

// pickerLine is the picker in one human line.
func pickerLine(p *pickerJSON) string {
	var ids []string
	for _, o := range p.Options {
		s := o.Model
		if o.Label != nil {
			s += " (" + *o.Label + ")"
		}
		ids = append(ids, s)
	}
	if len(ids) == 0 {
		return p.Mode + ", no rows"
	}
	return p.Mode + ": " + strings.Join(ids, ", ")
}

// pickerCreateClauses is the picker as clauses, for SHOW CREATE, and
// comments for rows the grammar cannot write.
func pickerCreateClauses(root *settings.Object) ([]grammar.Clause, []string) {
	if !root.Has(keyModelPicker) {
		return nil, nil
	}
	mp, err := root.Object(keyModelPicker)
	if err != nil {
		return nil, []string{"-- the model picker in " + settings.FileName + " is not an object; not written"}
	}
	var out []grammar.Clause
	var comments []string
	var rows []pickerRowJSON
	if _, err := pickerRows(mp); err != nil {
		comments = append(comments, "-- the model picker's options in "+settings.FileName+" are not a list of objects; not written")
	} else if p := readPicker(root); p != nil {
		rows = p.Options
	}
	for _, r := range rows {
		if r.Model == "" || strings.ContainsAny(r.Model, " \t\r\n") {
			comments = append(comments, "-- a model picker row without a one-word model id; not written")
			continue
		}
		// What the parser takes back: a one-line label and description, a
		// one-word behavesAs. Anything else is kept in the file, not written.
		if bad := unwritableRowField(r); bad != "" {
			comments = append(comments, "-- model picker row "+r.Model+": its "+bad+" is not what ADD MODEL takes; not written")
			continue
		}
		out = append(out, grammar.Clause{Kind: grammar.AddModel, Names: []string{r.Model},
			Row: &grammar.PickerRow{Model: r.Model, Label: r.Label, Description: r.Description, BehavesAs: r.BehavesAs}})
	}
	var only bool
	if ok, err := mp.Get("replaceBuiltInOptions", &only); ok && err == nil {
		mode := "APPEND"
		if only {
			mode = "ONLY"
		}
		out = append(out, grammar.Clause{Kind: grammar.SetModelPicker, Arg: mode})
	}
	return out, comments
}

// compactJSON is data without insignificant space: a member read from the
// file keeps its formatting until it is re-encoded.
func compactJSON(data []byte) []byte {
	var b bytes.Buffer
	if json.Compact(&b, data) != nil {
		return data
	}
	return b.Bytes()
}

// unwritableRowField names the first field of a picker row that ADD MODEL
// cannot say, "" when the row can be written.
func unwritableRowField(r pickerRowJSON) string {
	switch {
	case r.Label != nil && strings.ContainsAny(*r.Label, "\r\n"):
		return "label (more than one line)"
	case r.Description != nil && strings.ContainsAny(*r.Description, "\r\n"):
		return "description (more than one line)"
	case r.BehavesAs != nil && (*r.BehavesAs == "" || strings.ContainsAny(*r.BehavesAs, " \t\r\n")):
		return "behavesAs (not one word)"
	}
	return ""
}

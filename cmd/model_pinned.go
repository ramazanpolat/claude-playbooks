package cmd

import (
	"regexp"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
)

// A pinned model id (SPEC.md, "Model picker"): a first-party Claude release
// id such as claude-fable-5-1[1m] or claude-haiku-4-5-20251001 names one
// release and stays on it when a newer model ships, where the family's alias
// (fable[1m], haiku) follows the newest model the installed Claude Code
// knows. cpb says so where such an id is set or shown, and never refuses
// one: pinning can be the point.
var pinnedModelID = regexp.MustCompile(`^claude-[a-z0-9]+(?:-[a-z0-9]+)*$`)

// pinnedFamilies are the families whose alias Claude Code resolves.
var pinnedFamilies = map[string]bool{"opus": true, "sonnet": true, "haiku": true, "fable": true}

// pinnedModel reports whether id is a pinned first-party Claude release id,
// and the alias, with the same [1m] suffix, that follows the newest model
// of its family; alias is "" for a family without one. Aliases, gateway
// ids (cc/claude-…, glm-5.3), provider ids (anthropic.claude-…, a Vertex
// id with @) and an empty id are never pinned.
func pinnedModel(id string) (alias string, pinned bool) {
	id = strings.TrimSpace(id)
	base, suffix := id, ""
	if i := strings.Index(id, "["); i >= 0 {
		base, suffix = id[:i], id[i:]
		if !strings.EqualFold(suffix, "[1m]") {
			return "", false
		}
	}
	if !pinnedModelID.MatchString(base) || !strings.ContainsAny(base, "0123456789") {
		return "", false
	}
	for _, seg := range strings.Split(strings.TrimPrefix(base, "claude-"), "-") {
		if seg == "" || strings.ContainsAny(seg, "0123456789") {
			continue
		}
		// The first segment that is a word is the family (claude-3-5-sonnet-…
		// spells it after the version).
		if pinnedFamilies[seg] {
			return seg + strings.ToLower(suffix), true
		}
		return "", true
	}
	return "", true
}

// pinnedNote is the note a statement that names a pinned id prints.
func pinnedNote(id string) string {
	alias, _ := pinnedModel(id)
	if alias == "" {
		return "'" + id + "' names one release: it stays on that model when a newer one ships."
	}
	return "'" + id + "' names one release; '" + alias + "' follows the newest the installed Claude Code knows."
}

// pinnedMark is what SHOW and EXPLAIN say of a pinned id; "" for any other.
func pinnedMark(id string) string {
	alias, pinned := pinnedModel(id)
	switch {
	case !pinned:
		return ""
	case alias == "":
		return "pinned to one release"
	}
	return "pinned; " + alias + " follows releases"
}

// markPinned is id as SHOW prints it in a row or a list: a pinned id says so.
func markPinned(id string) string {
	if mark := pinnedMark(id); mark != "" {
		return id + " (" + mark + ")"
	}
	return id
}

// notePinned records a note for each pinned id a statement sets as the
// model or adds as a model picker row (a playbook's, a directory's or
// DEFAULTS'). Notes go to stderr after the result; nothing is refused.
func (r *stmtRun) notePinned(st *grammar.Stmt) {
	seen := map[string]bool{}
	for _, c := range grammar.Flatten(st.Clauses) {
		var id string
		switch c.Kind {
		case grammar.SetModel:
			id = c.Arg
		case grammar.AddModel:
			if c.Row != nil {
				id = c.Row.Model
			}
		default:
			continue
		}
		if _, pinned := pinnedModel(id); pinned && !seen[id] {
			seen[id] = true
			r.notes = append(r.notes, pinnedNote(id))
		}
	}
}

package play

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Header is a recipe's leading `-- key: value` lines (the template
// convention agreed with the website on 2026-10-01). The header ends at the
// first line that is not one; a recipe without a header is fine.
type Header struct {
	Title       string
	Description string
	Needs       string
	// CreateWith is advisory: create-time flags a recipe cannot set
	// ("SANDBOX", "NO PILOT PROFILE"). play honours SANDBOX.
	CreateWith string
	// MinCPB is the oldest cpb the recipe is written for, as X.Y.Z.
	MinCPB string
	// Unknown lists keys the convention does not have, for --check.
	Unknown []string
	// Problems are header lines --check reports: a malformed min-cpb.
	Problems []string
}

var (
	headerLine = regexp.MustCompile(`^--\s*([a-z][a-z-]*):\s*(.*?)\s*$`)
	semver     = regexp.MustCompile(`^([0-9]+)\.([0-9]+)\.([0-9]+)$`)
)

// ParseHeader reads the header.
func ParseHeader(src []byte) Header {
	var h Header
	for _, line := range strings.Split(string(src), "\n") {
		m := headerLine.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			break
		}
		switch k, v := m[1], m[2]; k {
		case "title":
			h.Title = v
		case "description":
			h.Description = v
		case "needs":
			h.Needs = v
		case "create-with":
			h.CreateWith = v
		case "min-cpb":
			h.MinCPB = strings.TrimPrefix(v, "v")
			if !semver.MatchString(h.MinCPB) {
				h.Problems = append(h.Problems, fmt.Sprintf("min-cpb %q is not a version X.Y.Z", v))
				h.MinCPB = ""
			}
		default:
			h.Unknown = append(h.Unknown, k)
		}
	}
	return h
}

// WantsSandbox reports a create-with: that names SANDBOX.
func (h Header) WantsSandbox() bool {
	for _, w := range strings.Fields(strings.ToUpper(h.CreateWith)) {
		if w == "SANDBOX" {
			return true
		}
	}
	return false
}

// A build past a release tag, as git describe names it (v3.27.0-3-g562f9ff,
// or -dirty): newer than that tag, and carrying what comes after it.
var describedBuild = regexp.MustCompile(`-[0-9]+-g[0-9a-f]+|-dirty`)

// TooOld reports whether version (this cpb's, vX.Y.Z or X.Y.Z) is older
// than min-cpb. A dev build, a build past a tag (git describe), or an
// unreadable version is never too old: it is built from a tree newer than
// the release it names. An rc compares as its release (v3.28.0-rc1 is 3.28.0).
func (h Header) TooOld(version string) bool {
	if h.MinCPB == "" || describedBuild.MatchString(version) {
		return false
	}
	v := strings.TrimPrefix(version, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	have, ok1 := parseSemver(v)
	want, ok2 := parseSemver(h.MinCPB)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if have[i] != want[i] {
			return have[i] < want[i]
		}
	}
	return false
}

func parseSemver(s string) ([3]int, bool) {
	m := semver.FindStringSubmatch(s)
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := 0; i < 3; i++ {
		out[i], _ = strconv.Atoi(m[i+1])
	}
	return out, true
}

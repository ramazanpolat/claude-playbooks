package cmd

import (
	"regexp"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// The request is the one input cpb ask sends that the user typed, so it is
// checked before anything is sent: a credential typed into it would reach
// the model as typed. What is found is named by its key or its kind, never
// by its value.

// askKeyValue is KEY=value or KEY: value in free text.
var askKeyValue = regexp.MustCompile(`\b([A-Za-z][A-Za-z0-9_]{2,})\s*[=:]\s*['"]?([^\s'"]+)`)

// askRefShape is a value that is a reference, not a credential: a scheme and
// a colon (keychain:x, op://v/i/f, vault:x), or a placeholder.
var askRefShape = regexp.MustCompile(`^([a-z][a-z0-9+.-]*:|<[^<>]+>$)`)

// askURLPassword is a URL that carries a password: scheme://user:pass@host.
var askURLPassword = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@]+:[^/\s@]+@`)

// askTokenShapes are words shaped like a credential, by kind.
var askTokenShapes = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"an Anthropic key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{10,}`)},
	{"an API key (sk-...)", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`)},
	{"a GitHub token", regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`)},
	{"a GitLab token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`)},
	{"a Slack token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`)},
	{"an AWS access key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"a Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{"a JSON web token", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
}

// askLongHex and askLongRun are long unbroken runs a key or a token is made
// of; a run that follows a word naming a hash (sha256, commit) is a hash.
var (
	askLongHex = regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`)
	askLongRun = regexp.MustCompile(`\b[A-Za-z0-9_-]{40,}\b`)
	askHashCue = regexp.MustCompile(`(?i)(sha1|sha256|sha|hash|commit|digest|checksum)[\s:=-]*$`)
)

// askRequestCredential describes the first credential-shaped thing in
// request, "" when there is none.
func askRequestCredential(request string) string {
	for _, m := range askKeyValue.FindAllStringSubmatch(request, -1) {
		key, value := m[1], m[2]
		if !manifest.LooksLikeSecretKey(key) || manifest.PlainSetting(value) || askRefShape.MatchString(value) {
			continue
		}
		if strings.Trim(value, ".*x") == "" {
			continue // a value left out: KEY=..., KEY=xxx
		}
		return key + "=..."
	}
	if askURLPassword.MatchString(request) {
		return "a URL with a password in it"
	}
	for _, t := range askTokenShapes {
		if t.re.MatchString(request) {
			return "a word shaped like " + t.kind
		}
	}
	for _, re := range []*regexp.Regexp{askLongHex, askLongRun} {
		for _, loc := range re.FindAllStringIndex(request, -1) {
			word := request[loc[0]:loc[1]]
			if askHashCue.MatchString(request[:loc[0]]) {
				continue
			}
			if re == askLongRun && !(strings.ContainsAny(word, "0123456789") && strings.IndexFunc(word, isLetter) >= 0) {
				continue
			}
			return "a long key-shaped word"
		}
	}
	return ""
}

func isLetter(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }

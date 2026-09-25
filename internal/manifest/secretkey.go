package manifest

import (
	"regexp"
	"strings"
)

// What marks a key as credential-looking. No env profile field carries a
// real per-field secret marker (internal/envprofile.Profile.Set is a plain
// map[string]string), so this heuristic is what there is to go on. Missing a
// credential leaks it; over-matching costs one --reveal, so each rule is as
// wide as it can be without swallowing an obvious non-secret:
//
//	substring: GITHUBTOKEN has no underscore to split on. TOKENIZER_PATH is
//	           over-redacted as a result, which is the cheap direction.
//	segment:   AUTH as a substring would redact AUTHOR_NAME. The
//	           abbreviations live here too and must: PWD is the standard
//	           MySQL password variable (MYSQL_PWD) but also the standard
//	           working directory, and PASS as a substring would redact
//	           COMPASS_URL and BYPASS_CACHE. PAT stays clear of PATH only
//	           because the match is a whole segment.
//	suffix:    API_KEY and MY_APIKEY are keys; KEYBOARD_LAYOUT is not.
//	public:    a key naming public material defeats the suffix rule only --
//	           PUBLIC_KEY is meant to be read and compared, while a
//	           PUBLIC_SECRET is a contradiction worth masking anyway.
var (
	secretKeySubstrings      = []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "PASSPHRASE", "CREDENTIAL"}
	secretKeySegments        = map[string]bool{"AUTH": true, "PWD": true, "PASS": true, "PAT": true}
	secretKeySegmentSuffixes = []string{"KEY", "KEYS"}
	publicKeySegments        = map[string]bool{"PUBLIC": true, "PUB": true}
)

// LooksLikeSecretKey reports whether key names what is likely a credential,
// by the rules above. It drives redaction in every display, and the
// grammar's refusal of credential-looking literals.
func LooksLikeSecretKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, substring := range secretKeySubstrings {
		if strings.Contains(upper, substring) {
			return true
		}
	}
	parts := strings.Split(upper, "_")
	for _, part := range parts {
		if secretKeySegments[part] {
			return true
		}
	}
	for _, part := range parts {
		if publicKeySegments[part] {
			return false
		}
	}
	for _, part := range parts {
		for _, suffix := range secretKeySegmentSuffixes {
			if strings.HasSuffix(part, suffix) {
				return true
			}
		}
	}
	return false
}

var plainNumber = regexp.MustCompile(`^-?[0-9]+$`)

// PlainSetting reports whether a value cannot be a secret, so that a
// credential-looking key holding it is a setting: MAX_THINKING_TOKENS=8000,
// SOME_AUTH_ENABLED=true, an empty value. The grammar lets these through
// without AS PLAINTEXT, and SHOW prints them.
func PlainSetting(v string) bool {
	return v == "" || plainNumber.MatchString(v) || strings.EqualFold(v, "true") || strings.EqualFold(v, "false")
}

package manifest

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// RefRefusedKeys may never hold a secret reference: keys whose VALUE cpb's
// own launch logic must read, which a reference hides by design ("set, value
// unknown" would break that logic silently). A future such key joins this
// list rather than getting a special case. Refused at every layer: a
// playbook's own block and every env set. Empty since v4.2.0:
// CLAUDE_CODE_OAUTH_TOKEN left it when the token decision learned to treat a
// reference as "a token, supplied by the helper at exec" (accounts).
var RefRefusedKeys = map[string]string{}

// refPattern is the shape every secret reference shares: a scheme, a colon,
// the rest (keychain:…, op://…, vault:…). A bare word is far more likely a
// value pasted where a reference belongs.
var refPattern = regexp.MustCompile(`^[a-z][a-z0-9+.-]*:.+`)

// LooksLikeRef reports whether s has the shape of a secret reference.
func LooksLikeRef(s string) bool { return refPattern.MatchString(s) }

// ValidateRefKey reports whether key may hold a reference. The error names
// the key and never a value.
func ValidateRefKey(key string) error {
	if err := ValidateEnvKey(key); err != nil {
		return err
	}
	if why, refused := RefRefusedKeys[key]; refused {
		return fmt.Errorf("%s cannot be a secret reference: %s", key, why)
	}
	return nil
}

// ValidateRefs checks a layer's references: well-formed keys that may hold
// one, reference-shaped values, and no key also in set or unset. Errors
// never quote a reference: a malformed one may be a pasted secret.
func ValidateRefs(refs, set map[string]string, unset []string) error {
	for key, ref := range refs {
		if err := ValidateRefKey(key); err != nil {
			return err
		}
		if !LooksLikeRef(ref) || strings.ContainsAny(ref, "\x00\n") {
			return fmt.Errorf("%s: not a secret reference (a scheme such as keychain: or op://, then the rest)", key)
		}
		if _, both := set[key]; both {
			return fmt.Errorf("%s is both set and a reference", key)
		}
		for _, u := range unset {
			if u == key {
				return fmt.Errorf("%s is both unset and a reference", key)
			}
		}
	}
	return nil
}

// WriteRefsTable emits a sorted [table] of references, when there are any.
// The manifest writes [env.refs]; an env set file writes [refs].
func WriteRefsTable(b *strings.Builder, header string, refs map[string]string) {
	if len(refs) == 0 {
		return
	}
	keys := make([]string, 0, len(refs))
	for key := range refs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	b.WriteString("\n" + header + "\n")
	for _, key := range keys {
		fmt.Fprintf(b, "%s = %s\n", key, QuoteTOML(refs[key]))
	}
}

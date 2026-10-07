package cmd

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// displayEnvValue is what SHOW and EXPLAIN print for a stored value:
// redacted when the key looks like a credential (manifest.LooksLikeSecretKey),
// with only a URL's credential masked otherwise.
func displayEnvValue(key, value string) string {
	if value == "" {
		return value
	}
	if manifest.LooksLikeSecretKey(key) {
		return redactSecretValue(value)
	}
	return redactURLCredentials(value)
}

// urlUserinfo matches the credential an absolute URL carries before its
// host. Anchored on "://" so a bare "user:pass@host" or a mailto: address is
// left alone. The authority ends at the first "/", "?" or "#", so none of
// them may appear in userinfo -- without "?" and "#" here, the "@" in
// https://service.test?email=a@example.com reads as a userinfo delimiter and
// an ordinary callback URL is mangled as though it carried a credential.
// RFC 3986 requires both to be percent-encoded inside userinfo anyway, so
// excluding them cannot miss a real one.
var urlUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^/?#@\s]+)@`)

// redactURLCredentials masks the credential inside a connection URL. Every
// other rule here reads the key, and this case cannot: DATABASE_URL,
// REDIS_URL, AMQP_URL and MONGODB_URI all name nothing secret while carrying
// a password in the value. Only the credential is masked, not the whole
// value -- the scheme, host and database are what the pilot came to read,
// and a wholly masked DATABASE_URL would just train them to read the file
// instead, which is how a feature like this stops being used.
func redactURLCredentials(value string) string {
	return urlUserinfo.ReplaceAllStringFunc(value, func(match string) string {
		parts := urlUserinfo.FindStringSubmatch(match)
		scheme, userinfo := parts[1], parts[2]
		left, right, hasColon := strings.Cut(userinfo, ":")
		if !hasColon {
			return scheme + redactSecretValue(userinfo) + "@"
		}
		return scheme + maskUserinfoField(left) + ":" + maskUserinfoField(right) + "@"
	})
}

// maskUserinfoField masks one side of a URL's user:password pair. Both sides
// are masked, because the colon says only that there are two fields -- never
// which one holds the secret. postgres://user:pw@host keeps it on the right,
// while https://TOKEN:x-oauth-basic@host (GitHub's documented form) and
// https://TOKEN:@host (what `git credential` writes) keep it on the left
// beside a dummy or empty password. Nothing in the structure distinguishes
// them, so masking only one side leaks the other half the time, and the
// username is the cheaper thing to lose. An empty field stays empty: there
// is nothing to hide, and "<redacted, 0 chars>" is noise that also advertises
// which shape this is.
func maskUserinfoField(field string) string {
	if field == "" {
		return ""
	}
	return redactSecretValue(field)
}

// redactSecretValue prints no character of a credential, only its length:
// "<redacted, 43 chars>". Showing the ends ("sk-a...7f2c") told which key was
// attached, but it is still part of the secret, in every terminal scroll-back
// and transcript it lands in; a key is told apart by its reference or its
// layer instead. Runes, not bytes, so the length is the one a person counts.
func redactSecretValue(value string) string {
	return fmt.Sprintf("<redacted, %d chars>", len([]rune(value)))
}

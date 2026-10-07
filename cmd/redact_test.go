package cmd

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

func TestLooksLikeSecretKey(t *testing.T) {
	cases := map[string]bool{
		"ANTHROPIC_AUTH_TOKEN":    true,
		"CLAUDE_CODE_OAUTH_TOKEN": true,
		"API_KEY":                 true,
		"OPENAI_SECRET":           true,
		"AUTH_HEADER_VALUE":       true,
		"DB_PASSWORD":             true,
		"AWS_SECRET_ACCESS_KEY":   true,

		// No underscore to split on: a whole-word match misses these
		// entirely, and a missed credential is the failure that costs.
		"GITHUBTOKEN": true,
		"APIKEY":      true,

		// Well-formed, underscore-separated, and still not a word in any
		// plausible word list -- the suffix and substring rules cover them
		// without enumerating every spelling of "key".
		"MY_APIKEY":      true,
		"GPG_SIGNKEY":    true,
		"SSH_KEYS":       true,
		"MY_CREDENTIAL":  true,
		"SSH_PASSPHRASE": true,

		// Conventional abbreviations. MYSQL_PWD is the standard MySQL
		// password variable and was printing in full.
		"MYSQL_PWD":  true,
		"DB_PASS":    true,
		"GITHUB_PAT": true,

		// ...which is exactly why those three are segment-anchored, not
		// substrings: PATH is not a PAT, and COMPASS/BYPASS merely contain
		// PASS.
		"PATH":         false,
		"COMPASS_URL":  false,
		"BYPASS_CACHE": false,

		// A bare PWD is usually the working directory, and masking it is
		// the accepted over-match: it could equally name a password, and
		// guessing "directory" is the assumption that leaks. OLDPWD is
		// unambiguous -- no password is ever called that -- and the
		// segment-exact rule leaves it alone on its own.
		"PWD":    true,
		"OLDPWD": false,

		// Public material is meant to be read and compared, so PUBLIC/PUB
		// defeats the KEY suffix rule...
		"PUBLIC_KEY":     false,
		"SSH_PUBLIC_KEY": false,
		"SSH_PUB_KEY":    false,

		// ...but only that rule. A "public secret" is a contradiction, and
		// the strong words still win.
		"PUBLIC_SECRET": true,
		"PUBLIC_TOKEN":  true,

		// Deliberate over-match: costs a masked value, never a credential.
		"TOKENIZER_PATH": true,

		// The counterexamples each rule is shaped around. KEY is anchored to
		// the end of a segment so KEYBOARD stays clear; AUTH matches a whole
		// segment only so AUTHOR does.
		"KEYBOARD_LAYOUT":    false,
		"AUTHOR_NAME":        false,
		"PRIVATE_NETWORK":    false,
		"ANTHROPIC_BASE_URL": false,
		"FROM_MANIFEST":      false,
		"A":                  false,
	}
	for key, want := range cases {
		if got := manifest.LooksLikeSecretKey(key); got != want {
			t.Errorf("manifest.LooksLikeSecretKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestRedactSecretValue(t *testing.T) {
	// No character of the value, at any length: only the length, in runes.
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	for _, n := range []int{1, 6, 8, 12, 16, 32, 36} {
		v := alphabet[:n]
		got := redactSecretValue(v)
		if want := fmt.Sprintf("<redacted, %d chars>", n); got != want {
			t.Errorf("redactSecretValue(%d chars) = %q, want %q", n, got, want)
		}
	}
	if got := redactSecretValue("sk-abcdef0123456789fedcba9876543210"); strings.Contains(got, "sk-a") || strings.Contains(got, "3210") || got != "<redacted, 35 chars>" {
		t.Fatalf("a token redacted as %q", got)
	}
	if got := redactSecretValue(strings.Repeat("é", 16)); got != "<redacted, 16 chars>" {
		t.Fatalf("multi-byte value redacted as %q", got)
	}
}

// A connection URL carries its credential in the value: DATABASE_URL,
// REDIS_URL and MONGODB_URI all name nothing secret, so no key-based rule
// can catch them.
func TestDisplayEnvValueMasksURLCredentials(t *testing.T) {
	cases := []struct {
		name, key, value, want string
	}{
		{
			// Both sides masked: structure cannot say which holds the secret.
			name:  "user and password are both masked, the rest stays legible",
			key:   "DATABASE_URL",
			value: "postgres://user:hunter2pass@db.example.com:5432/app",
			want:  "postgres://<redacted, 4 chars>:<redacted, 11 chars>@db.example.com:5432/app",
		},
		{
			// Masking only "after the colon" would leave this fully exposed.
			name:  "userinfo with no colon is entirely the credential",
			key:   "GIT_REMOTE",
			value: "https://ghp_abcdefghijklmnop@github.com/org/repo",
			want:  "https://<redacted, 20 chars>@github.com/org/repo",
		},
		{
			// What `git credential` writes: the token is the USERNAME and the
			// password is empty. Masking the password side leaked the token.
			name:  "token username with an empty password",
			key:   "GIT_REMOTE",
			value: "https://ghp_abcdefghijklmnop:@github.com/org/repo",
			want:  "https://<redacted, 20 chars>:@github.com/org/repo",
		},
		{
			// GitHub's own documented form: the password is a fixed dummy.
			name:  "token username with a dummy password",
			key:   "GIT_REMOTE",
			value: "https://ghp_abcdefghijklmnop:x-oauth-basic@github.com/o/r",
			want:  "https://<redacted, 20 chars>:<redacted, 13 chars>@github.com/o/r",
		},
		{
			// No path before the query, so the "@" of an email parameter used
			// to read as a userinfo delimiter and mangle the whole URL.
			name:  "an @ in a query parameter is not userinfo",
			key:   "CALLBACK_URL",
			value: "https://service.test?email=a@example.com",
			want:  "https://service.test?email=a@example.com",
		},
		{
			name:  "an @ in a fragment is not userinfo",
			key:   "DOCS_URL",
			value: "https://service.test#contact@example.com",
			want:  "https://service.test#contact@example.com",
		},
		{
			name:  "a URL with no credential is untouched",
			key:   "ANTHROPIC_BASE_URL",
			value: "http://proxy:1/v1",
			want:  "http://proxy:1/v1",
		},
		{
			name:  "a port is not userinfo and a path is not a host",
			key:   "SERVICE_URL",
			value: "https://example.com:8443/a@b/c",
			want:  "https://example.com:8443/a@b/c",
		},
		{
			name:  "an ordinary value containing @ is untouched",
			key:   "CONTACT",
			value: "someone@example.com",
			want:  "someone@example.com",
		},
		{
			name:  "scheme with a plus, as mongodb+srv uses",
			key:   "MONGODB_URI",
			value: "mongodb+srv://admin:s3cr3tvalue@cluster0.example.net/db",
			want:  "mongodb+srv://<redacted, 5 chars>:<redacted, 11 chars>@cluster0.example.net/db",
		},
		{
			name:  "IPv6 literal host with no credential is untouched",
			key:   "PG_URL",
			value: "http://[::1]:5432/db",
			want:  "http://[::1]:5432/db",
		},
		{
			name:  "IPv6 literal host keeps its brackets and port",
			key:   "PG_URL",
			value: "postgres://user:hunter2pass@[::1]:5432/db",
			want:  "postgres://<redacted, 4 chars>:<redacted, 11 chars>@[::1]:5432/db",
		},
		{
			// %40 is an encoded "@" and must not end the userinfo early.
			name:  "percent-encoded @ inside userinfo",
			key:   "DATABASE_URL",
			value: "https://user%40corp:hunter2pass@host/db",
			want:  "https://<redacted, 11 chars>:<redacted, 11 chars>@host/db",
		},
		{
			name:  "every URL in a multi-URL value is masked, not just the first",
			key:   "UPSTREAMS",
			value: "redis://u1:secretalpha@a.internal,redis://u2:secretbravo@b.internal",
			want:  "redis://<redacted, 2 chars>:<redacted, 11 chars>@a.internal,redis://<redacted, 2 chars>:<redacted, 11 chars>@b.internal",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := displayEnvValue(c.key, c.value); got != c.want {
				t.Errorf("displayEnvValue(%q, %q) = %q, want %q", c.key, c.value, got, c.want)
			}
		})
	}

	// A credential-looking key still redacts the whole value: the URL rule
	// widens coverage, it does not narrow it.
	if got := displayEnvValue("DATABASE_PASSWORD", "postgres://user:pw@host/db"); !strings.HasPrefix(got, "<redacted") && !strings.Contains(got, "...") {
		t.Errorf("credential-looking key should redact the whole value, got %q", got)
	}
}

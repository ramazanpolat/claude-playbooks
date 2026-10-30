package account

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// An account's extra request headers (SPEC.md, "CREATE / ALTER / DROP
// ACCOUNT"): SET header.<Name> FROM '<ref>'. A gateway behind an access
// proxy asks for headers of its own (an access client id and secret, say).
// Each value is a secret reference: cpb never holds one. At launch the
// secret helper sets each header's value in a variable of its own
// (HeaderVar), and cpb's header step joins them into
// ANTHROPIC_CUSTOM_HEADERS, the one variable Claude Code reads, in the
// environment of claude only.

// HeaderKeyPrefix starts a header's key in the grammar: header.<Name>.
const HeaderKeyPrefix = "header."

// MaxHeaders is how many headers an account may carry.
const MaxHeaders = 8

// HeaderVarPrefix starts the variables the secret helper sets a launch's
// header values in; the header step reads and removes them.
const HeaderVarPrefix = "CPB_ACCOUNT_HEADER_"

// HeaderVar is the variable the i-th header (in HeaderNames order) is
// resolved into.
func HeaderVar(i int) string { return HeaderVarPrefix + strconv.Itoa(i) }

var headerNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)

// ValidateHeaderName refuses a name that is not a plain header name, and
// one that the account's credential, Anthropic's API or the request itself
// owns.
func ValidateHeaderName(name string) error {
	if !headerNameRE.MatchString(name) {
		return fmt.Errorf("header %q: a header name is letters, digits and dashes, starting with a letter or digit (at most 64)", name)
	}
	l := strings.ToLower(name)
	switch {
	case l == "authorization" || l == "x-api-key" || l == "proxy-authorization":
		return fmt.Errorf("header %s carries the request's credential, which is the account's own: auth_token or api_key", name)
	case strings.HasPrefix(l, "anthropic-"):
		return fmt.Errorf("header %s belongs to Anthropic's API (its version and beta flags): Claude Code sets it", name)
	case l == "host" || l == "content-length" || l == "content-type" || l == "transfer-encoding" || l == "connection":
		return fmt.Errorf("header %s frames the request itself: Claude Code sets it", name)
	}
	return nil
}

// HeaderNames are the account's header names, sorted case-insensitively:
// the order they render in.
func (a *Account) HeaderNames() []string {
	names := make([]string, 0, len(a.Headers))
	for n := range a.Headers {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	return names
}

// SetHeader sets a header's reference, replacing one of the same name in
// another case.
func (a *Account) SetHeader(name, ref string) {
	a.ResetHeader(name)
	if a.Headers == nil {
		a.Headers = map[string]string{}
	}
	a.Headers[name] = ref
}

// ResetHeader removes a header, in any case; it reports whether one was
// there.
func (a *Account) ResetHeader(name string) bool {
	found := false
	for n := range a.Headers {
		if strings.EqualFold(n, name) {
			delete(a.Headers, n)
			found = true
		}
	}
	if len(a.Headers) == 0 {
		a.Headers = nil
	}
	return found
}

// validateHeaders checks the headers whole. Errors name a header, never
// its reference.
func (a *Account) validateHeaders() error {
	if len(a.Headers) == 0 {
		return nil
	}
	if a.Provider != Anthropic && a.Provider != Gateway {
		return fmt.Errorf("a %s account carries no headers: Claude Code sends custom headers on requests to an Anthropic or gateway address", a.Provider)
	}
	if len(a.Headers) > MaxHeaders {
		return fmt.Errorf("an account carries at most %d headers", MaxHeaders)
	}
	seen := map[string]bool{}
	for _, n := range a.HeaderNames() {
		if err := ValidateHeaderName(n); err != nil {
			return err
		}
		if seen[strings.ToLower(n)] {
			return fmt.Errorf("header %s is set twice (header names ignore case)", n)
		}
		seen[strings.ToLower(n)] = true
		if !manifest.LooksLikeRef(a.Headers[n]) {
			return errors.New("header " + n + " takes a secret reference: SET header." + n + " FROM '<ref>'")
		}
	}
	return nil
}

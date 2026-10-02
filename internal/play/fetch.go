package play

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// The fetch's limits. A recipe is a few KiB; 64 KiB leaves room and keeps
// a hostile server from feeding cpb a large body.
const (
	MaxSize      = 64 << 10
	MaxRedirects = 3
	dialTimeout  = 10 * time.Second
	totalTimeout = 30 * time.Second
)

// Recipe is a recipe's bytes, fetched once: the preview, the plan, the
// apply and a --keep record all use these, so what was previewed is what
// runs.
type Recipe struct {
	Bytes  []byte
	URL    string // the final URL after redirects; "" for a local file
	Path   string // a local file
	SHA256 string // hex
}

// NewClient is the fetch's HTTP client: https only, at most MaxRedirects
// redirects and every hop https, no cookies, the system's proxy settings,
// and the timeouts above.
func NewClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = http.ProxyFromEnvironment
	t.DialContext = (&net.Dialer{Timeout: dialTimeout}).DialContext
	t.TLSHandshakeTimeout = dialTimeout
	return &http.Client{
		Transport: t,
		Timeout:   totalTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > MaxRedirects {
				return fmt.Errorf("more than %d redirects", MaxRedirects)
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("a redirect to %s is refused: https only", req.URL.Scheme)
			}
			if req.URL.User != nil {
				return errors.New("a redirect to a URL carrying credentials is refused")
			}
			return nil
		},
	}
}

// ErrNotFound is a 404: for a template name, the caller suggests others.
var ErrNotFound = errors.New("not found")

// Fetch reads the recipe at rawURL once, within the limits.
func Fetch(ctx context.Context, client *http.Client, rawURL, userAgent string) (*Recipe, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if req.URL.Scheme != "https" {
		return nil, errors.New("https only")
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	final := resp.Request.URL.String()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s", ErrNotFound, final)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%s: HTTP %d", final, resp.StatusCode)
	}
	b, err := readCapped(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", final, err)
	}
	return newRecipe(b, final, "")
}

// ReadLocal reads a local recipe, with the same size and text checks.
func ReadLocal(path string) (*Recipe, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := readCapped(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return newRecipe(b, "", path)
}

// readCapped reads at most MaxSize bytes, and fails on more.
func readCapped(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxSize {
		return nil, fmt.Errorf("larger than %d KiB: a recipe is a few KiB", MaxSize>>10)
	}
	return b, nil
}

func newRecipe(b []byte, url, path string) (*Recipe, error) {
	if !utf8.Valid(b) {
		return nil, errors.New("not UTF-8 text")
	}
	for _, c := range b {
		if c == 0 {
			return nil, errors.New("not text: it holds a NUL byte")
		}
	}
	sum := sha256.Sum256(b)
	return &Recipe{Bytes: b, URL: url, Path: path, SHA256: hex.EncodeToString(sum[:])}, nil
}

// Suggest returns up to three names from a template index (one name per
// line) close to name: a shared prefix or substring, or a small edit
// distance; the closest first.
func Suggest(index []byte, name string) []string {
	type cand struct {
		n string
		d int
	}
	var cs []cand
	for _, n := range strings.Fields(string(index)) {
		if !templateName.MatchString(n) || n == name {
			continue
		}
		d := editDistance(n, name)
		if strings.Contains(n, name) || strings.Contains(name, n) {
			d = 0
		}
		if d <= 3 {
			cs = append(cs, cand{n, d})
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].d < cs[j].d })
	var out []string
	for i := 0; i < len(cs) && i < 3; i++ {
		out = append(out, cs[i].n)
	}
	return out
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := 1
			if a[i-1] == b[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev = cur
	}
	return prev[len(b)]
}

// IndexURL is the template index next to a template's URL.
func IndexURL(templateURL string) string {
	return templateURL[:strings.LastIndex(templateURL, "/")+1] + "index.txt"
}

package manifest

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// ParseEnvFile reads a dotenv-style file into an Env layer: one KEY=VALUE per
// line, blank lines and lines starting with # skipped, an optional leading
// "export " tolerated, and one pair of matching surrounding quotes (single or
// double) stripped from the value. No other quote or escape processing: the
// value is taken verbatim, which is what a shell would pass through too.
// Keys and values are validated like manifest entries, so a file cannot smuggle
// CLAUDE_CONFIG_DIR, a NUL, or non-UTF-8 past the same rules. Later lines win
// over earlier ones for the same key.
func ParseEnvFile(path string) (*Env, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := &Env{Set: map[string]string{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			// The line is not echoed: a bare token on its own line is as
			// likely a pasted secret as a typo, and stderr is often logged.
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, lineNo)
		}
		key = strings.TrimSpace(key)
		if ReservedEnvKeys[key] {
			return nil, fmt.Errorf("%s:%d: %s is managed by claude-playbook and cannot be overridden", path, lineNo, key)
		}
		if err := ValidateEnvKey(key); err != nil {
			// The rejected "key" is not echoed either: a secret containing
			// "=" (padded base64, say) splits into a bogus key and value.
			return nil, fmt.Errorf("%s:%d: invalid environment variable name (not shown; the line may hold a secret)", path, lineNo)
		}
		value = strings.TrimSpace(value)
		if n := len(value); n >= 2 && (value[0] == '"' || value[0] == '\'') && value[n-1] == value[0] {
			value = value[1 : n-1]
		}
		if err := ValidateEnvValue(key, value); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		out.Set[key] = value
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

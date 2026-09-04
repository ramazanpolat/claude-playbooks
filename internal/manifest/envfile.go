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
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE, got %q", path, lineNo, line)
		}
		key = strings.TrimSpace(key)
		if err := ValidateEnvKey(key); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNo, err)
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

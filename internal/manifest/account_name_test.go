package manifest

import (
	"strings"
	"testing"
)

// Account names are lowercase and short: the store may be on a
// case-insensitive filesystem, and a pasted credential is long. The error
// never quotes the name.
func TestValidateAccountName(t *testing.T) {
	for _, ok := range []string{"work", "glm-5.3", "a", "x_y", "machine", strings.Repeat("a", 40)} {
		if err := ValidateAccountName(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"Work", "WORK", "", "-x", ".x", strings.Repeat("a", 41), "sk-ant-api03-abc", "a b", "a/b"} {
		err := ValidateAccountName(bad)
		if err == nil {
			t.Errorf("%q accepted", bad)
			continue
		}
		if bad != "" && strings.Contains(err.Error(), bad) {
			t.Errorf("%q quoted back: %v", bad, err)
		}
	}
}

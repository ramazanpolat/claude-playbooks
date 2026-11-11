package manifest

import (
	"strings"
	"testing"
)

func TestValidateCPBRef(t *testing.T) {
	for _, ok := range []string{"cpb:a", "cpb:service-token", "cpb:a.b_c-9", "cpb:" + strings.Repeat("a", 64), "keychain:Anything Goes", "op://v/i/f"} {
		if err := ValidateCPBRef(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"cpb:", "cpb:Upper", "cpb:-lead", "cpb:.dot", "cpb:a/b", "cpb:a b", "cpb:" + strings.Repeat("a", 65), "cpb:a\x00b"} {
		err := ValidateCPBRef(bad)
		if err == nil {
			t.Errorf("%q passed", bad)
			continue
		}
		if name := strings.TrimPrefix(bad, "cpb:"); name != "" && strings.Contains(err.Error(), name) {
			t.Errorf("the error quotes the name: %v", err)
		}
	}
	if err := ValidateRefs(map[string]string{"TOKEN": "cpb:Bad"}, nil, nil); err == nil {
		t.Error("ValidateRefs passed a bad cpb: name")
	}
}

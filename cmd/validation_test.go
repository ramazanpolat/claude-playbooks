package cmd

import "testing"

// A playbook name is interpolated into a generated shell alias, its `run`
// argument, and pasteable commands. Rejecting unsafe names at input removes
// that whole class instead of escaping it at each site.
func TestValidateTopLevelNameCharset(t *testing.T) {
	ok := []string{"demo", "my_lab-2", "Work", "a", "_x", "2024"}
	for _, n := range ok {
		if err := validateTopLevelName("name", n); err != nil {
			t.Errorf("valid name %q rejected: %v", n, err)
		}
	}
	bad := []string{
		`x'; touch PWNED; #`, // command injection
		`bob's`,              // apostrophe alone broke alias quoting
		"a b",                // whitespace splits the run argument
		"-lead",              // reads as a flag
		"a$b", "a`b", `a"b`, "a;b", "a|b", "a&b", "a*b",
	}
	for _, n := range bad {
		if err := validateTopLevelName("name", n); err == nil {
			t.Errorf("unsafe name %q accepted", n)
		}
	}
}

// Lookup paths stay permissive so an existing playbook with an odd name can
// still be deleted.
func TestValidateSinglePathSegmentStaysPermissive(t *testing.T) {
	if err := validateSinglePathSegment("name", `bob's`); err != nil {
		t.Fatalf("delete must still be able to name an existing odd playbook: %v", err)
	}
}

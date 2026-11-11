package auth

import (
	"errors"
	"testing"
)

// The exported hook swaps the Keychain secret lookup and puts back the one
// it replaced, and the flag a guard reads follows it. Under it stands this
// package's TestMain stub, so the real lookup is never called.
func TestSetKeychainSecretForTest(t *testing.T) {
	if KeychainSecretStubbedForTest() {
		t.Fatal("reported stubbed before the hook was used")
	}
	restore := SetKeychainSecretForTest(func(service string) ([]byte, error) {
		return []byte("stub for " + service), nil
	})
	if !KeychainSecretStubbedForTest() {
		t.Fatal("not reported stubbed with the hook in place")
	}
	if out, err := keychainSecret("some-service"); err != nil || string(out) != "stub for some-service" {
		t.Fatalf("the lookup is not the hook's stub: %q, %v", out, err)
	}
	restore()
	if KeychainSecretStubbedForTest() {
		t.Fatal("still reported stubbed after restore")
	}
	if out, err := findGenericPassword("some-service"); !errors.Is(err, errKeychainStubbed) || out != nil {
		t.Fatalf("restore did not put back the lookup it replaced: %q, %v", out, err)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("a nil stub was accepted")
		}
	}()
	SetKeychainSecretForTest(nil)
}

package auth

// keychainSecretStubbed is true while a stub installed by
// SetKeychainSecretForTest stands in for the Keychain secret lookup.
var keychainSecretStubbed bool

// SetKeychainSecretForTest is for tests only; cpb never calls it. It
// replaces the lookup that reads a Keychain item's secret (on macOS,
// `security find-generic-password -w`) with stub, for the tests of other
// packages, which cannot reach this package's own seam, and restore puts
// back what was there. A test binary that can reach a sync (cmd's) installs
// a stub once, in TestMain, so none of its tests can read a real Keychain
// item, on any OS. It panics on a nil stub.
func SetKeychainSecretForTest(stub func(service string) ([]byte, error)) (restore func()) {
	if stub == nil {
		panic("auth.SetKeychainSecretForTest: nil stub")
	}
	prev, prevStubbed := findGenericPassword, keychainSecretStubbed
	findGenericPassword, keychainSecretStubbed = stub, true
	return func() { findGenericPassword, keychainSecretStubbed = prev, prevStubbed }
}

// KeychainSecretStubbedForTest is for tests only; cpb never calls it. It
// reports whether SetKeychainSecretForTest has replaced the Keychain secret
// lookup, without calling the lookup, so a test binary's guard can fail
// before any of its tests could read a real Keychain item.
func KeychainSecretStubbedForTest() bool { return keychainSecretStubbed }

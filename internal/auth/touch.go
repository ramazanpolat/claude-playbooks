package auth

import "path/filepath"

// CredentialTouch, when set, is called each time cpb is about to touch a
// credential: the token file (to read it, or to see whether it is there), a
// login store, or the Keychain. It is nil in cpb; a test sets it to prove
// that a code path touches none.
var CredentialTouch func(what string)

// What CredentialTouch is called with.
const (
	TouchTokenFile = "the token file"
	TouchStore     = "a login store"
	TouchKeychain  = "the Keychain"
)

func touch(what string) {
	if CredentialTouch != nil {
		CredentialTouch(what)
	}
}

// StorePath is a config directory's login store, its .credentials.json.
// Every use of a store starts from it, so CredentialTouch sees each one.
func StorePath(dir string) string {
	touch(TouchStore)
	return filepath.Join(dir, CredentialsFileName)
}

// keychainProbe and keychainSecret are the Keychain lookups, through the
// replaceable functions tests stub.
func keychainProbe(account, service string) KeychainState {
	touch(TouchKeychain)
	return KeychainProbe(account, service)
}

func keychainSecret(service string) ([]byte, error) {
	touch(TouchKeychain)
	return findGenericPassword(service)
}

package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// A symlink to the global config directory names the same directory; string
// comparison of absolute paths let both mutations through to the global store.
func TestQuarantineRefusesGlobalStoreThroughSymlinkedDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	global := filepath.Join(home, ".claude")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	store := writeStore(t, global, `{`+grantJSON+`}`)
	alias := filepath.Join(home, "linked-global")
	if err := os.Symlink(global, alias); err != nil {
		t.Fatal(err)
	}

	if err := QuarantineStoredOAuth(alias); err != nil {
		t.Fatalf("quarantine through the alias: %v", err)
	}
	if _, present := readStore(t, store)["claudeAiOauth"]; !present {
		t.Fatal("the GLOBAL grant was quarantined through a symlinked directory")
	}
}

func TestLinkCredentialsRefusesGlobalStoreThroughSymlinkedDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	global := filepath.Join(home, ".claude")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	store := writeStore(t, global, testCreds)
	alias := filepath.Join(home, "linked-global")
	if err := os.Symlink(global, alias); err != nil {
		t.Fatal(err)
	}

	if err := LinkCredentials(alias, store); err != nil {
		t.Fatalf("link through the alias: %v", err)
	}
	info, err := os.Lstat(store)
	if err != nil {
		t.Fatalf("global store gone: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("the global store was replaced by a symlink to itself")
	}
	if got, _ := os.ReadFile(store); string(got) != testCreds {
		t.Fatalf("global store content changed: %q", got)
	}
}

// The same protection when ~/.claude itself is the symlink and the playbook
// path is the resolved target: identity, not spelling.
func TestSameDirSeesThroughSymlinkedGlobal(t *testing.T) {
	home := t.TempDir()
	real := filepath.Join(home, "real-claude")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(home, ".claude")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if !isGlobalConfigDir(real) {
		t.Fatal("the resolved target of a symlinked ~/.claude was not recognised as the global dir")
	}
	if isGlobalConfigDir(filepath.Join(home, "elsewhere")) {
		t.Fatal("a non-existent path was taken for the global dir")
	}
}

// The reverse alias: ~/.claude/.credentials.json is a symlink INTO the config
// dir being quarantined. Its regular file is the global store's target.
func TestQuarantineRefusesGlobalStoreTargetInAnotherDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	shared := filepath.Join(home, "configs", "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	store := writeStore(t, shared, `{`+grantJSON+`}`)
	global := filepath.Join(home, ".claude")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(store, filepath.Join(global, CredentialsFileName)); err != nil {
		t.Fatal(err)
	}

	if err := QuarantineStoredOAuth(shared); err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	if _, present := readStore(t, store)["claudeAiOauth"]; !present {
		t.Fatal("the global store's target was stripped through the directory that holds it")
	}
}

// The ordinary playbook layout under a token launch: .credentials.json is an
// OUTGOING symlink to the real global store under $HOME/.claude. It resolves
// to the global file, and must still be detached -- the fixture puts the
// global store where it really lives, which earlier symlink tests did not.
func TestQuarantineStillDetachesOutgoingLinkToRealGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	global := filepath.Join(home, ".claude")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	store := writeStore(t, global, `{`+grantJSON+`}`)
	pb := filepath.Join(home, "playbook")
	if err := os.MkdirAll(pb, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(pb, CredentialsFileName)
	if err := os.Symlink(store, link); err != nil {
		t.Fatal(err)
	}

	if err := QuarantineStoredOAuth(pb); err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatal("the outgoing link to the global store was not detached; the stale grant stays adoptable")
	}
	if _, present := readStore(t, store)["claudeAiOauth"]; !present {
		t.Fatal("the global store was stripped through the link")
	}
}

// A symlink chain the global store passes through: ~/.claude/.credentials.json
// -> shared/.credentials.json -> real store. Linking `shared` to the global
// path would close a cycle; it must recognise the same file and do nothing.
func TestLinkCredentialsDoesNotCloseSymlinkCycle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	real := filepath.Join(home, "real-store.json")
	if err := os.WriteFile(real, []byte(testCreds), 0o600); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(home, "shared")
	global := filepath.Join(home, ".claude")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	sharedLink := filepath.Join(shared, CredentialsFileName)
	if err := os.Symlink(real, sharedLink); err != nil {
		t.Fatal(err)
	}
	globalLink := filepath.Join(global, CredentialsFileName)
	if err := os.Symlink(sharedLink, globalLink); err != nil {
		t.Fatal(err)
	}

	if err := LinkCredentials(shared, globalLink); err != nil {
		t.Fatalf("link: %v", err)
	}
	if got, _ := os.Readlink(sharedLink); got != real {
		t.Fatalf("shared link rewritten to %q; a cycle through the global path", got)
	}
	if body, err := os.ReadFile(globalLink); err != nil || string(body) != testCreds {
		t.Fatalf("global store unreadable after linking: %v %q", err, body)
	}
}

// Quarantine of the MIDDLE directory in a chain the global store passes
// through (~/.claude/.credentials.json -> shared/.credentials.json -> real
// store) must leave that link alone, for a grant-only store and for one with
// sibling keys; the ordinary outgoing playbook link is still detached.
func TestQuarantineLeavesGlobalChainLinksAlone(t *testing.T) {
	for name, body := range map[string]string{
		"grant only":   `{` + grantJSON + `}`,
		"sibling keys": `{` + grantJSON + `,` + mcpJSON + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			real := filepath.Join(home, "real-store.json")
			if err := os.WriteFile(real, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			shared := filepath.Join(home, "shared")
			global := filepath.Join(home, ".claude")
			if err := os.MkdirAll(shared, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(global, 0o755); err != nil {
				t.Fatal(err)
			}
			sharedLink := filepath.Join(shared, CredentialsFileName)
			if err := os.Symlink(real, sharedLink); err != nil {
				t.Fatal(err)
			}
			globalLink := filepath.Join(global, CredentialsFileName)
			if err := os.Symlink(sharedLink, globalLink); err != nil {
				t.Fatal(err)
			}

			if err := QuarantineStoredOAuth(shared); err != nil {
				t.Fatalf("quarantine: %v", err)
			}
			if got, err := os.Readlink(sharedLink); err != nil || got != real {
				t.Fatalf("middle link of the global chain was touched: %q %v", got, err)
			}
			if _, present := readStore(t, globalLink)["claudeAiOauth"]; !present {
				t.Fatal("the global grant is gone or unreadable through the chain")
			}

			// An ordinary playbook linking to the global path is NOT on the
			// chain: it must still be detached.
			pb := filepath.Join(home, "playbook")
			if err := os.MkdirAll(pb, 0o755); err != nil {
				t.Fatal(err)
			}
			pbLink := filepath.Join(pb, CredentialsFileName)
			if err := os.Symlink(globalLink, pbLink); err != nil {
				t.Fatal(err)
			}
			if err := QuarantineStoredOAuth(pb); err != nil {
				t.Fatalf("quarantine playbook: %v", err)
			}
			// Detached: gone for a grant-only store, or re-materialised as
			// this dir's own regular file holding only the sibling keys.
			if info, err := os.Lstat(pbLink); err == nil && info.Mode()&os.ModeSymlink != 0 {
				t.Fatal("the ordinary outgoing link was not detached")
			} else if err == nil {
				if _, present := readStore(t, pbLink)["claudeAiOauth"]; present {
					t.Fatal("re-materialised playbook store still holds the grant")
				}
			}
			if _, present := readStore(t, globalLink)["claudeAiOauth"]; !present {
				t.Fatal("detaching the outgoing link damaged the global chain")
			}
		})
	}
}

// A symlinked global directory plus RELATIVE chain links: ~/.claude ->
// vault/config, vault/config/.credentials.json -> ../shared/.credentials.json,
// vault/shared/.credentials.json -> vault/store.json. Lexical joining of ".."
// against the symlinked parent would look for ~/shared instead and miss the
// hop, so quarantine of vault/shared would detach a link on the global chain.
func TestQuarantineChainWalkResolvesRelativeHopsPhysically(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	vault := filepath.Join(home, "vault")
	for _, d := range []string{filepath.Join(vault, "config"), filepath.Join(vault, "shared")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	store := filepath.Join(vault, "store.json")
	if err := os.WriteFile(store, []byte(`{`+grantJSON+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sharedLink := filepath.Join(vault, "shared", CredentialsFileName)
	if err := os.Symlink(store, sharedLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "shared", CredentialsFileName), filepath.Join(vault, "config", CredentialsFileName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(vault, "config"), filepath.Join(home, ".claude")); err != nil {
		t.Fatal(err)
	}

	if err := QuarantineStoredOAuth(filepath.Join(vault, "shared")); err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	if got, err := os.Readlink(sharedLink); err != nil || got != store {
		t.Fatalf("chain link detached: %q %v", got, err)
	}
	if _, present := readStore(t, filepath.Join(home, ".claude", CredentialsFileName))["claudeAiOauth"]; !present {
		t.Fatal("global grant unreachable through the chain")
	}
}

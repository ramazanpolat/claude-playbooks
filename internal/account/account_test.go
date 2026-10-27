package account

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	ref := Auth{Kind: AuthToken, Ref: "keychain:glm"}
	cases := []struct {
		name string
		a    Account
		want string // "" valid, else a substring of the error
	}{
		{"gateway with key", Account{Provider: Gateway, BaseURL: "http://localhost:4000/v1", Auth: ref}, ""},
		{"gateway keyless", Account{Provider: Gateway, BaseURL: "http://127.0.0.1:4000", Auth: Auth{Kind: None}}, ""},
		{"gateway needs url", Account{Provider: Gateway, Auth: ref}, "needs base_url"},
		{"gateway http elsewhere", Account{Provider: Gateway, BaseURL: "http://router.example/v1", Auth: ref}, "unencrypted"},
		{"gateway userinfo", Account{Provider: Gateway, BaseURL: "https://u:p@router.example/v1", Auth: ref}, "user or password"},
		{"gateway claude login untrusted", Account{Provider: Gateway, BaseURL: "https://gw.example", Auth: Auth{Kind: ClaudeLogin}}, "claude_login_trusted"},
		{"gateway claude login trusted", Account{Provider: Gateway, BaseURL: "https://gw.example", ClaudeLoginTrusted: true, Auth: Auth{Kind: ClaudeLogin}}, ""},
		{"gateway claude token trusted http", Account{Provider: Gateway, BaseURL: "http://localhost:4000", ClaudeLoginTrusted: true, Auth: Auth{Kind: ClaudeToken, Ref: "keychain:t"}}, "only over https"},
		{"gateway cloud", Account{Provider: Gateway, BaseURL: "https://gw.example", Auth: Auth{Kind: Cloud}}, "not auth = 'cloud'"},
		{"anthropic login", Account{Provider: Anthropic, Auth: Auth{Kind: ClaudeLogin}}, ""},
		{"anthropic token", Account{Provider: Anthropic, Auth: Auth{Kind: ClaudeToken, Ref: "keychain:t"}}, ""},
		{"anthropic api url", Account{Provider: Anthropic, BaseURL: "https://api.anthropic.com", Auth: Auth{Kind: APIKey, Ref: "keychain:k"}}, ""},
		{"anthropic foreign url", Account{Provider: Anthropic, BaseURL: "https://router.example", Auth: Auth{Kind: APIKey, Ref: "keychain:k"}}, "provider = 'gateway'"},
		{"anthropic odd port", Account{Provider: Anthropic, BaseURL: "https://api.anthropic.com:8443", Auth: Auth{Kind: APIKey, Ref: "keychain:k"}}, "provider = 'gateway'"},
		{"anthropic none", Account{Provider: Anthropic, Auth: Auth{Kind: None}}, "not auth = 'none'"},
		{"anthropic trusted", Account{Provider: Anthropic, ClaudeLoginTrusted: true, Auth: Auth{Kind: ClaudeLogin}}, "gateway account only"},
		{"bedrock cloud", Account{Provider: Bedrock, Auth: Auth{Kind: Cloud}}, ""},
		{"vertex key", Account{Provider: Vertex, Auth: ref}, "auth = 'cloud'"},
		{"no provider", Account{Auth: ref}, "no provider"},
		{"bad provider", Account{Provider: "openai", Auth: ref}, "unknown provider"},
		{"no credential", Account{Provider: Anthropic}, "no credential"},
		{"ref and value", Account{Provider: Anthropic, Auth: Auth{Kind: APIKey, Ref: "keychain:k", Value: "x"}}, "not both"},
		{"not a ref", Account{Provider: Anthropic, Auth: Auth{Kind: APIKey, Ref: "sk-ant-123"}}, "not a secret reference"},
		{"plaintext value", Account{Provider: Anthropic, Auth: Auth{Kind: APIKey, Value: "sk-ant-123"}}, ""},
		{"helper", Account{Provider: Anthropic, Auth: Auth{Kind: Helper, Helper: "my-key-helper"}}, ""},
		{"helper empty", Account{Provider: Anthropic, Auth: Auth{Kind: Helper}}, "needs a command"},
		{"login holds a ref", Account{Provider: Anthropic, Auth: Auth{Kind: ClaudeLogin, Ref: "keychain:x"}}, "holds nothing"},
		{"bad access", Account{Provider: Anthropic, Access: "some", Auth: Auth{Kind: ClaudeLogin}}, "access takes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.a.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
			for _, secret := range []string{"sk-ant-123", "u:p", "keychain:glm"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error quotes a credential: %v", err)
				}
			}
		})
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), DirName)
	in := &Account{
		Name: "glm", Description: "GLM through the router", Provider: Gateway,
		BaseURL: "http://localhost:4000/v1", Identity: "me@example.com", Access: AccessListed,
		DefaultModel: "glm-5.3", HaikuModel: "glm-5.3-flash",
		Auth: Auth{Kind: AuthToken, Ref: "keychain:glm"},
	}
	if err := Write(dir, in); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "glm.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", info.Mode().Perm())
	}
	out, err := Read(dir, "glm")
	if err != nil || out == nil {
		t.Fatalf("read: %v %v", out, err)
	}
	if *out != *in {
		t.Fatalf("round trip:\n got %+v\nwant %+v", *out, *in)
	}
	if a, err := Read(dir, "nope"); a != nil || err != nil {
		t.Fatalf("missing account: %v %v", a, err)
	}
	if _, err := Must(dir, "nope"); !errors.Is(err, ErrAccount) {
		t.Fatalf("Must of a missing account: %v", err)
	}
}

func TestReadRefusesInvalidAndUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name+FileExt), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("unknown", "provider = \"anthropic\"\nnot_a_key = 1\n[auth]\nkind = \"claude_login\"\n")
	if _, err := Read(dir, "unknown"); err == nil {
		t.Fatal("an unknown key was accepted")
	}
	write("broken", "provider = \"gateway\"\n[auth]\nkind = \"none\"\n")
	if _, err := Read(dir, "broken"); err == nil || !strings.Contains(err.Error(), "needs base_url") {
		t.Fatalf("an invalid account: %v", err)
	}
	if _, err := List(dir); err == nil {
		t.Fatal("List hid an unreadable account")
	}
}

func TestNames(t *testing.T) {
	for _, bad := range []string{"machine", "MACHINE", "", "a b", "../x", ".hidden"} {
		if ValidateName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, good := range []string{"glm", "glm-5.3", "claude_work", "w2"} {
		if err := ValidateName(good); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
}

func TestDefaultMarker(t *testing.T) {
	dir := filepath.Join(t.TempDir(), DirName)
	if name, err := Default(dir); name != "" || err != nil {
		t.Fatalf("no marker: %q %v", name, err)
	}
	if err := WriteDefault(dir, "work"); err != nil {
		t.Fatal(err)
	}
	if name, err := Default(dir); name != "work" || err != nil {
		t.Fatalf("marker: %q %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, DefaultMarker), []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Default(dir); err == nil {
		t.Fatal("an empty marker read as no default")
	}
	if err := WriteDefault(dir, ""); err != nil {
		t.Fatal(err)
	}
	if name, err := Default(dir); name != "" || err != nil {
		t.Fatalf("cleared: %q %v", name, err)
	}
	// The marker is not an account: List skips it.
	if err := WriteDefault(dir, "work"); err != nil {
		t.Fatal(err)
	}
	if got, err := List(dir); err != nil || len(got) != 0 {
		t.Fatalf("List: %v %v", got, err)
	}
}

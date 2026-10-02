package cmd

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// seamBackend is a sandboxBackend whose secret returns a placeholder of its
// own choosing (inside) and that can delete a mapping (revoke): the seam's
// contract, not sbx's particular behaviour. It records the secret and
// revoke calls.
type seamBackend struct {
	inside     string
	fail       bool
	registered map[string]string
	calls      []string
}

func (b *seamBackend) names() ([]string, error)                    { return nil, nil }
func (b *seamBackend) mounts(string) ([]string, error)             { return nil, nil }
func (b *seamBackend) create(string, bool, bool, []string) error   { return nil }
func (b *seamBackend) allowNetwork(string, string) error           { return nil }
func (b *seamBackend) shell(string, string) error                  { return nil }
func (b *seamBackend) shellOutput(string, string) (string, error)  { return "", nil }
func (b *seamBackend) secrets(string) (map[string]string, error)   { return b.registered, nil }
func (b *seamBackend) attach(string, []string, bool, string) error { return nil }
func (b *seamBackend) remove(string) error                         { return nil }
func (b *seamBackend) homeDir() string                             { return "/sandbox" }
func (b *seamBackend) hostAlias() string                           { return "host.example.internal" }
func (b *seamBackend) policyHost(host string) string               { return host }

func (b *seamBackend) secret(name, host, env, value string) (string, error) {
	b.calls = append(b.calls, "secret "+name+" "+host+" "+env)
	if b.fail {
		return "", fmt.Errorf("backend refused %s", value)
	}
	return b.inside, nil
}

func (b *seamBackend) revoke(name, host, env string, registered map[string]string) (bool, error) {
	b.calls = append(b.calls, "revoke "+name+" "+host+" "+env)
	_, ok := registered[env]
	return ok, nil
}

func TestInjectSecretsBackendOwnedPlaceholder(t *testing.T) {
	// A key the backend injects itself leaves the attach environment; the
	// rest of it is kept, in order. A key no longer present is revoked
	// through the backend, which decides whether there was a mapping.
	b := &seamBackend{registered: map[string]string{"ANTHROPIC_AUTH_TOKEN": "provider-1"}}
	env := []string{"ANTHROPIC_BASE_URL=http://router.local:9/v1", "ANTHROPIC_API_KEY=real-key", "MODEL=glm"}
	var got []string
	var err error
	stderr := captureStderr(t, func() { got, err = injectSecrets(b, "cpb-x", env, "") })
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ANTHROPIC_BASE_URL=http://router.local:9/v1", "MODEL=glm"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attach env: got %q, want %q", got, want)
	}
	if want := []string{"revoke cpb-x router.local ANTHROPIC_AUTH_TOKEN", "secret cpb-x router.local ANTHROPIC_API_KEY"}; !reflect.DeepEqual(b.calls, want) {
		t.Fatalf("calls: got %q, want %q", b.calls, want)
	}
	for _, want := range []string{"Secret ANTHROPIC_AUTH_TOKEN revoked at the proxy", "Secret ANTHROPIC_API_KEY stays on the host: injected at the proxy for router.local"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr lacks %q: %s", want, stderr)
		}
	}
	if strings.Contains(stderr, "real-key") {
		t.Fatalf("the value reached stderr: %s", stderr)
	}
	// Nothing registered and nothing present: no revoke message.
	b = &seamBackend{}
	stderr = captureStderr(t, func() { _, err = injectSecrets(b, "cpb-x", []string{"MODEL=glm"}, "") })
	if err != nil || strings.Contains(stderr, "revoked") {
		t.Fatalf("nothing to revoke: err %v, stderr %s", err, stderr)
	}
	// A failed registration refuses the launch, the value redacted.
	b = &seamBackend{fail: true}
	_, err = injectSecrets(b, "cpb-x", []string{"ANTHROPIC_API_KEY=real-key"}, "")
	if err == nil || !strings.Contains(err.Error(), "<redacted>") || strings.Contains(err.Error(), "real-key") {
		t.Fatalf("failed registration: %v", err)
	}
}

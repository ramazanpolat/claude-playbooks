package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeReleaseServer serves the GitHub "latest release" JSON and a stand-in
// asset. The asset is a tiny shell script that behaves like the new binary:
// `<asset> --version` prints the version string, which verifyBinary checks.
func fakeReleaseServer(t *testing.T, tag, versionOutput string) *httptest.Server {
	t.Helper()
	asset := fmt.Sprintf("claude-playbook-%s-%s", runtime.GOOS, runtime.GOARCH)
	script := "#!/bin/sh\necho \"" + versionOutput + "\"\n"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			fmt.Fprintf(w, `{"tag_name":%q}`, tag)
		case strings.HasSuffix(r.URL.Path, "/"+tag+"/"+asset):
			_, _ = w.Write([]byte(script))
		default:
			http.NotFound(w, r)
		}
	}))
}

func newExecutable(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "claude-playbook")
	if err := os.WriteFile(exe, []byte("OLD-BINARY"), 0755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func baseConfig(exe string, srv *httptest.Server) selfUpdateConfig {
	return selfUpdateConfig{
		repo:         "ramazanpolat/claude-playbooks",
		apiBase:      srv.URL,
		downloadBase: srv.URL,
		goos:         runtime.GOOS,
		goarch:       runtime.GOARCH,
		execPath:     exe,
		httpClient:   srv.Client(),
		verifyExec:   true,
	}
}

func TestSelfUpdateReplacesBinary(t *testing.T) {
	tag := "v9.9.9"
	srv := fakeReleaseServer(t, tag, "claude-playbook version "+tag)
	defer srv.Close()
	exe := newExecutable(t)

	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v0.0.1"

	var out bytes.Buffer
	if err := selfUpdate(&out, cfg); err != nil {
		t.Fatalf("selfUpdate: %v\noutput:\n%s", err, out.String())
	}
	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "OLD-BINARY") {
		t.Fatalf("binary was not replaced: %q", got)
	}
	if !strings.Contains(out.String(), tag) {
		t.Fatalf("output did not report the new version:\n%s", out.String())
	}
	// The replacement must remain executable.
	if info, err := os.Stat(exe); err != nil || info.Mode()&0111 == 0 {
		t.Fatalf("replacement is not executable: mode=%v err=%v", info.Mode(), err)
	}
}

func TestSelfUpdateAlreadyCurrentSkips(t *testing.T) {
	tag := "v9.9.9"
	srv := fakeReleaseServer(t, tag, "claude-playbook version "+tag)
	defer srv.Close()
	exe := newExecutable(t)

	cfg := baseConfig(exe, srv)
	cfg.currentVersion = tag // already on latest

	var out bytes.Buffer
	if err := selfUpdate(&out, cfg); err != nil {
		t.Fatalf("selfUpdate: %v", err)
	}
	if !strings.Contains(out.String(), "Already up to date") {
		t.Fatalf("expected up-to-date message, got:\n%s", out.String())
	}
	if got, _ := os.ReadFile(exe); string(got) != "OLD-BINARY" {
		t.Fatalf("binary was replaced despite being current: %q", got)
	}
}

func TestSelfUpdateForceReinstalls(t *testing.T) {
	tag := "v9.9.9"
	srv := fakeReleaseServer(t, tag, "claude-playbook version "+tag)
	defer srv.Close()
	exe := newExecutable(t)

	cfg := baseConfig(exe, srv)
	cfg.currentVersion = tag // already on latest...
	cfg.force = true         // ...but force reinstall

	var out bytes.Buffer
	if err := selfUpdate(&out, cfg); err != nil {
		t.Fatalf("selfUpdate: %v\noutput:\n%s", err, out.String())
	}
	if got, _ := os.ReadFile(exe); strings.Contains(string(got), "OLD-BINARY") {
		t.Fatalf("force did not reinstall: %q", got)
	}
}

func TestSelfUpdateCheckOnlyDoesNotReplace(t *testing.T) {
	tag := "v9.9.9"
	srv := fakeReleaseServer(t, tag, "claude-playbook version "+tag)
	defer srv.Close()
	exe := newExecutable(t)

	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v0.0.1"
	cfg.checkOnly = true

	var out bytes.Buffer
	if err := selfUpdate(&out, cfg); err != nil {
		t.Fatalf("selfUpdate: %v", err)
	}
	if !strings.Contains(out.String(), "update is available") {
		t.Fatalf("expected an update-available message, got:\n%s", out.String())
	}
	if got, _ := os.ReadFile(exe); string(got) != "OLD-BINARY" {
		t.Fatalf("check-only replaced the binary: %q", got)
	}
}

func TestSelfUpdateVerifyRejectsBadDownload(t *testing.T) {
	tag := "v9.9.9"
	// The asset reports the WRONG version -> verification must reject it.
	srv := fakeReleaseServer(t, tag, "claude-playbook version v0.0.0")
	defer srv.Close()
	exe := newExecutable(t)

	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v0.0.1"

	var out bytes.Buffer
	err := selfUpdate(&out, cfg)
	if err == nil || !strings.Contains(err.Error(), "verification") {
		t.Fatalf("expected a verification failure, got err=%v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "OLD-BINARY" {
		t.Fatalf("a failed verification still replaced the binary: %q", got)
	}
}

// sumsReleaseServer is fakeReleaseServer plus a SHA256SUMS route whose
// content is produced by sums(asset, script).
func sumsReleaseServer(t *testing.T, tag, versionOutput string, sums func(asset, script string) string) *httptest.Server {
	t.Helper()
	asset := fmt.Sprintf("claude-playbook-%s-%s", runtime.GOOS, runtime.GOARCH)
	script := "#!/bin/sh\necho \"" + versionOutput + "\"\n"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			fmt.Fprintf(w, `{"tag_name":%q}`, tag)
		case strings.HasSuffix(r.URL.Path, "/"+tag+"/"+asset):
			_, _ = w.Write([]byte(script))
		case strings.HasSuffix(r.URL.Path, "/"+tag+"/SHA256SUMS"):
			_, _ = w.Write([]byte(sums(asset, script)))
		default:
			http.NotFound(w, r)
		}
	}))
}

func scriptDigest(script string) string {
	h := sha256.Sum256([]byte(script))
	return hex.EncodeToString(h[:])
}

func TestSelfUpdateChecksumVerifies(t *testing.T) {
	srv := sumsReleaseServer(t, "v9.9.9", "claude-playbook version v9.9.9", func(asset, script string) string {
		return scriptDigest(script) + "  " + asset + "\n"
	})
	defer srv.Close()
	exe := newExecutable(t)
	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v1.0.0"

	var out strings.Builder
	if err := selfUpdate(&out, cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Checksum verified (sha256).") {
		t.Fatalf("no verification line in output:\n%s", out.String())
	}
}

func TestSelfUpdateChecksumMismatchAborts(t *testing.T) {
	srv := sumsReleaseServer(t, "v9.9.9", "claude-playbook version v9.9.9", func(asset, script string) string {
		return strings.Repeat("ab", 32) + "  " + asset + "\n"
	})
	defer srv.Close()
	exe := newExecutable(t)
	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v1.0.0"

	var out strings.Builder
	err := selfUpdate(&out, cfg)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
	data, rerr := os.ReadFile(exe)
	if rerr != nil || string(data) != "OLD-BINARY" {
		t.Fatalf("binary replaced despite mismatch: %q %v", data, rerr)
	}
}

func TestSelfUpdateChecksumMalformedWarnsAndProceeds(t *testing.T) {
	srv := sumsReleaseServer(t, "v9.9.9", "claude-playbook version v9.9.9", func(asset, script string) string {
		return "1234  " + asset + "\n"
	})
	defer srv.Close()
	exe := newExecutable(t)
	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v1.0.0"

	var out strings.Builder
	if err := selfUpdate(&out, cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "malformed SHA256SUMS") {
		t.Fatalf("no malformed warning:\n%s", out.String())
	}
}

func TestSelfUpdateNoSumsWarnsAndProceeds(t *testing.T) {
	srv := fakeReleaseServer(t, "v9.9.9", "claude-playbook version v9.9.9")
	defer srv.Close()
	exe := newExecutable(t)
	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v1.0.0"

	var out strings.Builder
	if err := selfUpdate(&out, cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "skipping checksum verification") {
		t.Fatalf("no skip warning:\n%s", out.String())
	}
}

func TestSelfUpdateChecksumBinaryModeEntryVerifies(t *testing.T) {
	srv := sumsReleaseServer(t, "v9.9.9", "claude-playbook version v9.9.9", func(asset, script string) string {
		return scriptDigest(script) + " *" + asset + "\n"
	})
	defer srv.Close()
	exe := newExecutable(t)
	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v1.0.0"

	var out strings.Builder
	if err := selfUpdate(&out, cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Checksum verified (sha256).") {
		t.Fatalf("binary-mode entry not verified:\n%s", out.String())
	}
}

func TestSelfUpdateOversizedSumsWarnsAndProceeds(t *testing.T) {
	srv := sumsReleaseServer(t, "v9.9.9", "claude-playbook version v9.9.9", func(asset, script string) string {
		return strings.Repeat("x", 70*1024)
	})
	defer srv.Close()
	exe := newExecutable(t)
	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v1.0.0"

	var out strings.Builder
	if err := selfUpdate(&out, cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "exceeds") {
		t.Fatalf("no oversize warning:\n%s", out.String())
	}
}

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

// fakeReleaseServer serves a GitHub release list of one release and a
// stand-in asset. The asset is a tiny shell script that behaves like the new binary:
// `<asset> --version` prints the version string, which verifyBinary checks.
func fakeReleaseServer(t *testing.T, tag, versionOutput string) *httptest.Server {
	t.Helper()
	asset := fmt.Sprintf("cpb-%s-%s", runtime.GOOS, runtime.GOARCH)
	script := "#!/bin/sh\necho \"" + versionOutput + "\"\n"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases"):
			fmt.Fprintf(w, `[{"tag_name":%q,"draft":false,"prerelease":false}]`, tag)
		case strings.HasSuffix(r.URL.Path, "/"+tag+"/"+asset):
			_, _ = w.Write([]byte(script))
		default:
			http.NotFound(w, r)
		}
	}))
}

func newExecutable(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "cpb")
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
	srv := fakeReleaseServer(t, tag, "cpb version "+tag)
	defer srv.Close()
	exe := newExecutable(t)

	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v9.0.1"

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
	srv := fakeReleaseServer(t, tag, "cpb version "+tag)
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
	srv := fakeReleaseServer(t, tag, "cpb version "+tag)
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
	srv := fakeReleaseServer(t, tag, "cpb version "+tag)
	defer srv.Close()
	exe := newExecutable(t)

	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v9.0.1"
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
	srv := fakeReleaseServer(t, tag, "cpb version v0.0.0")
	defer srv.Close()
	exe := newExecutable(t)

	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v9.0.1"

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
	asset := fmt.Sprintf("cpb-%s-%s", runtime.GOOS, runtime.GOARCH)
	script := "#!/bin/sh\necho \"" + versionOutput + "\"\n"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases"):
			fmt.Fprintf(w, `[{"tag_name":%q,"draft":false,"prerelease":false}]`, tag)
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
	srv := sumsReleaseServer(t, "v9.9.9", "cpb version v9.9.9", func(asset, script string) string {
		return scriptDigest(script) + "  " + asset + "\n"
	})
	defer srv.Close()
	exe := newExecutable(t)
	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v9.0.0"

	var out strings.Builder
	if err := selfUpdate(&out, cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Checksum verified (sha256).") {
		t.Fatalf("no verification line in output:\n%s", out.String())
	}
}

func TestSelfUpdateChecksumMismatchAborts(t *testing.T) {
	srv := sumsReleaseServer(t, "v9.9.9", "cpb version v9.9.9", func(asset, script string) string {
		return strings.Repeat("ab", 32) + "  " + asset + "\n"
	})
	defer srv.Close()
	exe := newExecutable(t)
	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v9.0.0"

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

// An entry for the asset that is not a hash cannot vouch for it: refused,
// and the running binary stays.
func TestSelfUpdateChecksumMalformedRefuses(t *testing.T) {
	srv := sumsReleaseServer(t, "v9.9.9", "cpb version v9.9.9", func(asset, script string) string {
		return "1234  " + asset + "\n"
	})
	defer srv.Close()
	exe := newExecutable(t)
	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v9.0.0"

	var out strings.Builder
	err := selfUpdate(&out, cfg)
	if err == nil || !strings.Contains(err.Error(), "malformed SHA256SUMS entry") {
		t.Fatalf("expected a malformed-entry refusal, got %v\n%s", err, out.String())
	}
	if data, rerr := os.ReadFile(exe); rerr != nil || string(data) != "OLD-BINARY" {
		t.Fatalf("binary replaced despite a malformed entry: %q %v", data, rerr)
	}
}

// Two different hashes for the asset are conflicting evidence: a mismatch,
// even when one of them is right. The same hash listed twice verifies.
func TestSelfUpdateChecksumConflictingAndDuplicateEntries(t *testing.T) {
	srv := sumsReleaseServer(t, "v9.9.9", "cpb version v9.9.9", func(asset, script string) string {
		return scriptDigest(script) + "  " + asset + "\n" + strings.Repeat("ab", 32) + "  " + asset + "\n"
	})
	defer srv.Close()
	exe := newExecutable(t)
	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v9.0.0"
	var out strings.Builder
	err := selfUpdate(&out, cfg)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") || !strings.Contains(err.Error(), "conflicting hashes") {
		t.Fatalf("expected a conflicting-hashes mismatch, got %v", err)
	}
	if data, rerr := os.ReadFile(exe); rerr != nil || string(data) != "OLD-BINARY" {
		t.Fatalf("binary replaced despite conflicting hashes: %q %v", data, rerr)
	}

	dup := sumsReleaseServer(t, "v9.9.9", "cpb version v9.9.9", func(asset, script string) string {
		d := scriptDigest(script)
		return d + "  " + asset + "\n" + strings.ToUpper(d) + " *" + asset + "\n"
	})
	defer dup.Close()
	exe2 := newExecutable(t)
	cfg2 := baseConfig(exe2, dup)
	cfg2.currentVersion = "v9.0.0"
	var out2 strings.Builder
	if err := selfUpdate(&out2, cfg2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2.String(), "Checksum verified (sha256).") {
		t.Fatalf("a duplicated identical entry did not verify:\n%s", out2.String())
	}
}

func TestSelfUpdateNoSumsWarnsAndProceeds(t *testing.T) {
	srv := fakeReleaseServer(t, "v9.9.9", "cpb version v9.9.9")
	defer srv.Close()
	exe := newExecutable(t)
	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v9.0.0"

	var out strings.Builder
	if err := selfUpdate(&out, cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "skipping checksum verification") {
		t.Fatalf("no skip warning:\n%s", out.String())
	}
}

func TestSelfUpdateChecksumBinaryModeEntryVerifies(t *testing.T) {
	srv := sumsReleaseServer(t, "v9.9.9", "cpb version v9.9.9", func(asset, script string) string {
		return scriptDigest(script) + " *" + asset + "\n"
	})
	defer srv.Close()
	exe := newExecutable(t)
	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v9.0.0"

	var out strings.Builder
	if err := selfUpdate(&out, cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Checksum verified (sha256).") {
		t.Fatalf("binary-mode entry not verified:\n%s", out.String())
	}
}

func TestSelfUpdateOversizedSumsWarnsAndProceeds(t *testing.T) {
	srv := sumsReleaseServer(t, "v9.9.9", "cpb version v9.9.9", func(asset, script string) string {
		return strings.Repeat("x", 70*1024)
	})
	defer srv.Close()
	exe := newExecutable(t)
	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v9.0.0"

	var out strings.Builder
	if err := selfUpdate(&out, cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "exceeds") {
		t.Fatalf("no oversize warning:\n%s", out.String())
	}
}

func TestIsNixStorePath(t *testing.T) {
	for p, want := range map[string]bool{
		"/nix/store/2y7j-cpb-3.17.0/bin/cpb":    true,
		"/home/u/.local/bin/cpb":                false,
		"/nix/var/nix/profiles/default/bin/cpb": false,
		"/tmp/nix/store/x/bin/cpb":              false,
	} {
		if got := isNixStorePath(p); got != want {
			t.Errorf("isNixStorePath(%q) = %v, want %v", p, got, want)
		}
	}
}

// A Nix-managed binary is never replaced: the store is read-only, and the
// generic permission-error advice ("re-run with sudo") would corrupt it.
func TestSelfUpdateRefusesNixManagedBinary(t *testing.T) {
	tag := "v9.9.9"
	srv := fakeReleaseServer(t, tag, "cpb version "+tag)
	defer srv.Close()
	exe := newExecutable(t)

	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v9.0.1"
	cfg.nixManaged = true

	var out bytes.Buffer
	err := selfUpdate(&out, cfg)
	if err == nil || !strings.Contains(err.Error(), "managed by Nix") || !strings.Contains(err.Error(), "devbox.json and run `devbox install`") {
		t.Fatalf("expected a Nix refusal naming devbox, got err=%v out=%s", err, out.String())
	}
	if strings.Contains(err.Error(), "sudo") {
		t.Fatalf("the refusal must not suggest sudo: %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "OLD-BINARY" {
		t.Fatalf("a Nix-managed binary was replaced: %q", got)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".cpb.update-*")); len(left) != 0 {
		t.Fatalf("staged files left behind: %v", left)
	}
}

// --check still reports, and points at devbox rather than at self-update.
func TestSelfUpdateCheckOnlyNixManagedHintsDevbox(t *testing.T) {
	tag := "v9.9.9"
	srv := fakeReleaseServer(t, tag, "cpb version "+tag)
	defer srv.Close()
	exe := newExecutable(t)

	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v9.0.1"
	cfg.checkOnly = true
	cfg.nixManaged = true

	var out bytes.Buffer
	if err := selfUpdate(&out, cfg); err != nil {
		t.Fatalf("selfUpdate: %v", err)
	}
	if !strings.Contains(out.String(), "update is available") || !strings.Contains(out.String(), "devbox.json and run `devbox install`") || strings.Contains(out.String(), "Run 'cpb update'") {
		t.Fatalf("expected the devbox hint instead of the self-update hint, got:\n%s", out.String())
	}
}

// Refused even when already current, and before any network: an up-to-date
// store binary must not answer "Already up to date." as if it could update
// itself (found by an end-to-end run against the flake).
func TestSelfUpdateRefusesNixManagedBeforeLookup(t *testing.T) {
	exe := newExecutable(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	cfg := baseConfig(exe, srv)
	cfg.apiBase = "http://127.0.0.1:1" // nothing listens: a lookup would fail differently
	cfg.currentVersion = "v9.9.9"
	cfg.nixManaged = true

	var out bytes.Buffer
	err := selfUpdate(&out, cfg)
	if err == nil || !strings.Contains(err.Error(), "managed by Nix") {
		t.Fatalf("expected the Nix refusal before any lookup, got err=%v out=%s", err, out.String())
	}
	if strings.Contains(out.String(), "Newest") || strings.Contains(out.String(), "up to date") {
		t.Fatalf("a Nix-managed binary talked about updating itself:\n%s", out.String())
	}
}

// listServer serves the release list as pages (pages[i] is page i+1, with
// a Link header to the next) and, for any tag, an asset whose --version
// prints that tag. hits records every path asked for.
func listServer(t *testing.T, hits *[]string, pages ...string) *httptest.Server {
	t.Helper()
	asset := fmt.Sprintf("cpb-%s-%s", runtime.GOOS, runtime.GOARCH)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			*hits = append(*hits, r.URL.Path)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases"):
			n := 1
			fmt.Sscanf(r.URL.Query().Get("page"), "%d", &n)
			if n < 1 || n > len(pages) {
				http.NotFound(w, r)
				return
			}
			if n < len(pages) {
				w.Header().Set("Link", fmt.Sprintf(`<%s%s?per_page=100&page=%d>; rel="next", <%s%s?page=%d>; rel="last"`, srv.URL, r.URL.Path, n+1, srv.URL, r.URL.Path, len(pages)))
			}
			fmt.Fprint(w, pages[n-1])
		case strings.HasSuffix(r.URL.Path, "/"+asset):
			tag := filepath.Base(filepath.Dir(r.URL.Path))
			fmt.Fprintf(w, "#!/bin/sh\necho \"cpb version %s\"\n", tag)
		default:
			http.NotFound(w, r)
		}
	}))
	return srv
}

// rel is one entry of a release list.
func rel(tag string, flags ...string) string {
	draft, pre := false, false
	for _, f := range flags {
		draft = draft || f == "draft"
		pre = pre || f == "pre"
	}
	return fmt.Sprintf(`{"tag_name":%q,"draft":%v,"prerelease":%v}`, tag, draft, pre)
}

func list(rels ...string) string { return "[" + strings.Join(rels, ",") + "]" }

// By date, newest first: a backport to v4.2 and a v4 hotfix after v5.0.0
// (the newest by date is not the newest by number), pre-releases flagged
// and unflagged, and a draft (seen only with a token).
var majorList = list(rel("v4.2.1"), rel("v5.0.0"), rel("v4.3.1"), rel("v5.1.0-rc1", "pre"), rel("v4.4.0-rc1"), rel("v4.9.9", "draft"), rel("v4.3.0"), rel("v4.2.0"), rel("not-a-version"))

func installed(t *testing.T, exe string) string {
	t.Helper()
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// self-update stays within its major version: the newest v4 by number, a
// hotfix dated after v5.0.0 included, and says once that v5 exists.
func TestSelfUpdateStaysInItsMajor(t *testing.T) {
	srv := listServer(t, nil, majorList)
	defer srv.Close()
	exe := newExecutable(t)
	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v4.1.0"
	var out bytes.Buffer
	if err := selfUpdate(&out, cfg); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(installed(t, exe), "v4.3.1") {
		t.Fatalf("installed %q, want v4.3.1\n%s", installed(t, exe), out.String())
	}
	for _, want := range []string{"Newest v4 release: v4.3.1", "Newest release:  v5.0.0",
		"v5.0.0 is available, a new major version: run `cpb self-update --major` (read its release notes first)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// --major crosses to the newest release of any major version.
func TestSelfUpdateMajorCrosses(t *testing.T) {
	srv := listServer(t, nil, majorList)
	defer srv.Close()
	exe := newExecutable(t)
	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v4.1.0"
	cfg.major = true
	var out bytes.Buffer
	if err := selfUpdate(&out, cfg); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(installed(t, exe), "v5.0.0") || strings.Contains(out.String(), "is available, a new major version") {
		t.Fatalf("installed %q\n%s", installed(t, exe), out.String())
	}
}

// On the newest v4 with a v5 out: up to date, the v5 named, nothing installed.
func TestSelfUpdateNewerMajorStaysPut(t *testing.T) {
	srv := listServer(t, nil, majorList)
	defer srv.Close()
	exe := newExecutable(t)
	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v4.3.1"
	var out bytes.Buffer
	if err := selfUpdate(&out, cfg); err != nil {
		t.Fatal(err)
	}
	if installed(t, exe) != "OLD-BINARY" || !strings.Contains(out.String(), "Already up to date.") || !strings.Contains(out.String(), "v5.0.0 is available, a new major version") {
		t.Fatalf("%q\n%s", installed(t, exe), out.String())
	}
}

// --check reports the newest release of this major and the newest overall.
func TestSelfUpdateCheckReportsBoth(t *testing.T) {
	srv := listServer(t, nil, majorList)
	defer srv.Close()
	exe := newExecutable(t)
	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v4.1.0"
	cfg.checkOnly = true
	var out bytes.Buffer
	if err := selfUpdate(&out, cfg); err != nil {
		t.Fatal(err)
	}
	want := "Current version: v4.1.0\nNewest v4 release: v4.3.1\nNewest release:  v5.0.0\n" +
		"v5.0.0 is available, a new major version: run `cpb self-update --major` (read its release notes first)\n" +
		"An update is available: v4.1.0 -> v4.3.1\nRun 'cpb self-update' to install it.\n"
	if out.String() != want || installed(t, exe) != "OLD-BINARY" {
		t.Fatalf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}

// A running version newer than every release of its major is never
// downgraded, --force included; a pre-release moves to its release.
func TestSelfUpdateNeverDowngrades(t *testing.T) {
	srv := listServer(t, nil, list(rel("v4.3.1"), rel("v4.0.0")))
	defer srv.Close()
	exe := newExecutable(t)
	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v4.5.0"
	cfg.force = true
	var out bytes.Buffer
	if err := selfUpdate(&out, cfg); err != nil || installed(t, exe) != "OLD-BINARY" || !strings.Contains(out.String(), "nothing to install") {
		t.Fatalf("%v %q\n%s", err, installed(t, exe), out.String())
	}
	cfg.currentVersion, cfg.force = "v4.3.1-rc2", false
	out.Reset()
	if err := selfUpdate(&out, cfg); err != nil || !strings.Contains(installed(t, exe), "v4.3.1") {
		t.Fatalf("a pre-release did not move to its release: %v %q\n%s", err, installed(t, exe), out.String())
	}
}

// A dev build has no major version: it needs --major, and --check says so.
func TestSelfUpdateDevNeedsMajor(t *testing.T) {
	srv := listServer(t, nil, majorList)
	defer srv.Close()
	exe := newExecutable(t)
	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "dev"
	var out bytes.Buffer
	if err := selfUpdate(&out, cfg); err == nil || !strings.Contains(err.Error(), "--major") || installed(t, exe) != "OLD-BINARY" {
		t.Fatalf("dev without --major: %v\n%s", err, out.String())
	}
	cfg.checkOnly = true
	out.Reset()
	if err := selfUpdate(&out, cfg); err != nil || !strings.Contains(out.String(), "`cpb self-update --major` installs v5.0.0") {
		t.Fatalf("dev --check: %v\n%s", err, out.String())
	}
	cfg.checkOnly, cfg.major = false, true
	out.Reset()
	if err := selfUpdate(&out, cfg); err != nil || !strings.Contains(installed(t, exe), "v5.0.0") {
		t.Fatalf("dev --major: %v\n%s", err, out.String())
	}
}

// The list is read page by page, following GitHub's Link header.
func TestSelfUpdateFollowsPages(t *testing.T) {
	srv := listServer(t, nil, list(rel("v5.0.0"), rel("v4.0.0")), list(rel("v4.3.1")))
	defer srv.Close()
	exe := newExecutable(t)
	cfg := baseConfig(exe, srv)
	cfg.currentVersion = "v4.0.0"
	var out bytes.Buffer
	if err := selfUpdate(&out, cfg); err != nil || !strings.Contains(installed(t, exe), "v4.3.1") {
		t.Fatalf("%v %q\n%s", err, installed(t, exe), out.String())
	}
}

// A list that cannot be fetched or read fails closed: nothing installed,
// the reason given, and no fallback to /releases/latest, which can name a
// higher major version.
func TestSelfUpdateFailsClosed(t *testing.T) {
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			t.Errorf("asked for /releases/latest")
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"API rate limit exceeded"}`)
	}))
	defer limited.Close()
	away := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<https://elsewhere.invalid/releases?page=2>; rel="next"`)
		fmt.Fprint(w, list(rel("v4.0.0")))
	}))
	defer away.Close()
	many := make([]string, releasePages+1)
	for i := range many {
		many[i] = list(rel(fmt.Sprintf("v4.0.%d", i)))
	}
	for name, c := range map[string]struct {
		srv  *httptest.Server
		want string
	}{
		"rate limit": {limited, "403"},
		"bad JSON":   {listServer(t, nil, `[{"tag_name":`), "not valid JSON"},
		"no release": {listServer(t, nil, list(rel("v5.0.0-rc1", "pre"))), "holds no release"},
		"elsewhere":  {away, "points outside"},
		"too long":   {listServer(t, nil, many...), "longer than"},
	} {
		exe := newExecutable(t)
		cfg := baseConfig(exe, c.srv)
		cfg.currentVersion = "v4.0.0"
		var out bytes.Buffer
		err := selfUpdate(&out, cfg)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "nothing is installed") || installed(t, exe) != "OLD-BINARY" {
			t.Errorf("%s: %v %q", name, err, installed(t, exe))
		}
	}
}

// redirectAPIServer is github.com's latest-release page and GitHub's API in
// one: /releases/latest answers redirect (a status, or 302 to
// .../releases/tag/<tag> when tag is set); the release list answers
// apiStatus, with list as its body on 200; any tag's asset is served. It
// counts the requests to each and keeps the redirect's Authorization.
type redirectAPI struct {
	srv                 *httptest.Server
	redirectHits, lists int
	redirectAuth        string
}

func redirectAPIServer(t *testing.T, tag string, redirect, apiStatus int, list string) *redirectAPI {
	t.Helper()
	ra := &redirectAPI{}
	asset := fmt.Sprintf("cpb-%s-%s", runtime.GOOS, runtime.GOARCH)
	ra.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			ra.redirectHits++
			ra.redirectAuth = r.Header.Get("Authorization")
			repo := strings.TrimSuffix(r.URL.Path, "/releases/latest")
			if redirect == http.StatusFound && tag != "" {
				http.Redirect(w, r, repo+"/releases/tag/"+tag, http.StatusFound)
				return
			}
			if redirect == http.StatusFound {
				http.Redirect(w, r, repo+"/releases", http.StatusFound)
				return
			}
			w.WriteHeader(redirect)
		case strings.HasSuffix(r.URL.Path, "/releases"):
			ra.lists++
			w.WriteHeader(apiStatus)
			if apiStatus == http.StatusOK {
				fmt.Fprint(w, list)
			} else {
				fmt.Fprint(w, `{"message":"API rate limit exceeded"}`)
			}
		case strings.HasSuffix(r.URL.Path, "/"+asset):
			fmt.Fprintf(w, "#!/bin/sh\necho \"cpb version %s\"\n", filepath.Base(filepath.Dir(r.URL.Path)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ra.srv.Close)
	return ra
}

func redirectConfig(exe string, ra *redirectAPI) selfUpdateConfig {
	cfg := baseConfig(exe, ra.srv)
	cfg.latestURL = ra.srv.URL + "/ramazanpolat/claude-playbooks/releases/latest"
	cfg.token = "not-a-token"
	return cfg
}

// The redirect names a release in the running major: it is installed, and
// GitHub's API (answering 403 here) is never asked; the redirect gets no
// token. --check reports it the same way.
func TestSelfUpdateRedirectInsideMajorNeedsNoAPI(t *testing.T) {
	ra := redirectAPIServer(t, "v4.5.0", http.StatusFound, http.StatusForbidden, "")
	exe := newExecutable(t)
	cfg := redirectConfig(exe, ra)
	cfg.currentVersion = "v4.1.0"
	var out bytes.Buffer
	if err := selfUpdate(&out, cfg); err != nil || !strings.Contains(installed(t, exe), "v4.5.0") {
		t.Fatalf("%v %q\n%s", err, installed(t, exe), out.String())
	}
	if ra.lists != 0 || ra.redirectHits != 1 || ra.redirectAuth != "" {
		t.Fatalf("lists %d, redirects %d, redirect auth %q", ra.lists, ra.redirectHits, ra.redirectAuth)
	}
	exe2 := newExecutable(t)
	cfg = redirectConfig(exe2, ra)
	cfg.currentVersion, cfg.checkOnly = "v4.1.0", true
	out.Reset()
	if err := selfUpdate(&out, cfg); err != nil || !strings.Contains(out.String(), "An update is available: v4.1.0 -> v4.5.0") || ra.lists != 0 || installed(t, exe2) != "OLD-BINARY" {
		t.Fatalf("--check: %v lists %d\n%s", err, ra.lists, out.String())
	}
}

// The redirect names a new major version and the API answers 403: nothing
// is installed, and the error names the cause, the pinned form and --major.
func TestSelfUpdateRedirectOutsideMajorRateLimited(t *testing.T) {
	ra := redirectAPIServer(t, "v5.0.0", http.StatusFound, http.StatusForbidden, "")
	exe := newExecutable(t)
	cfg := redirectConfig(exe, ra)
	cfg.currentVersion = "v4.1.0"
	var out bytes.Buffer
	err := selfUpdate(&out, cfg)
	for _, want := range []string{"nothing is installed", "the newest release is v5.0.0, a new major version", "rate-limited the release list (HTTP 403; unauthenticated requests share your IP)", "CPB_UPDATE_VERSION=<tag> cpb self-update", "`cpb self-update --major`", "https://github.com/ramazanpolat/claude-playbooks/releases"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if installed(t, exe) != "OLD-BINARY" || ra.lists != 1 {
		t.Fatalf("installed %q, lists %d", installed(t, exe), ra.lists)
	}
}

// --major takes the redirect's release across majors, with no API request.
func TestSelfUpdateRedirectMajorNeedsNoAPI(t *testing.T) {
	ra := redirectAPIServer(t, "v5.0.0", http.StatusFound, http.StatusForbidden, "")
	exe := newExecutable(t)
	cfg := redirectConfig(exe, ra)
	cfg.currentVersion, cfg.major = "v4.1.0", true
	var out bytes.Buffer
	if err := selfUpdate(&out, cfg); err != nil || !strings.Contains(installed(t, exe), "v5.0.0") || ra.lists != 0 {
		t.Fatalf("%v %q lists %d\n%s", err, installed(t, exe), ra.lists, out.String())
	}
}

// A redirect that names no release (an error, or no tag in it) falls back
// to the API's list; when that fails as well, the error names both and the
// pinned form, and nothing is installed.
func TestSelfUpdateRedirectFailsFallsBackToTheList(t *testing.T) {
	for _, redirect := range []int{http.StatusNotFound, http.StatusFound} {
		ra := redirectAPIServer(t, "", redirect, http.StatusOK, majorList)
		exe := newExecutable(t)
		cfg := redirectConfig(exe, ra)
		cfg.currentVersion = "v4.1.0"
		var out bytes.Buffer
		if err := selfUpdate(&out, cfg); err != nil || !strings.Contains(installed(t, exe), "v4.3.1") || ra.lists != 1 {
			t.Fatalf("redirect %d: %v %q lists %d\n%s", redirect, err, installed(t, exe), ra.lists, out.String())
		}
	}
	ra := redirectAPIServer(t, "", http.StatusNotFound, http.StatusTooManyRequests, "")
	exe := newExecutable(t)
	cfg := redirectConfig(exe, ra)
	cfg.currentVersion = "v4.1.0"
	var out bytes.Buffer
	err := selfUpdate(&out, cfg)
	for _, want := range []string{"nothing is installed", "answered 404 Not Found without naming a release", "rate-limited the release list (HTTP 429", "CPB_UPDATE_VERSION=<tag> cpb self-update"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if installed(t, exe) != "OLD-BINARY" {
		t.Fatal("installed after both lookups failed")
	}
}

// CPB_UPDATE_VERSION names the release: nothing is looked up, and the usual
// rules hold (a release tag only, --major for a new major, never a
// downgrade).
func TestSelfUpdatePinnedAsksNothing(t *testing.T) {
	ra := redirectAPIServer(t, "v4.9.0", http.StatusFound, http.StatusOK, majorList)
	exe := newExecutable(t)
	cfg := redirectConfig(exe, ra)
	cfg.currentVersion, cfg.pinned = "v4.1.0", "v4.3.0"
	var out bytes.Buffer
	if err := selfUpdate(&out, cfg); err != nil || !strings.Contains(installed(t, exe), "v4.3.0") || ra.lists != 0 || ra.redirectHits != 0 || !strings.Contains(out.String(), "CPB_UPDATE_VERSION") {
		t.Fatalf("%v %q lists %d redirects %d\n%s", err, installed(t, exe), ra.lists, ra.redirectHits, out.String())
	}
	for pin, want := range map[string]string{"v4.4.0-rc1": "is not a release", "latest": "is not a release", "v5.0.0": "a new major version: add --major"} {
		exe := newExecutable(t)
		cfg := redirectConfig(exe, ra)
		cfg.currentVersion, cfg.pinned = "v4.1.0", pin
		err := selfUpdate(&out, cfg)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "nothing is installed") || installed(t, exe) != "OLD-BINARY" {
			t.Errorf("pinned %s: %v", pin, err)
		}
	}
	exe = newExecutable(t)
	cfg = redirectConfig(exe, ra)
	cfg.currentVersion, cfg.pinned = "v4.5.0", "v4.3.0"
	out.Reset()
	if err := selfUpdate(&out, cfg); err != nil || installed(t, exe) != "OLD-BINARY" || !strings.Contains(out.String(), "nothing to install") {
		t.Fatalf("a pinned downgrade: %v\n%s", err, out.String())
	}
	if ra.lists != 0 || ra.redirectHits != 0 {
		t.Fatalf("a pinned run looked something up: lists %d redirects %d", ra.lists, ra.redirectHits)
	}
}

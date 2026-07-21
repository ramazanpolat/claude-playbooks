package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// defaultUpdateRepo is the GitHub repo self-update pulls releases from. It
// mirrors install.sh's REPO default.
const defaultUpdateRepo = "ramazanpolat/claude-playbooks"

// selfUpdateConfig captures everything selfUpdate needs. Splitting it out from
// the runtime/env plumbing keeps the core logic hermetically testable against
// an httptest server -- no network, no touching the real executable.
type selfUpdateConfig struct {
	currentVersion string
	repo           string
	apiBase        string // GitHub API base (default https://api.github.com)
	downloadBase   string // release-asset base (default https://github.com/<repo>/releases/download)
	goos           string
	goarch         string
	execPath       string // the file to replace, already symlink-resolved
	httpClient     *http.Client
	token          string // optional GitHub token, for API rate limits
	force          bool
	checkOnly      bool
	verifyExec     bool // exec the downloaded binary with --version before swapping it in
}

// runSelfUpdate builds a selfUpdateConfig from the real runtime/env and runs it.
// This is the entry point wired into `claude-playbook update` (no name).
func runSelfUpdate(force, checkOnly bool) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot locate the running executable: %w", err)
	}
	// Resolve symlinks so we replace the real binary (e.g. the one behind the
	// `cpb` symlink), not the link itself.
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}

	cfg := selfUpdateConfig{
		currentVersion: Version,
		repo:           envOr("CLAUDE_PLAYBOOK_UPDATE_REPO", defaultUpdateRepo),
		apiBase:        envOr("CLAUDE_PLAYBOOK_UPDATE_API_BASE", "https://api.github.com"),
		downloadBase:   os.Getenv("CLAUDE_PLAYBOOK_UPDATE_DOWNLOAD_BASE"),
		goos:           runtime.GOOS,
		goarch:         runtime.GOARCH,
		execPath:       exe,
		httpClient:     &http.Client{Timeout: 60 * time.Second},
		token:          os.Getenv("GITHUB_TOKEN"),
		force:          force,
		checkOnly:      checkOnly,
		verifyExec:     true,
	}
	return selfUpdate(os.Stdout, cfg)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// selfUpdate resolves the latest release, and (unless up to date or check-only)
// downloads the matching asset, verifies it, and atomically replaces execPath.
func selfUpdate(w io.Writer, cfg selfUpdateConfig) error {
	fmt.Fprintf(w, "Current version: %s\n", cfg.currentVersion)

	latest, err := fetchLatestReleaseTag(cfg)
	if err != nil {
		return fmt.Errorf("could not determine the latest release: %w", err)
	}
	fmt.Fprintf(w, "Latest version:  %s\n", latest)

	upToDate := cfg.currentVersion == latest
	if cfg.checkOnly {
		if upToDate {
			fmt.Fprintln(w, "You are on the latest version.")
		} else {
			fmt.Fprintf(w, "An update is available: %s -> %s\n", cfg.currentVersion, latest)
			fmt.Fprintln(w, "Run 'claude-playbook update' to install it.")
		}
		return nil
	}
	if upToDate && !cfg.force {
		fmt.Fprintln(w, "Already up to date.")
		return nil
	}

	asset := fmt.Sprintf("claude-playbook-%s-%s", cfg.goos, cfg.goarch)
	downloadBase := cfg.downloadBase
	if downloadBase == "" {
		downloadBase = fmt.Sprintf("https://github.com/%s/releases/download", cfg.repo)
	}
	url := fmt.Sprintf("%s/%s/%s", strings.TrimRight(downloadBase, "/"), latest, asset)

	fmt.Fprintf(w, "Downloading %s %s (%s/%s)...\n", asset, latest, cfg.goos, cfg.goarch)

	// Stage the download in the target's own directory so the final rename is
	// an atomic same-filesystem swap (never a cross-device copy).
	dir := filepath.Dir(cfg.execPath)
	tmp, err := os.CreateTemp(dir, ".claude-playbook.update-*")
	if err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("cannot write to %s: permission denied. Re-run with elevated privileges (e.g. sudo) or reinstall via the installer", dir)
		}
		return fmt.Errorf("cannot create a temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // harmless no-op once the rename below succeeds

	if err := downloadTo(cfg, url, tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return err
	}

	if cfg.verifyExec {
		if err := verifyBinary(tmpPath, latest); err != nil {
			return fmt.Errorf("downloaded binary failed verification (aborting without replacing the current one): %w", err)
		}
	}

	if err := os.Rename(tmpPath, cfg.execPath); err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("cannot replace %s: permission denied. Re-run with elevated privileges (e.g. sudo) or reinstall via the installer", cfg.execPath)
		}
		return fmt.Errorf("failed to install the update: %w", err)
	}

	fmt.Fprintf(w, "Updated to %s at %s.\n", latest, cfg.execPath)
	return nil
}

// fetchLatestReleaseTag returns the tag_name of the repo's latest GitHub release.
func fetchLatestReleaseTag(cfg selfUpdateConfig) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(cfg.apiBase, "/"), cfg.repo)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "claude-playbook-selfupdate")
	req.Header.Set("Accept", "application/vnd.github+json")
	if cfg.token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.token)
	}
	resp, err := cfg.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("GitHub API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.TagName == "" {
		return "", fmt.Errorf("latest release has no tag_name")
	}
	return payload.TagName, nil
}

// downloadTo streams url into dst, following GitHub's redirect to asset storage.
func downloadTo(cfg selfUpdateConfig, url string, dst io.Writer) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "claude-playbook-selfupdate")
	resp, err := cfg.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: %s (%s)", resp.Status, url)
	}
	if _, err := io.Copy(dst, resp.Body); err != nil {
		return err
	}
	return nil
}

// verifyBinary runs `<path> --version` and confirms it executes and reports the
// expected version. This catches a corrupt/wrong/HTML download before it can
// clobber the working binary.
func verifyBinary(path, wantVersion string) error {
	out, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v (output: %s)", err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), wantVersion) {
		return fmt.Errorf("expected version %q in output %q", wantVersion, strings.TrimSpace(string(out)))
	}
	return nil
}

package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
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
	major          bool // --major: the newest release may be of a higher major version
	verifyExec     bool // exec the downloaded binary with --version before swapping it in
	nixManaged     bool // execPath lives in the Nix store (devbox, nix profile): never replace it
}

// isNixStorePath reports whether a (symlink-resolved) path is inside the Nix
// store. A binary there was installed by Nix -- devbox, `nix profile`, a
// flake -- and the store is read-only and content-addressed: replacing a file
// in it would corrupt that package, and the advice this command otherwise
// gives on a permission error ("re-run with sudo") would do exactly that.
func isNixStorePath(p string) bool {
	return strings.HasPrefix(p, "/nix/store/")
}

// runSelfUpdate builds a selfUpdateConfig from the real runtime/env and runs it.
var (
	selfUpdateCheck bool
	selfUpdateForce bool
	selfUpdateMajor bool
)

var selfUpdateCmd = &cobra.Command{
	Use:   "self-update",
	Short: "Update cpb itself to the newest release of its major version",
	Long: `Update cpb itself to the newest release of its major version.

A new major version is never installed on its own: self-update says it is
available and stays put. --major installs it; read its release notes first.
Pre-releases are never installed.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSelfUpdate(selfUpdateForce, selfUpdateCheck, selfUpdateMajor)
	},
}

func init() {
	selfUpdateCmd.Flags().BoolVar(&selfUpdateCheck, "check", false, "report the newest release of this major version and the newest overall, without installing")
	selfUpdateCmd.Flags().BoolVarP(&selfUpdateForce, "force", "f", false, "reinstall even if already on the newest release")
	selfUpdateCmd.Flags().BoolVar(&selfUpdateMajor, "major", false, "allow an update to a new major version (read its release notes first)")
}

// runSelfUpdate is `cpb self-update`.
func runSelfUpdate(force, checkOnly, major bool) error {
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
		repo:           envOr("CPB_UPDATE_REPO", defaultUpdateRepo),
		apiBase:        envOr("CPB_UPDATE_API_BASE", "https://api.github.com"),
		downloadBase:   os.Getenv("CPB_UPDATE_DOWNLOAD_BASE"),
		goos:           runtime.GOOS,
		goarch:         runtime.GOARCH,
		execPath:       exe,
		httpClient:     &http.Client{Timeout: 60 * time.Second},
		token:          os.Getenv("GITHUB_TOKEN"),
		force:          force,
		checkOnly:      checkOnly,
		major:          major,
		verifyExec:     true,
		// Judged on the RESOLVED path: under devbox, argv[0] is the profile's
		// stable symlink (.devbox/nix/profile/default/bin/...), not the store.
		nixManaged: isNixStorePath(exe),
	}
	return selfUpdate(os.Stdout, cfg)
}

// A devbox `add` with a different ref APPENDS a second package rather than
// replacing the first, so the hint says to change the ref, not to re-add.
const nixUpdateHint = "Installed through Nix (devbox or a flake): update there instead. In a devbox project, change\n" +
	"the tag in devbox.json and run `devbox install`; the package is\n" +
	"  git+https://github.com/ramazanpolat/claude-playbooks?ref=refs/tags/<tag>#cpb"

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

	// A Nix-managed binary is refused before anything else -- no release
	// lookup, no network, whatever the latest version is: it is never this
	// command's to replace. --check below still reports, as information.
	if cfg.nixManaged && !cfg.checkOnly {
		return fmt.Errorf("%s is managed by Nix and cannot be replaced in place.\n%s", cfg.execPath, nixUpdateHint)
	}

	tags, err := fetchReleaseTags(cfg)
	if err != nil {
		return fmt.Errorf("could not read the release list, so nothing is installed: %w", err)
	}
	cur, curOK := parseVersion(runningVersion, cfg.currentVersion)
	newest, newestOK := pickRelease(tags, -1)
	if !newestOK {
		return fmt.Errorf("the release list holds no release (vMAJOR.MINOR.PATCH, not a pre-release), so nothing is installed")
	}
	var latest string
	var target semver
	switch {
	case !curOK:
		// A dev build has no major version to stay within.
		fmt.Fprintf(w, "Newest release:  %s\n", newest)
		if !cfg.major {
			fmt.Fprintf(w, "%s is not a release version, so cpb cannot tell its major version: `cpb self-update --major` installs %s.\n", cfg.currentVersion, newest)
			if cfg.checkOnly {
				return nil
			}
			return fmt.Errorf("not a release version: run `cpb self-update --major` to install %s", newest)
		}
		target, latest = newest, newest.String()
	default:
		same, sameOK := pickRelease(tags, cur.major)
		if sameOK {
			fmt.Fprintf(w, "Newest v%d release: %s\n", cur.major, same)
		} else {
			fmt.Fprintf(w, "Newest v%d release: none\n", cur.major)
		}
		fmt.Fprintf(w, "Newest release:  %s\n", newest)
		target, latest = same, same.String()
		if cfg.major {
			target, latest, sameOK = newest, newest.String(), true
		} else if newest.major > cur.major {
			fmt.Fprintf(w, "%s is available, a new major version: run `cpb self-update --major` (read its release notes first)\n", newest)
		}
		if !sameOK {
			fmt.Fprintf(w, "No v%d release is published; nothing to install.\n", cur.major)
			return nil
		}
		if cur.after(target) {
			// Never a downgrade, --force included.
			fmt.Fprintf(w, "%s is newer than the newest release to install (%s); nothing to install.\n", cfg.currentVersion, latest)
			return nil
		}
	}

	upToDate := curOK && !target.after(cur)
	if cfg.checkOnly {
		if upToDate {
			fmt.Fprintln(w, "You are on the newest release.")
		} else {
			fmt.Fprintf(w, "An update is available: %s -> %s\n", cfg.currentVersion, latest)
			if cfg.nixManaged {
				fmt.Fprintln(w, nixUpdateHint)
			} else if target.major != cur.major || !curOK {
				fmt.Fprintln(w, "Run 'cpb self-update --major' to install it.")
			} else {
				fmt.Fprintln(w, "Run 'cpb self-update' to install it.")
			}
		}
		return nil
	}
	if upToDate && !cfg.force {
		fmt.Fprintln(w, "Already up to date.")
		return nil
	}

	asset := fmt.Sprintf("cpb-%s-%s", cfg.goos, cfg.goarch)
	downloadBase := cfg.downloadBase
	if downloadBase == "" {
		downloadBase = fmt.Sprintf("https://github.com/%s/releases/download", cfg.repo)
	}
	url := fmt.Sprintf("%s/%s/%s", strings.TrimRight(downloadBase, "/"), latest, asset)

	fmt.Fprintf(w, "Downloading %s %s (%s/%s)...\n", asset, latest, cfg.goos, cfg.goarch)

	// Stage the download in the target's own directory so the final rename is
	// an atomic same-filesystem swap (never a cross-device copy).
	dir := filepath.Dir(cfg.execPath)
	tmp, err := os.CreateTemp(dir, ".cpb.update-*")
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

	if err := verifyChecksum(w, cfg, downloadBase, latest, asset, tmpPath); err != nil {
		return fmt.Errorf("downloaded binary failed checksum verification (aborting without replacing the current one): %w", err)
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

// semver is a release version, vMAJOR.MINOR.PATCH; pre is the running
// version's pre-release suffix (-rc1), which a release tag never has.
type semver struct {
	major, minor, patch int
	pre                 string
}

var (
	// releaseTag is a release self-update may install: no suffix, so a
	// v4.0.0-rc1 is never picked, flagged as a pre-release or not.
	releaseTag = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)
	// runningVersion is a version this binary may report.
	runningVersion = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.-]+)?$`)
)

func parseVersion(re *regexp.Regexp, s string) (semver, bool) {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return semver{}, false
	}
	var v semver
	for i, p := range []*int{&v.major, &v.minor, &v.patch} {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return semver{}, false
		}
		*p = n
	}
	if len(m) > 4 {
		v.pre = strings.TrimPrefix(m[4], "-")
	}
	return v, true
}

func (v semver) String() string { return fmt.Sprintf("v%d.%d.%d", v.major, v.minor, v.patch) }

// after reports whether v is a later version than o: by number, and a
// pre-release before the release of the same number.
func (v semver) after(o semver) bool {
	if v.major != o.major {
		return v.major > o.major
	}
	if v.minor != o.minor {
		return v.minor > o.minor
	}
	if v.patch != o.patch {
		return v.patch > o.patch
	}
	return v.pre == "" && o.pre != ""
}

// pickRelease is the highest release among tags, by number, within major
// (any major when major is -1). The release list is ordered by date, so a
// hotfix to an older major published after a newer one is still found.
func pickRelease(tags []string, major int) (semver, bool) {
	var best semver
	found := false
	for _, t := range tags {
		v, ok := parseVersion(releaseTag, t)
		if !ok || (major >= 0 && v.major != major) {
			continue
		}
		if !found || v.after(best) {
			best, found = v, true
		}
	}
	return best, found
}

// releasePages is how many pages of the release list self-update reads, at
// 100 a page; a longer list fails closed rather than missing a release.
const releasePages = 10

var nextLink = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// fetchReleaseTags returns the tags of the repo's published releases that
// are not pre-releases, from GitHub's release list, page by page. Any
// failure is an error: self-update installs nothing it could not choose
// from the whole list. It never falls back to /releases/latest, which can
// name a higher major version.
func fetchReleaseTags(cfg selfUpdateConfig) ([]string, error) {
	base := strings.TrimRight(cfg.apiBase, "/")
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=100", base, cfg.repo)
	var tags []string
	for page := 1; url != ""; page++ {
		if page > releasePages {
			return nil, fmt.Errorf("the release list is longer than %d pages", releasePages)
		}
		// Only the API base is ever asked, and given the token.
		if !strings.HasPrefix(url, base+"/") {
			return nil, fmt.Errorf("the release list points outside %s", base)
		}
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "cpb-selfupdate")
		req.Header.Set("Accept", "application/vnd.github+json")
		if cfg.token != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.token)
		}
		resp, err := cfg.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, fmt.Errorf("GitHub API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
		}
		var releases []struct {
			TagName    string `json:"tag_name"`
			Draft      bool   `json:"draft"`
			Prerelease bool   `json:"prerelease"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&releases)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("the release list is not valid JSON: %w", err)
		}
		for _, r := range releases {
			if !r.Draft && !r.Prerelease && r.TagName != "" {
				tags = append(tags, r.TagName)
			}
		}
		url = ""
		if m := nextLink.FindStringSubmatch(resp.Header.Get("Link")); m != nil {
			url = m[1]
		}
	}
	return tags, nil
}

// downloadTo streams url into dst, following GitHub's redirect to asset storage.
func downloadTo(cfg selfUpdateConfig, url string, dst io.Writer) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "cpb-selfupdate")
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

var hexDigest = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// verifyChecksum fetches the release's SHA256SUMS and compares the staged
// download's digest. Policy mirrors install.sh: a genuine mismatch aborts;
// every unverifiable case (no sums published, entry missing, malformed or
// duplicated) warns and continues — the sums travel over the same channel
// as the binary, so they guard against corruption and truncation, not a
// compromised host.
func verifyChecksum(w io.Writer, cfg selfUpdateConfig, downloadBase, latest, asset, path string) error {
	url := fmt.Sprintf("%s/%s/SHA256SUMS", strings.TrimRight(downloadBase, "/"), latest)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintf(w, "Warning: no SHA256SUMS for %s; skipping checksum verification\n", latest)
		return nil
	}
	req.Header.Set("User-Agent", "cpb-selfupdate")
	resp, err := cfg.httpClient.Do(req)
	if err != nil {
		fmt.Fprintf(w, "Warning: no SHA256SUMS for %s; skipping checksum verification\n", latest)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(w, "Warning: no SHA256SUMS published for %s; skipping checksum verification\n", latest)
		return nil
	}
	const sumsLimit = 64 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, sumsLimit+1))
	if err != nil {
		fmt.Fprintf(w, "Warning: could not read SHA256SUMS for %s; skipping checksum verification\n", latest)
		return nil
	}
	if len(body) > sumsLimit {
		// A truncated parse could miss the real entry and either skip
		// verification or, worse, match a wrong prefix line — treat an
		// oversized manifest as unverifiable, never as partially true.
		fmt.Fprintf(w, "Warning: SHA256SUMS for %s exceeds %d bytes; skipping checksum verification\n", latest, sumsLimit)
		return nil
	}

	want := ""
	matches := 0
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		// sha256sum text mode writes "hash  name"; --binary mode writes
		// "hash *name" — both are valid manifests.
		if len(fields) == 2 && (fields[1] == asset || fields[1] == "*"+asset) {
			matches++
			want = fields[0]
		}
	}
	if matches == 0 {
		fmt.Fprintf(w, "Warning: %s not listed in SHA256SUMS; skipping checksum verification\n", asset)
		return nil
	}
	if matches != 1 || !hexDigest.MatchString(want) {
		// A truncated or duplicated entry must not fail a legitimate binary
		// as "mismatch" — it is unverifiable, not wrong.
		fmt.Fprintf(w, "Warning: malformed SHA256SUMS entry for %s; skipping checksum verification\n", asset)
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(want, got) {
		return fmt.Errorf("checksum mismatch for %s %s: expected %s, got %s", asset, latest, strings.ToLower(want), got)
	}
	fmt.Fprintln(w, "Checksum verified (sha256).")
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

package cmd

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// A playbook whose CLAUDE.md imports ~/.pilot-profile/ sends the profile
// with every request. When a statement points such a playbook at a model
// route that is not Anthropic's, it gets a warning: never a refusal, since
// the pilot decides where their profile may go. The warning is given by the
// statement that makes it so, not again by every later one.

const pilotProfileDir = ".pilot-profile/"

// exposureCandidates names the playbooks a statement can expose: the one it
// creates or alters, or, for an env set or DEFAULTS, every playbook. nil:
// the statement cannot (a rename, a plain config directory, a read).
func (r *stmtRun) exposureCandidates(st *grammar.Stmt) []string {
	switch {
	case st.Dir != "":
		return nil
	case st.Object == grammar.Playbook && st.Verb == grammar.Create:
		return []string{st.Name}
	case st.Object == grammar.Playbook && st.Verb == grammar.Alter:
		if st.Name == "" || lifecycle(st) {
			return nil
		}
		return []string{st.Name}
	case st.Object == grammar.Env && st.Write(),
		st.Object == grammar.Defaults && st.Verb == grammar.Alter:
		pbs, err := playbook.Discover(config.ResolvePlaybooksDir())
		if err != nil {
			return nil
		}
		var names []string
		for _, pb := range pbs {
			names = append(names, pb.Name)
		}
		if r.dry != nil {
			for name, alive := range r.dry.playbooks {
				if alive && !slices.Contains(names, name) {
					names = append(names, name)
				}
			}
		}
		return names
	}
	return nil
}

// profileExposure maps each of names that imports the pilot profile and
// whose launch would set a literal non-Anthropic ANTHROPIC_BASE_URL to that
// URL's host, as the run sees them. A playbook it cannot resolve (a missing
// env set, a base URL given by reference) is left out: the warning is
// advice, never a guess, and a reference is never resolved for it.
func (r *stmtRun) profileExposure(names []string) map[string]string {
	out := map[string]string{}
	if len(names) == 0 {
		return out
	}
	playbooksDir := config.ResolvePlaybooksDir()
	profDir := envprofile.Dir(playbooksDir)
	onDisk := map[string]*playbook.Playbook{}
	if pbs, err := playbook.Discover(playbooksDir); err == nil {
		for _, pb := range pbs {
			onDisk[pb.Name] = pb
		}
	}
	for _, name := range names {
		known, alive := r.playbookState(name)
		if known && !alive {
			continue
		}
		pb := onDisk[name]
		if pb == nil && !known {
			continue
		}
		if !r.importsPilotProfile(name, pb) {
			continue
		}
		var disk *manifest.Env
		if pb != nil && pb.Manifest != nil {
			disk = pb.Manifest.Env
		}
		if host, ok := r.thirdPartyHost(profDir, r.playbookEnv(name, disk)); ok {
			out[name] = host
		}
	}
	return out
}

// importsPilotProfile reports whether a playbook's CLAUDE.md imports
// ~/.pilot-profile/, as the run sees it: in a dry run, a playbook created
// earlier has the CLAUDE.md its CREATE would write.
func (r *stmtRun) importsPilotProfile(name string, pb *playbook.Playbook) bool {
	if r.dry != nil {
		if v, ok := r.dry.pilotProfile[name]; ok {
			return v
		}
	}
	dir := r.configDir(name, pb)
	if dir == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		return false
	}
	return importsProfile(data)
}

// importsProfile reports whether a CLAUDE.md has an @import line under
// ~/.pilot-profile/, written with ~ or with the home directory.
func importsProfile(data []byte) bool {
	home, _ := os.UserHomeDir()
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "@~/"+pilotProfileDir) ||
			(home != "" && strings.HasPrefix(line, "@"+filepath.Join(home, pilotProfileDir)+"/")) {
			return true
		}
	}
	return false
}

// thirdPartyHost resolves the ANTHROPIC_BASE_URL a launch would get from
// DEFAULTS, the playbook's env sets and its own block, and reports its host
// when that is not Anthropic's.
func (r *stmtRun) thirdPartyHost(profDir string, own *manifest.Env) (string, bool) {
	names, err := r.defaultsList(profDir)
	if err != nil {
		return "", false
	}
	if own != nil {
		names = append(names, own.Profiles...)
	}
	layers := make([]*manifest.Env, 0, len(names)+1)
	for _, n := range names {
		p, err := r.profile(profDir, n)
		if err != nil || p == nil {
			return "", false
		}
		layers = append(layers, p.Env())
	}
	if own != nil {
		layers = append(layers, &manifest.Env{Set: own.Set, Refs: own.Refs, Unset: own.Unset})
	}
	v, ok := manifest.MergeEnv(layers...).Set["ANTHROPIC_BASE_URL"]
	if !ok {
		return "", false
	}
	u, err := url.Parse(strings.TrimSpace(v))
	if err != nil {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || host == "anthropic.com" || strings.HasSuffix(host, ".anthropic.com") {
		return "", false
	}
	return host, true
}

// warnExposure adds the warning for the playbooks a statement exposed: in
// after, and not already in before.
func (r *stmtRun) warnExposure(before, after map[string]string) {
	var names []string
	for name := range after {
		if _, had := before[name]; !had {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = fmt.Sprintf("%s (%s)", n, after[n])
	}
	subject, verb := "PLAYBOOK "+parts[0]+" imports", "its"
	if len(parts) > 1 {
		subject, verb = "PLAYBOOKS "+strings.Join(parts, ", ")+" import", "their"
	}
	msg := subject + " ~/.pilot-profile/ and now sends requests to a non-Anthropic ANTHROPIC_BASE_URL, so the profile goes there with every request: remove the imports from " + verb + " CLAUDE.md (a new playbook: CREATE PLAYBOOK <name> NO PILOT PROFILE)"
	if r.warning != "" {
		r.warning += "; " + msg
	} else {
		r.warning, r.warningCode = msg, warnPilotProfileThirdParty
	}
}

// SHOW PLAYBOOK's pilot_profile values (v3.26.0).
const (
	pilotImported    = "imported"
	pilotNotImported = "not_imported"
	pilotUnknown     = "unknown"
)

// pilotProfileState is whether the CLAUDE.md in dir imports the pilot
// profile. No CLAUDE.md imports nothing; one that cannot be read is
// unknown, never a guess.
func pilotProfileState(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return pilotNotImported
	case err != nil:
		return pilotUnknown
	case importsProfile(data):
		return pilotImported
	}
	return pilotNotImported
}

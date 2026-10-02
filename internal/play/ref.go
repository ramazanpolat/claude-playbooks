// Package play fetches and checks a recipe someone else wrote, for
// `cpb play <ref>` (v4.0.0): where the ref points, a bounded fetch, the
// recipe's header, and which statements a played recipe may hold and which
// of them are risks to show before anything runs. It plans and writes
// nothing: the cmd package does that, with these results.
package play

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Ref kinds.
const (
	KindTemplate = "template" // a curated name: site/p/<name>.cpb at this cpb's tag
	KindURL      = "url"      // https://…
	KindGitHub   = "github"   // github:<owner>/<repo>/<path>.cpb@<ref>
	KindLocal    = "local"    // ./x.cpb, /abs/x.cpb, ~/x.cpb
)

// TemplateRepo holds the curated templates, under site/p/.
const TemplateRepo = "ramazanpolat/claude-playbooks"

// rawBase serves a repository's files over https.
const rawBase = "https://raw.githubusercontent.com"

var (
	templateName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	githubPart   = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	gitRefName   = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_./+-]*$`)
	releaseTag   = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-rc[0-9]+)?$`)
	commitish    = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)
	nameChars    = regexp.MustCompile(`[^a-z0-9-]+`)
)

// Source is where a ref points.
type Source struct {
	Ref  string // as typed
	Kind string
	URL  string // the https URL to fetch (every kind but local)
	Path string // a local file
	// Name is the base of the temporary playbook's name: the template's
	// name, or the file's base name without .cpb.
	Name string
	// Pinned: a release tag or a commit (template, github), or a local file.
	// A URL is pinned only by a checksum.
	Pinned bool
	// Note is shown in the preview when the ref is not pinned.
	Note string
}

// Resolve says where ref points. version is this cpb's version: a curated
// name is read at that release's tag, so a template is the one this cpb was
// tested against; a dev build reads main, and says so.
func Resolve(ref, version string) (*Source, error) {
	switch {
	case ref == "":
		return nil, errors.New("play what? a template name, an https URL, github:<owner>/<repo>/<path>.cpb@<ref>, or a local file")
	case strings.HasPrefix(ref, "./"), strings.HasPrefix(ref, "../"), strings.HasPrefix(ref, "/"), strings.HasPrefix(ref, "~/"),
		// x.cpb or dir/x.cpb: a template name has no dot or slash, and
		// every other form a scheme, so this can only be a path.
		!strings.Contains(ref, ":") && (strings.HasSuffix(ref, ".cpb") || strings.Contains(ref, "/")):
		path := ref
		if strings.HasPrefix(path, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			path = filepath.Join(home, path[2:])
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		return &Source{Ref: ref, Kind: KindLocal, Path: abs, Name: baseName(abs), Pinned: true}, nil
	case strings.HasPrefix(ref, "https://"):
		u, err := url.Parse(ref)
		if err != nil || u.Host == "" {
			return nil, errors.New("not a valid https URL")
		}
		if u.User != nil {
			return nil, errors.New("a URL carrying credentials is refused")
		}
		return &Source{Ref: ref, Kind: KindURL, URL: u.String(), Name: baseName(u.Path),
			Note: "a URL: pinned only by its checksum (--sha256)"}, nil
	case strings.HasPrefix(ref, "github:"):
		return resolveGitHub(ref)
	case strings.Contains(ref, "://"):
		return nil, errors.New("https only: a recipe is fetched over https, and nothing else")
	case templateName.MatchString(ref):
		tag, note := version, ""
		if !releaseTag.MatchString(version) {
			tag, note = "main", "a dev build: the template is read from main, not from a release"
		}
		return &Source{Ref: ref, Kind: KindTemplate, Name: ref, Pinned: note == "", Note: note,
			URL: fmt.Sprintf("%s/%s/%s/site/p/%s.cpb", rawBase, TemplateRepo, tag, ref)}, nil
	}
	return nil, fmt.Errorf("%q is not a template name, an https URL, a github: ref or a local file (./x.cpb)", ref)
}

// resolveGitHub reads github:<owner>/<repo>/<path>.cpb@<ref>. The ref is
// required: following the default branch would make the preview and the
// next run differ.
func resolveGitHub(ref string) (*Source, error) {
	const form = "github:<owner>/<repo>/<path>.cpb@<tag, commit or branch>"
	rest := strings.TrimPrefix(ref, "github:")
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return nil, fmt.Errorf("pin it: %s@<tag> (a tag or a commit; a branch can change)", ref)
	}
	path, gitRef := rest[:at], rest[at+1:]
	parts := strings.SplitN(path, "/", 3)
	if len(parts) != 3 || !githubPart.MatchString(parts[0]) || !githubPart.MatchString(parts[1]) {
		return nil, fmt.Errorf("a github ref is %s", form)
	}
	file := parts[2]
	if !strings.HasSuffix(file, ".cpb") || strings.HasPrefix(file, "/") || strings.Contains("/"+file+"/", "/../") || strings.Contains(file, "//") {
		return nil, fmt.Errorf("a github ref is %s: a .cpb file in the repository", form)
	}
	if gitRef == "" || !gitRefName.MatchString(gitRef) || strings.Contains(gitRef, "..") || strings.HasSuffix(gitRef, "/") {
		return nil, fmt.Errorf("a github ref is %s", form)
	}
	s := &Source{Ref: ref, Kind: KindGitHub, Name: baseName(file),
		URL: fmt.Sprintf("%s/%s/%s/%s/%s", rawBase, parts[0], parts[1], gitRef, file)}
	if releaseTag.MatchString(gitRef) || commitish.MatchString(gitRef) {
		s.Pinned = true
	} else {
		s.Note = "not a release tag: this may change"
	}
	return s, nil
}

// baseName is a file's name without .cpb, reduced to a playbook-name
// fragment: lowercase letters, digits and dashes, at most 24 characters.
func baseName(p string) string {
	b := strings.TrimSuffix(filepath.Base(p), ".cpb")
	b = strings.Trim(nameChars.ReplaceAllString(strings.ToLower(b), "-"), "-")
	if len(b) > 24 {
		b = strings.Trim(b[:24], "-")
	}
	if b == "" {
		b = "recipe"
	}
	return b
}

package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// cpb ask <playbook> "<request>": the question is addressed to a playbook.
// It signs in the way a launch of that playbook would, decided from its
// manifest and stores without preparing a launch, and "you" in the request
// means that playbook. Only its login is used: its CLAUDE.md, settings,
// hooks and MCP servers stay out as for every question.

// askTarget resolves a playbook by directory name first and then by
// launcher, as a launcher command does; nil when name is neither.
func askTarget(root, name string) (*playbook.Playbook, error) {
	pbs, _, err := playbook.Scan(root)
	if err != nil {
		return nil, err
	}
	for _, pb := range pbs {
		if pb.Name == name {
			return pb, nil
		}
	}
	for _, pb := range pbs {
		if pb.Manifest != nil && pb.Manifest.Launcher == name {
			return pb, nil
		}
	}
	return nil, nil
}

// askSplitArgs reads cpb ask's arguments: with more than one, a first one
// that names a playbook or a launcher addresses it, and the rest is the
// request; otherwise every argument is the request.
func askSplitArgs(root string, args []string) (*playbook.Playbook, string, error) {
	if len(args) > 1 {
		pb, err := askTarget(root, args[0])
		if err != nil {
			return nil, "", err
		}
		if pb != nil {
			return pb, strings.TrimSpace(strings.Join(args[1:], " ")), nil
		}
	}
	return nil, strings.TrimSpace(strings.Join(args, " ")), nil
}

// askTargetSignIn decides how a question addressed to pb signs in: as a
// launch of pb would, on Anthropic only, with no fallback to another login.
func askTargetSignIn(root string, pb *playbook.Playbook) (askSignIn, error) {
	var env *manifest.Env
	if pb.Manifest != nil {
		env = pb.Manifest.Env
	}
	if route := askRoute(envset.Dir(root), env); route != "anthropic" {
		return askSignIn{}, fmt.Errorf("%s sends its requests away from Anthropic (route %s), and cpb ask asks Anthropic only: nothing was sent. Ask without naming it: cpb ask \"<request>\"", pb.Name, route)
	}
	rep := auth.Inspect(pb.Name, pb.Path, time.Now())
	switch rep.Mode {
	case auth.ModeToken:
		// The machine's token, from the file every launch reads; an
		// exported one passes through the environment as it is.
		if data, err := os.ReadFile(auth.OAuthTokenFile()); err == nil && strings.TrimSpace(string(data)) != "" {
			return askSignIn{mode: askLoginToken}, nil
		}
		return askSignIn{mode: askLoginClaude}, nil
	case auth.ModeSharedLogin:
		return askSignIn{mode: askLoginClaude}, nil
	case auth.ModeIsolatedLogin:
		switch rep.Login() {
		case "store", "keychain":
			return askSignIn{mode: askLoginPlaybook, configDir: pb.Path}, nil
		case "unknown":
			return askSignIn{}, fmt.Errorf("%s's own login cannot be read (cpb auth status %s says more): nothing was sent", pb.Name, pb.Name)
		}
		return askSignIn{}, fmt.Errorf("%s has no login of its own yet: launch it once and run /login, then ask again. Nothing was sent", pb.Name)
	case auth.ModePlaybookToken:
		return askSignIn{}, fmt.Errorf("%s signs in with a token its own layers set, which cpb ask does not use yet: nothing was sent. Ask without naming it: cpb ask \"<request>\"", pb.Name)
	case auth.ModeError:
		return askSignIn{}, fmt.Errorf("%s cannot be launched (%s): nothing was sent", pb.Name, askToolScrub(rep.ModeError))
	}
	return askSignIn{}, fmt.Errorf("cpb ask cannot tell how %s signs in (%s): nothing was sent", pb.Name, rep.Mode)
}

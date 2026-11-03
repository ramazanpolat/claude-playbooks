package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/auth"
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
// flag is the --account the question names ("" for none): the sign-in is
// then decided on that account, as `cpb run --account <flag> <pb>` decides
// it.
func askTargetSignIn(root string, pb *playbook.Playbook, flag string) (askSignIn, error) {
	var a *account.Account
	if flag == "" {
		if route := askPlaybookRoute(pb.Path); route != "anthropic" {
			return askSignIn{}, fmt.Errorf("%s sends its requests away from Anthropic (route %s), and cpb ask asks Anthropic only: nothing was sent. Ask without naming it: cpb ask \"<request>\"", pb.Name, route)
		}
		a = askPlaybookAccount(pb.Path, pb.Name)
	} else {
		// The account a launch with --account <flag> runs on, or its
		// refusal (a playbook that lists accounts runs only on them).
		var list []string
		if m, _ := manifest.Nearest(pb.Path); m != nil && m.Env != nil {
			list = m.Env.Accounts
		}
		sel, err := account.Select(account.Dir(root), pb.Name, list, flag)
		if err != nil {
			return askSignIn{}, fmt.Errorf("%w: nothing was sent", err)
		}
		if a = sel.Account; a != nil {
			if err := askAccountUsable(a); err != nil {
				return askSignIn{}, err
			}
		}
	}
	// An Anthropic account that signs in with a Claude token: the question
	// signs in with it, as a launch would. One that signs in with a key or
	// a token of another kind is refused: cpb ask signs in with a Claude
	// login or a Claude token only.
	if a != nil && a.Auth.Kind != account.ClaudeLogin {
		if a.Auth.Kind != account.ClaudeToken {
			return askSignIn{}, fmt.Errorf("%s runs on account %s, which signs in with its %s, and cpb ask signs in with a Claude login or a Claude token only: nothing was sent. Ask without naming it: cpb ask \"<request>\"", pb.Name, a.Name, a.Auth.Kind)
		}
		return askTokenAccountSignIn(a)
	}
	return askLoginSignIn(pb, flag, a != nil)
}

// askLoginSignIn is how a question addressed to pb signs in on a Claude
// login, or the machine's token, as a launch of pb on flag (its own choice
// of account when "") would. onAccount: the launch runs on a claude_login
// account, which blocks every other credential, so the question keeps none
// beside the login either.
func askLoginSignIn(pb *playbook.Playbook, flag string, onAccount bool) (askSignIn, error) {
	rep := auth.InspectOn(pb.Name, pb.Path, flag, time.Now())
	switch rep.Mode {
	case auth.ModeToken:
		// The machine's token, from the file every launch reads; an
		// exported one passes through the environment as it is.
		if data, err := os.ReadFile(auth.OAuthTokenFile()); err == nil && strings.TrimSpace(string(data)) != "" {
			return askSignIn{mode: askLoginToken}, nil
		}
		return askSignIn{mode: askLoginClaude}, nil
	case auth.ModeSharedLogin:
		return askSignIn{mode: askLoginClaude, loginOnly: onAccount || rep.TokenBlocked}, nil
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

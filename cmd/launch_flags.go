package cmd

import (
	"fmt"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// Launch flags: one-off environment layers for a single launch, applied on
// top of the playbook's own [env] block in command-line order and never
// written anywhere.
//
//	--env-profile NAME   layer an existing env profile
//	--env KEY=VALUE      set one variable
//	--unset KEY          remove one variable
//	--env-file PATH      layer a dotenv-style file (KEY=VALUE lines)
//
// They are recognised only as a LEADING run of arguments: for `run`, before
// the playbook name and again immediately after it; for `start`, before the
// path; for a launcher, at the very start. The first argument that is not
// one of these ends the scan and everything from there on belongs to claude,
// so a claude flag can never be mistaken for ours and vice versa. Each flag
// takes its value as the next argument or after "=".
const launchFlagsUsage = "[--env-profile NAME] [--env KEY=VALUE] [--unset KEY] [--env-file PATH]"

var launchFlagNames = map[string]bool{"--env-profile": true, "--env": true, "--unset": true, "--env-file": true}

// takeLaunchFlags consumes leading launch flags from args and returns the
// remaining arguments and the layers they describe, in order. Validation is
// complete before anything else happens: a bad value must fail before the
// registry is consulted or credentials are touched.
func takeLaunchFlags(args []string) (rest []string, layers []*manifest.Env, err error) {
	return takeLaunchFlagsWith(args, nil)
}

// takeLaunchFlagsWith is takeLaunchFlags that also recognises the boolean
// wrapper flags in bools (name -> destination) within the same leading run.
// `start --delete` is the one user: recognised only here, it can no longer be
// mistaken for a claude argument, a claude flag's value, or anything after
// "--". The scan stops at "--" without consuming it, so what follows is
// claude's verbatim.
func takeLaunchFlagsWith(args []string, bools map[string]*bool) (rest []string, layers []*manifest.Env, err error) {
	i := 0
	for i < len(args) {
		if args[i] == "--" {
			break
		}
		if dst, ok := bools[args[i]]; ok {
			*dst = true
			i++
			continue
		}
		flag, value, inline := strings.Cut(args[i], "=")
		if !launchFlagNames[flag] {
			break
		}
		if !inline {
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("flag needs an argument: %s", flag)
			}
			value = args[i+1]
			i++
		}
		i++
		layer, err := launchLayer(flag, value)
		if err != nil {
			return nil, nil, err
		}
		layers = append(layers, layer)
	}
	return args[i:], layers, nil
}

func launchLayer(flag, value string) (*manifest.Env, error) {
	switch flag {
	case "--env-profile":
		if err := manifest.ValidateProfileName(value); err != nil {
			return nil, err
		}
		return &manifest.Env{Profiles: []string{value}}, nil
	case "--env":
		key, val, ok := strings.Cut(value, "=")
		if !ok {
			return nil, fmt.Errorf("--env expects KEY=VALUE, got %q", value)
		}
		if err := manifest.ValidateEnvKey(key); err != nil {
			return nil, err
		}
		if err := manifest.ValidateEnvValue(key, val); err != nil {
			return nil, err
		}
		return &manifest.Env{Set: map[string]string{key: val}}, nil
	case "--unset":
		if strings.Contains(value, "=") {
			return nil, fmt.Errorf("--unset expects a variable name, got %q", value)
		}
		if err := manifest.ValidateEnvKey(value); err != nil {
			return nil, err
		}
		return &manifest.Env{Unset: []string{value}}, nil
	case "--env-file":
		layer, err := manifest.ParseEnvFile(value)
		if err != nil {
			return nil, fmt.Errorf("--env-file: %w", err)
		}
		return layer, nil
	}
	return nil, fmt.Errorf("unknown launch flag %s", flag)
}

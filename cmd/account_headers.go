package cmd

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"syscall"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
)

// An account's request headers reach claude in one variable,
// ANTHROPIC_CUSTOM_HEADERS ("Name: value" lines), while each value is a
// secret reference that cpb never resolves. So a launch with headers runs
//
//	<helper> CPB_ACCOUNT_HEADER_0=<ref> … -- <cpb> __account-headers <Name> … -- claude <args>
//
// The helper sets each value in a variable of its own and execs cpb's
// header step, which joins them, removes those variables, and execs claude.
// No value is ever on an argument list or printed.

// headerStepArg is the header step's hidden first argument.
const headerStepArg = "__account-headers"

// headerStepArgv is what the helper execs after its "--": cpb's header step,
// then claude.
func headerStepArgv(self string, names []string, claudePath string, claudeArgs []string) []string {
	argv := append([]string{self, headerStepArg}, names...)
	argv = append(append(argv, "--", claudePath), claudeArgs...)
	return argv
}

// runHeaderStep joins the header values the helper set and execs claude.
// args are what follows headerStepArg: <Name> … -- <claude> <args>.
func runHeaderStep(args []string) error {
	sep := slices.Index(args, "--")
	if sep < 1 || sep == len(args)-1 {
		return errors.New("the account header step takes <header> … -- <command> [args]; a launch runs it, never a person")
	}
	names, command := args[:sep], args[sep+1:]
	headers, err := joinHeaders(names, os.Getenv)
	if err != nil {
		return err
	}
	env := withoutPrefix(os.Environ(), account.HeaderVarPrefix)
	env = append(withoutKeys(env, []string{"ANTHROPIC_CUSTOM_HEADERS"}), "ANTHROPIC_CUSTOM_HEADERS="+headers)
	return syscall.Exec(command[0], command, env)
}

// joinHeaders builds ANTHROPIC_CUSTOM_HEADERS from the values the helper
// set, in the order of names. An error names the header, never its value:
// a value the helper left empty, or one with a line break, which would
// start a header of its own.
func joinHeaders(names []string, getenv func(string) string) (string, error) {
	lines := make([]string, 0, len(names))
	for i, n := range names {
		if err := account.ValidateHeaderName(n); err != nil {
			return "", err
		}
		v := getenv(account.HeaderVar(i))
		if v == "" {
			return "", fmt.Errorf("account header %s: the secret helper set no value for it", n)
		}
		if strings.ContainsAny(v, "\r\n\x00") {
			return "", fmt.Errorf("account header %s: its value holds a line break, which would start another header; claude was not started", n)
		}
		lines = append(lines, n+": "+v)
	}
	return strings.Join(lines, "\n"), nil
}

// withoutPrefix removes every variable whose name starts with prefix.
func withoutPrefix(env []string, prefix string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return out
}

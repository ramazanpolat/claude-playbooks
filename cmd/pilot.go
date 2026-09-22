package cmd

import "os/exec"

// wirePilotProfile connects a newly created or installed playbook to the
// pilot-profile component (https://github.com/agent-realm/pilot-profile), if
// that component is installed on this machine.
//
// pilot-profile is a separate component, not a playbook: it owns the user's
// shared profile at ~/.pilot-profile/ and the tooling that maintains it. The
// dependency edge points one way only -- claude-playbook may call pilot, and
// pilot must never need claude-playbook, because the stock ~/.claude is not
// managed by this tool and still deserves a profile. So nothing about the
// profile's schema, capture protocol or migrations is vendored here; this
// function is the entire integration surface.
//
// Best-effort by contract, and the discarded error is the point rather than an
// oversight: a broken, half-installed or merely unlucky `pilot` must never be
// able to fail a playbook create or install. When pilot is absent this does
// nothing and says nothing -- it does not prompt, suggest, or fetch. `pilot
// wire` is itself idempotent and declines to dirty a tracked CLAUDE.md, so
// running it repeatedly is a no-op.
func wirePilotProfile(dest string) {
	pilot, err := exec.LookPath("pilot")
	if err != nil {
		return
	}
	_ = exec.Command(pilot, "wire", dest).Run()
}

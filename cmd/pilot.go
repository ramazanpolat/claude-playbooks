package cmd

import (
	"context"
	"os/exec"
	"time"
)

// pilotWireTimeout bounds the optional wire step. `pilot wire` edits one
// playbook's CLAUDE.md/CLAUDE.local.md and is idempotent, so seconds is
// generous; the number exists to cap a pathological binary, not to pace a
// healthy one.
const pilotWireTimeout = 5 * time.Second

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
// nothing and says nothing -- it does not prompt, suggest, or fetch.
//
// "Never fail the install" has to mean "never HANG the install" too, which is
// why the call is bounded. An unbounded Run() on a pilot that starts and never
// exits would block forever, and hanging is worse than failing because failing
// is at least visible. Callers additionally release the machine-global registry
// lock before calling this, so even a pilot that burns the whole timeout cannot
// stall claude-playbook for every other playbook on the machine. The timeout is
// discarded exactly like any other failure.
// releaseOnce wraps a lock's unlock function so it can be called early and
// still be deferred safely. The create and install paths release the registry
// lock before the optional wire step, but must keep the deferred release for
// every error path that returns before reaching it.
func releaseOnce(unlock func()) func() {
	released := false
	return func() {
		if released {
			return
		}
		released = true
		unlock()
	}
}

func wirePilotProfile(dest string) {
	pilot, err := exec.LookPath("pilot")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), pilotWireTimeout)
	defer cancel()
	_ = exec.CommandContext(ctx, pilot, "wire", dest).Run()
}

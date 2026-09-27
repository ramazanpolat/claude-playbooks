#!/usr/bin/env python3
"""Pins the local drift in gentar/dryrun.py (policy.toml [check] allow_drift).

A goal pilot is reported UNVERIFIED without starting its driver: a driver
that never exits must not block the dry-run, and its verify must not run.
Its oracle steps still run, and a failing step is still a failure.
Needs no engine: the scenario parser is stubbed. Remove this file together
with the drift, at the gentar tag that carries the fix.

    python3 gentar/test_dryrun_goal.py
"""
import importlib.util
import io
import os
import signal
import sys
import tempfile
import time
import types
import unittest
from contextlib import redirect_stdout
from pathlib import Path

HERE = Path(__file__).resolve().parent


class FakeScenario:
    """What run_one reads from a goal-pilot scenario."""
    credentials: list = []
    turns: list = []
    files: list = []
    steps: list = []
    # A driver that would block the dry-run if started (bounded, so an
    # unpatched dry-run leaves no long-lived process behind).
    driver_command = "sleep 30"
    goal = "a goal"

    def __init__(self, path):
        self.commands = [{"command": "echo verify-ran > \"$HOME/verify-ran\"", "contains": "never"}]


def load_dryrun():
    """Import gentar/dryrun.py with the engine's parser stubbed (and an
    empty directory standing in for the engine it refuses to start without)."""
    os.environ["GENTAR_ENGINE"] = tempfile.mkdtemp()
    pkg = types.ModuleType("gentar")
    pkg.__path__ = []
    mod = types.ModuleType("gentar.toml_scenario")
    mod.TomlScenario = FakeScenario
    sys.modules["gentar"] = pkg
    sys.modules["gentar.toml_scenario"] = mod
    spec = importlib.util.spec_from_file_location("kit_dryrun", HERE / "dryrun.py")
    dryrun = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(dryrun)
    return dryrun


DRYRUN = load_dryrun()


class GoalPilotDryRun(unittest.TestCase):
    def run_one(self, steps, unverified_ok):
        FakeScenario.steps = steps
        home = tempfile.mkdtemp()
        env = dict(os.environ, HOME=home)
        if unverified_ok:
            os.environ["GENTAR_DRYRUN_UNVERIFIED"] = "ok"
        else:
            os.environ.pop("GENTAR_DRYRUN_UNVERIFIED", None)
        out, start = io.StringIO(), time.monotonic()

        def blocked(signum, frame):
            raise TimeoutError("the dry-run blocked: it started the goal pilot's driver")
        signal.signal(signal.SIGALRM, blocked)
        signal.alarm(10)
        try:
            with redirect_stdout(out):
                rc = DRYRUN.run_one(Path("goal.toml"), env, home, home)
        finally:
            signal.alarm(0)
        return rc, out.getvalue(), time.monotonic() - start, home

    def test_unverified_without_starting_the_driver(self):
        rc, out, took, home = self.run_one(["true"], unverified_ok=True)
        self.assertEqual(rc, 0)
        self.assertIn("goal.toml: UNVERIFIED (goal pilot", out)
        self.assertLess(took, 10, "the driver was started")
        self.assertFalse(Path(home, "verify-ran").exists(), "verify ran with nothing driven")

    def test_unverified_fails_outside_check(self):
        rc, out, _, _ = self.run_one(["true"], unverified_ok=False)
        self.assertNotEqual(rc, 0)
        self.assertIn("UNVERIFIED", out)

    def test_a_failing_step_is_still_a_failure(self):
        rc, out, _, _ = self.run_one(["exit 3"], unverified_ok=True)
        self.assertNotEqual(rc, 0)
        self.assertIn("goal.toml: FAILURE (goal pilot", out)


if __name__ == "__main__":
    unittest.main(verbosity=2)

#!/usr/bin/env bash
# check-cockpit-contract -- the nine behaviours cockpit relies on
# (docs/handoffs/cockpit-ship.md §4, plus 10-12 from cockpit's own call sites),
# checked against one claude-playbook binary.
#
#   check-cockpit-contract.sh <path-to-claude-playbook>
#
# Hermetic: a throwaway HOME, a stub `claude` that records what it was given,
# a local git repo for the install-from-git case. Prints one PASS/FAIL line per
# check; run it against two versions and diff the output. Item 9 (release asset
# names) is a GitHub query, done outside this script.
set -u
BIN=$(readlink -f "$1")
W=$(mktemp -d); trap 'rm -rf "$W"' EXIT
export HOME=$W/home; mkdir -p "$HOME/bin"
export PATH=$HOME/bin:/usr/bin:/bin
unset CLAUDE_PLAYBOOKS_DIR CLAUDE_CONFIG_DIR CLAUDE_CONFIG_DIR_OVERRIDE
cp "$BIN" "$HOME/bin/claude-playbook"; C=$HOME/bin/claude-playbook
cat > "$HOME/bin/claude" <<'EOF'
#!/bin/sh
{ echo "CFG=${CLAUDE_CONFIG_DIR:-}"; echo "OVR=${CLAUDE_CONFIG_DIR_OVERRIDE-<unset>}"
  for a in "$@"; do echo "ARG=$a"; done; } > "$HOME/claude-called"
exit "${STUB_RC:-0}"
EOF
chmod +x "$HOME/bin/claude"
ok() { echo "PASS $1"; }
no() { echo "FAIL $1 -- $2"; }
chk() { local id=$1; shift; if "$@" >/dev/null 2>&1; then ok "$id"; else no "$id" "${DETAIL:-}"; fi; DETAIL=; }

FIX=$W/fix; mkdir -p "$FIX"; printf 'name = "fx"\n' > "$FIX/.playbook"; printf '# fx\n' > "$FIX/CLAUDE.md"
DEF=$HOME/.claude-playbooks ENVR=$W/root-env FLAG=$W/root-flag

# 7. --version
V=$($C --version 2>&1); DETAIL="got: $V"
# Strict by default: a RELEASE binary prints a bare vX.Y.Z, and that is what
# cockpit parses. A build from a checkout is stamped with whatever `git describe`
# gave (v3.18.0-1-gce1c8b0, or a bare commit in a tagless shallow clone); the
# arena suite passes that exact stamp as CONTRACT_EXPECT_VERSION, and check 7
# then requires exactly it -- stronger than any pattern.
version_ok() {
  if [ -n "${CONTRACT_EXPECT_VERSION:-}" ]; then
    [ "$V" = "claude-playbook version $CONTRACT_EXPECT_VERSION" ]
  else
    printf '%s' "$V" | grep -Eq '^claude-playbook version v[0-9]+\.[0-9]+\.[0-9]+$'
  fi
}
chk "7 --version prints 'claude-playbook version vX.Y.Z'" version_ok

# 1. playbooks-root precedence: flag > CLAUDE_PLAYBOOKS_DIR > ~/.claude-playbooks
$C install "$FIX" --name pdef --no-alias >/dev/null 2>&1
CLAUDE_PLAYBOOKS_DIR=$ENVR $C install "$FIX" --name penv --no-alias >/dev/null 2>&1
CLAUDE_PLAYBOOKS_DIR=$ENVR $C --playbooks-dir "$FLAG" install "$FIX" --name pflag --no-alias >/dev/null 2>&1
chk "1a install: default root"       test -f "$DEF/pdef/CLAUDE.md"
chk "1b install: env root"           test -f "$ENVR/penv/CLAUDE.md"
chk "1c install: flag beats env"     sh -c "test -f '$FLAG/pflag/CLAUDE.md' && test ! -e '$ENVR/pflag'"
I=$(CLAUDE_PLAYBOOKS_DIR=$ENVR $C info penv 2>&1); DETAIL="got: $(printf '%s' "$I" | grep Path: )"
chk "1d info: env root"              sh -c "printf '%s' \"\$0\" | grep -q '^Path: *$ENVR/penv'" "$I"
I=$(CLAUDE_PLAYBOOKS_DIR=$ENVR $C --playbooks-dir "$FLAG" info pflag 2>&1)
chk "1e info: flag beats env"        sh -c "printf '%s' \"\$0\" | grep -q '^Path: *$FLAG/pflag'" "$I"
# A refusal, not a crash: non-zero AND the not-found message. A negated
# command would also "pass" on a missing binary or a mangled path.
unknown_pb() { local o rc; o=$("$@" 2>&1); rc=$?; [ "$rc" != 0 ] && printf '%s' "$o" | grep -q 'unknown playbook'; }
flag_hides_env() { CLAUDE_PLAYBOOKS_DIR=$ENVR unknown_pb "$C" --playbooks-dir "$FLAG" info penv; }
chk "1f info: flag root hides env-only playbook" flag_hides_env
rm -f "$HOME/claude-called"; CLAUDE_PLAYBOOKS_DIR=$ENVR $C run penv >/dev/null 2>&1
chk "1g run: env root sets CLAUDE_CONFIG_DIR" grep -qx "CFG=$ENVR/penv" "$HOME/claude-called"
rm -f "$HOME/claude-called"; CLAUDE_PLAYBOOKS_DIR=$ENVR $C --playbooks-dir "$FLAG" run pflag >/dev/null 2>&1
chk "1h run: flag beats env"         grep -qx "CFG=$FLAG/pflag" "$HOME/claude-called"
CLAUDE_PLAYBOOKS_DIR=$ENVR $C env-profile pe set A=1 >/dev/null 2>&1
CLAUDE_PLAYBOOKS_DIR=$ENVR $C --playbooks-dir "$FLAG" env-profile pf set A=1 >/dev/null 2>&1
chk "1i env-profile: env root store" test -f "$ENVR/.env-profiles/pe.toml"
chk "1j env-profile: flag beats env" sh -c "test -f '$FLAG/.env-profiles/pf.toml' && test ! -e '$ENVR/.env-profiles/pf.toml'"
CLAUDE_PLAYBOOKS_DIR=$ENVR $C env penv set KE=1 >/dev/null 2>&1
CLAUDE_PLAYBOOKS_DIR=$ENVR $C --playbooks-dir "$FLAG" env pflag set KF=1 >/dev/null 2>&1
chk "1k env: env root manifest"      grep -q KE "$ENVR/penv/.playbook"
chk "1l env: flag beats env"         grep -q KF "$FLAG/pflag/.playbook"

# 2. install <local-dir> --name --no-alias under a custom root; --alias there writes no launcher.
#    "No launcher" is judged on BOTH candidate launcher dirs -- the binary's own
#    ($HOME/bin here) and ~/.local/bin (the fallback for an unwritable one) --
#    and on every entry, whatever its name.
bins() { ls -A "$HOME/bin" "$HOME/.local/bin" 2>/dev/null | sort; }
mkdir -p "$HOME/.local/bin"
before=$(bins)
O=$($C --playbooks-dir "$FLAG" install "$FIX" --name pna --no-alias 2>&1); rc=$?
DETAIL="rc=$rc bins changed: $(diff <(printf '%s\n' "$before") <(bins) | grep '^[<>]' | tr '\n' ' ')"
na_ok() { [ "$rc" = 0 ] && test -f "$FLAG/pna/CLAUDE.md" && [ "$before" = "$(bins)" ]; }
chk "2a install --no-alias under custom root: installs, writes no launcher" na_ok
before=$(bins)
O=$($C --playbooks-dir "$FLAG" install "$FIX" --name pal --alias palx 2>&1); rc=$?
after=$(bins)
DETAIL="rc=$rc out: $(printf '%s' "$O" | tr '\n' ' ' | cut -c1-160)"
chk "2b --alias under custom root: installs" sh -c "[ $rc = 0 ] && test -f '$FLAG/pal/CLAUDE.md'"
chk "2c --alias under custom root: prints the default-root note" sh -c "printf '%s' \"\$0\" | grep -q 'launchers are managed only for the default playbooks root'" "$O"
DETAIL="bins changed: $(diff <(printf '%s\n' "$before") <(printf '%s\n' "$after") | grep '^[<>]' | tr '\n' ' ')"
no_launcher() { [ "$before" = "$after" ] && [ -z "$(find "$W" -name palx 2>/dev/null)" ]; }
chk "2d --alias under custom root: writes no launcher (any name, either dir)" no_launcher

# 3. install <git-url> --branch <tag> --subdir <dir> --name N
G=$W/src; mkdir -p "$G/sub/pb"; printf 'name = "g"\nversion = "1.0.0"\n' > "$G/sub/pb/.playbook"; printf 'TAGGED\n' > "$G/sub/pb/CLAUDE.md"
git -C "$G" init -q -b main && git -C "$G" -c user.email=t@t -c user.name=t add -A && git -C "$G" -c user.email=t@t -c user.name=t commit -qm one && git -C "$G" tag v1.0.0
printf 'MOVED\n' > "$G/sub/pb/CLAUDE.md"; git -C "$G" -c user.email=t@t -c user.name=t commit -qam two
O=$($C --playbooks-dir "$FLAG" install "file://$G" --branch v1.0.0 --subdir sub/pb --name gpb --no-alias 2>&1); DETAIL="out: $(printf '%s' "$O" | tr '\n' ' ' | cut -c1-200)"
chk "3 install git-url --branch tag --subdir --name: the TAGGED tree" grep -qx TAGGED "$FLAG/gpb/CLAUDE.md"

# 4. run <name> [args...] forwards arguments verbatim
rm -f "$HOME/claude-called"; $C --playbooks-dir "$FLAG" run pflag --model some-model "two words" '$HOME' -p "x;y" >/dev/null 2>&1
exp=$(printf 'ARG=%s\n' --model some-model "two words" '$HOME' -p "x;y")
got=$(grep '^ARG=' "$HOME/claude-called" 2>/dev/null); DETAIL="got: $(printf '%s' "$got" | tr '\n' '|')"
chk "4 run forwards args verbatim" test "$exp" = "$got"

# 5. CLAUDE_CONFIG_DIR_OVERRIDE consumed by run, not propagated
mkdir -p "$W/ovr"; rm -f "$HOME/claude-called"
CLAUDE_CONFIG_DIR_OVERRIDE=$W/ovr $C --playbooks-dir "$FLAG" run pflag >/dev/null 2>&1
DETAIL="got: $(tr '\n' '|' < "$HOME/claude-called" 2>/dev/null)"
chk "5a override sets CLAUDE_CONFIG_DIR" grep -qx "CFG=$W/ovr" "$HOME/claude-called"
chk "5b override not propagated to claude" grep -qx "OVR=<unset>" "$HOME/claude-called"

# 6. info prints Name: and Path:
I=$($C --playbooks-dir "$FLAG" info pflag 2>&1)
chk "6 info prints 'Name:' and 'Path:' lines" sh -c "printf '%s' \"\$0\" | grep -q '^Name:' && printf '%s' \"\$0\" | grep -q '^Path:'" "$I"

# 8. env-profile store format and the manifest's [env] profiles
$C --playbooks-dir "$FLAG" env-profile pf unset B >/dev/null 2>&1
$C --playbooks-dir "$FLAG" env-profile pf describe "a description" >/dev/null 2>&1
P=$FLAG/.env-profiles/pf.toml
chk "8a store: <root>/.env-profiles/<name>.toml with [set]" grep -q '^\[set\]' "$P"
chk "8b store: unset key"          grep -Eq '^unset *=' "$P"
chk "8c store: description key"    grep -Eq '^description *=' "$P"
$C --playbooks-dir "$FLAG" env pflag use pf >/dev/null 2>&1
chk "8d manifest: [env] profiles = [...]" sh -c "grep -q '^\[env\]' '$FLAG/pflag/.playbook' && grep -Eq '^profiles *= *\[' '$FLAG/pflag/.playbook'"

# §5.1 (informational, not contract): a manifest rewrite DROPS an unknown
# table -- why cockpit keeps its base pin in base.toml. Asserted as documented,
# and only after the rewrite demonstrably ran.
printf '\n[cockpit]\nbase = "x"\n' >> "$FLAG/pal/.playbook"
$C --playbooks-dir "$FLAG" env pal use pf >/dev/null 2>&1; rc=$?
DETAIL="rc=$rc manifest: $(tr '\n' '|' < "$FLAG/pal/.playbook")"
dropped_ok() { [ "$rc" = 0 ] && grep -Eq '^profiles *= *\[.*"pf"' "$FLAG/pal/.playbook" && ! grep -q '^\[cockpit\]' "$FLAG/pal/.playbook"; }
chk "info §5.1 manifest rewrite drops an unknown [cockpit] table" dropped_ok

# 10. info exits non-zero for a name that is not installed, 0 for one that is
#     (cockpit setup.sh:36 chooses fresh install vs refresh on it)
chk "10a info <missing> exits non-zero" unknown_pb "$C" --playbooks-dir "$FLAG" info not-installed
chk "10b info <installed> exits 0"      $C --playbooks-dir "$FLAG" info pflag

# 11. run propagates claude's exit status (cockpit check.sh:38)
STUB_RC=3 $C --playbooks-dir "$FLAG" run pflag >/dev/null 2>&1; rc=$?; DETAIL="rc=$rc"
chk "11a run returns claude's rc (3)"   test "$rc" = 3
STUB_RC=0 $C --playbooks-dir "$FLAG" run pflag >/dev/null 2>&1; rc=$?; DETAIL="rc=$rc"
chk "11b run returns claude's rc (0)"   test "$rc" = 0

# 12. [source] (repository, subdir) and version survive install and an env rewrite
#     (/cockpit new reinstalls the running release from them)
M=$FLAG/gpb/.playbook
src_ok() { grep -q '^\[source\]' "$M" && grep -Eq "^repository *= *\"file://$G\"" "$M" && grep -Eq '^subdir *= *"sub/pb"' "$M" && grep -Eq '^version *= *"1\.0\.0"' "$M"; }
DETAIL="manifest: $(tr '\n' '|' < "$M" 2>/dev/null)"
chk "12a after install: [source] repository+subdir, version" src_ok
# The rewrite must have RUN (exit 0, and the manifest now names the profile),
# or a failed command would leave 12a's file and pass on stale content.
$C --playbooks-dir "$FLAG" env gpb use pf >/dev/null 2>&1; rc=$?
DETAIL="rc=$rc manifest: $(tr '\n' '|' < "$M" 2>/dev/null)"
uses_pf() { grep -Eq '^profiles *= *\[.*"pf"' "$1"; }
rewrite_ok() { [ "$rc" = 0 ] && uses_pf "$M" && src_ok; }
chk "12b after env use rewrite: [source] repository+subdir, version kept" rewrite_ok

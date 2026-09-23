#!/usr/bin/env bash
# check-cockpit-contract -- the nine behaviours cockpit relies on
# (docs/handoffs/cockpit-ship.md §4), checked against one claude-playbook binary.
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
EOF
chmod +x "$HOME/bin/claude"
ok() { echo "PASS $1"; }
no() { echo "FAIL $1 -- $2"; }
chk() { local id=$1; shift; if "$@" >/dev/null 2>&1; then ok "$id"; else no "$id" "${DETAIL:-}"; fi; DETAIL=; }

FIX=$W/fix; mkdir -p "$FIX"; printf 'name = "fx"\n' > "$FIX/.playbook"; printf '# fx\n' > "$FIX/CLAUDE.md"
DEF=$HOME/.claude-playbooks ENVR=$W/root-env FLAG=$W/root-flag

# 7. --version
V=$($C --version 2>&1); DETAIL="got: $V"
chk "7 --version prints 'claude-playbook version vX.Y.Z'" sh -c "printf '%s' '$V' | grep -Eq '^claude-playbook version v[0-9]+\.[0-9]+\.[0-9]+$'"

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
chk "1f info: flag root hides env-only playbook" sh -c "! CLAUDE_PLAYBOOKS_DIR=$ENVR $C --playbooks-dir $FLAG info penv"
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

# 2. install <local-dir> --name --no-alias under a custom root; --alias there writes no launcher
before=$(ls "$HOME/bin" | sort)
O=$($C --playbooks-dir "$FLAG" install "$FIX" --name pal --alias palx 2>&1); rc=$?
after=$(ls "$HOME/bin" | sort)
chk "2a install --no-alias under custom root" test -f "$FLAG/pflag/CLAUDE.md"
DETAIL="rc=$rc out: $(printf '%s' "$O" | tr '\n' ' ' | cut -c1-160)"
chk "2b --alias under custom root: installs" sh -c "[ $rc = 0 ] && test -f '$FLAG/pal/CLAUDE.md'"
chk "2c --alias under custom root: prints the default-root note" sh -c "printf '%s' \"\$0\" | grep -q 'launchers are managed only for the default playbooks root'" "$O"
DETAIL="bin before/after differ"
chk "2d --alias under custom root: writes no launcher" test "$before" = "$after"

# 3. install <git-url> --branch <tag> --subdir <dir> --name N
G=$W/src; mkdir -p "$G/sub/pb"; printf 'name = "g"\n' > "$G/sub/pb/.playbook"; printf 'TAGGED\n' > "$G/sub/pb/CLAUDE.md"
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

# §5.1 (informational): does a manifest rewrite keep an unknown table?
printf '\n[cockpit]\nbase = "x"\n' >> "$FLAG/pal/.playbook"
$C --playbooks-dir "$FLAG" env pal use pf >/dev/null 2>&1
chk "info §5.1 manifest rewrite keeps an unknown [cockpit] table" grep -q '^\[cockpit\]' "$FLAG/pal/.playbook"

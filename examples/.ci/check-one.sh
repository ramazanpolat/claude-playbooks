# One example of examples/check.sh, run as `sh -e`: every command that fails
# ends the run (errexit is ignored inside an AND-OR list or an if-condition,
# so a subshell in check.sh could not be trusted to stop).
# Arguments: <example dir> <throwaway HOME> <examples/.ci>
dir=$1 home=$2 ci=$3
entry=playbook.cpb
[ -f "$dir/.entry" ] && entry=$(cat "$dir/.entry")
export HOME="$home" PATH="$ci:$home/bin:$PATH" CPB_SECRET_HELPER= FAKE_MARKETS=""
cd "$dir"
if [ -f .setup ]; then sh -e .setup; fi
cpb APPLY "$entry" --dry-run > "$home/dry.out"
cpb APPLY "$entry" --yes > "$home/apply.out"
cpb APPLY "$entry" --yes > "$home/again.out"
grep -q " 0 created, 0 changed, " "$home/again.out"
# .check: what the README shows beyond APPLY (a query, a second target).
if [ -f .check ]; then sh -e .check; fi

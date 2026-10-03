#!/bin/sh
# Tests bin/npx-shim.sh's download path with a stub curl serving a fake
# release store: which release it runs when the package's own version is not
# published yet (#142), and when it refuses. Nothing touches the network.
# Run by CI on both OSes: sh .github/scripts/npx-shim_test.sh
set -eu
here=$(cd "$(dirname "$0")" && pwd)
shim="$here/../../bin/npx-shim.sh"
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in x86_64) arch=amd64 ;; *) arch=arm64 ;; esac
asset="cpb-$os-$arch"
store="$t/store" # store/<tag>/<asset>, store/<tag>/SHA256SUMS, store/latest

# publish <tag>: a release whose binary prints its own tag and its args.
publish() {
  mkdir -p "$store/$1"
  printf '#!/bin/sh\necho "cpb %s $*"\n' "$1" > "$store/$1/$asset"
  if command -v sha256sum >/dev/null 2>&1; then s=$(sha256sum "$store/$1/$asset" | awk '{print $1}')
  else s=$(shasum -a 256 "$store/$1/$asset" | awk '{print $1}'); fi
  printf '%s  %s\n' "$s" "$asset" > "$store/$1/SHA256SUMS"
}
latest() { printf '{\n  "tag_name": "%s",\n  "prerelease": false\n}\n' "$1" > "$store/latest"; }

# The stub curl: requests with -w '%{http_code}' (downloads, the latest
# release) print the HTTP code, with FAKE_HTTP forcing one for every such
# request and FAKE_LATEST_HTTP for the latest-release lookup alone; -f
# fetches (sums) fail with curl's exit 22 when the file is missing. Every
# URL is logged.
mkdir -p "$t/stub"
cat > "$t/stub/curl" <<'EOF'
#!/bin/sh
out="" w="" url=""
while [ $# -gt 0 ]; do
  case "$1" in -o) out=$2; shift ;; -w) w=$2; shift ;; -*) ;; *) url=$1 ;; esac
  shift
done
echo "$url" >> "$FAKE_STORE/../curl.log"
case "$url" in
  */releases/latest) f="$FAKE_STORE/latest" ;;
  https://dl.test/*) f="$FAKE_STORE/${url#https://dl.test/}" ;;
  *) exit 6 ;;
esac
if [ -n "$w" ]; then
  # The body goes to -o FILE, or to stdout before the -w text, whose
  # %{http_code} is the status.
  code="" body=""
  case "$url" in */releases/latest) code=${FAKE_LATEST_HTTP:-} ;; esac
  [ -n "$code" ] || code=${FAKE_HTTP:-}
  if [ -z "$code" ]; then
    if [ -f "$f" ]; then code=200; body=$f; else code=404; fi
  fi
  if [ -n "$out" ]; then
    if [ -n "$body" ]; then cp "$body" "$out"; else printf 'Not Found' > "$out"; fi
  elif [ -n "$body" ]; then
    cat "$body"
  fi
  printf "$(printf '%s' "$w" | sed "s/%{http_code}/$code/")"
  exit 0
fi
[ -f "$f" ] || exit 22
if [ -n "$out" ]; then cp "$f" "$out"; else cat "$f"; fi
EOF
chmod +x "$t/stub/curl"

fail=0
n=0
# run <name>: the shim in ephemeral mode (a fresh cache per case unless
# KEEP_CACHE=1), stdout to $t/out, stderr to $t/err, exit code to $rc.
run() {
  n=$((n + 1))
  [ "${KEEP_CACHE:-}" = 1 ] || rm -rf "$t/cache"
  : > "$t/curl.log"
  set +e
  env PATH="$t/stub:/usr/bin:/bin" HOME="$t/home" FAKE_STORE="$store" \
    CPB_INSTALL_DOWNLOAD_BASE=https://dl.test CPB_NPX_CACHE="$t/cache" CPB_NPX_BOOTSTRAP=0 \
    "$@" sh "$shim" --version > "$t/out" 2> "$t/err"
  rc=$?
  set -e
}
check() { # check <description> <condition...>
  d=$1; shift
  if "$@"; then echo "ok:   $d"; else echo "FAIL: $d"; sed 's/^/        stderr: /' "$t/err"; fail=1; fi
}
out_is() { [ "$(cat "$t/out")" = "$1" ]; }
err_has() { grep -qF -- "$1" "$t/err"; }
no_err() { ! grep -qF -- "$1" "$t/err"; }
fetched() { grep -qF -- "$1" "$t/curl.log"; }

publish v3.25.0
latest v3.25.0

# The package's version is published: used unchanged, no notice, no lookup.
run npm_package_version=3.25.0
check "a published package version runs as is" [ $rc = 0 ]
check "  it is that release" out_is "cpb v3.25.0 --version"
check "  with no notice" no_err "not published yet"
check "  and no latest-release lookup" sh -c "! grep -q releases/latest '$t/curl.log'"
check "  and its checksum verified" err_has "Checksum verified"

# The bump-before-tag window: 3.26.0 is not published, v3.25.0 is newest.
run npm_package_version=3.26.0
check "an unpublished package version falls back" [ $rc = 0 ]
check "  to the newest published release" out_is "cpb v3.25.0 --version"
check "  and says so, in one line" err_has "cpb v3.26.0 is not published yet; running v3.25.0"
check "  after trying its own release first" fetched "https://dl.test/v3.26.0/$asset"
check "  into the fallback's own cache" [ -x "$t/cache/v3.25.0/cpb" ]
check "  leaving no temp file behind" sh -c "! ls -A '$t/cache/v3.26.0' 2>/dev/null | grep -q ."

# A second run reuses the fallback's cached binary: no second download.
KEEP_CACHE=1
run npm_package_version=3.26.0
KEEP_CACHE=
check "a second fallback run reuses the cache" [ $rc = 0 ]
check "  and does not download v3.25.0 again" sh -c "! grep -q 'v3.25.0/$asset' '$t/curl.log'"

# Never across a major, never to an rc, never to the missing tag itself.
publish v4.0.0; latest v4.0.0
run npm_package_version=3.26.0
check "no fallback across a major version" [ $rc != 0 ]
check "  it says why" err_has "(v4.0.0) cannot stand in"
check "  and runs nothing" out_is ""

latest v3.26.0-rc1
run npm_package_version=3.26.0
check "no fallback to an rc" [ $rc != 0 ]
check "  it says why, naming the rc" err_has "(latest: v3.26.0-rc1)"
check "  and runs nothing" out_is ""

latest v3.26.0
run npm_package_version=3.26.0
check "no fallback to the missing release itself (assets not uploaded yet)" [ $rc != 0 ]
check "  it says why" err_has "(v3.26.0) cannot stand in"

rm -f "$store/latest"
run npm_package_version=3.26.0
check "no fallback when the latest release cannot be read" [ $rc != 0 ]
check "  it says why" err_has "cpb v3.26.0 is not published yet, and the newest release could not be looked up: HTTP 404 from https://api.github.com"
latest v3.25.0

# A rate-limited fallback lookup says so.
run npm_package_version=3.26.0 FAKE_LATEST_HTTP=403
check "a rate-limited fallback lookup refuses" [ $rc != 0 ]
check "  naming the rate limit" err_has "Error: cpb v3.26.0 is not published yet, and the newest release could not be looked up: GitHub rate-limited this request (HTTP 403; unauthenticated requests share your IP). Try again later, or pin one: CPB_NPX_VERSION=vX.Y.Z"
check "  and runs nothing" out_is ""

# No version given: the latest release runs, and a lookup that fails says
# why. 403 and 429 are a rate limit, not the network.
run
check "with no version, the latest release runs" out_is "cpb v3.25.0 --version"
for c in 403 429; do
  run FAKE_LATEST_HTTP=$c
  check "a $c on the lookup refuses" [ $rc != 0 ]
  check "  naming the rate limit" err_has "Error: GitHub rate-limited this request (HTTP $c; unauthenticated requests share your IP): pin a release to skip the lookup,"
  check "  with the pin" err_has "CPB_NPX_VERSION=v4.0.0 npx github:"
  check "  and not the network" no_err "internet connection"
  check "  and runs nothing" out_is ""
done
run FAKE_LATEST_HTTP=500
check "another status on the lookup names it" err_has "could not determine which release to fetch (HTTP 500 from https://api.github.com). Check your internet connection,"
check "  and runs nothing" out_is ""

# An explicit CPB_NPX_VERSION is the pilot's pin: a missing one is an error.
run CPB_NPX_VERSION=v3.26.0
check "a missing CPB_NPX_VERSION is not replaced" [ $rc != 0 ]
check "  it names the 404" err_has "v3.26.0 has no $asset (HTTP 404)"
check "  and runs nothing" out_is ""
run CPB_NPX_VERSION=v3.25.0 npm_package_version=3.26.0
check "CPB_NPX_VERSION wins over the package version" out_is "cpb v3.25.0 --version"

# Only a 404 means "not published": any other failure is an error.
run npm_package_version=3.26.0 FAKE_HTTP=500
check "an HTTP 500 is an error, not a fallback" [ $rc != 0 ]
check "  it names the code" err_has "could not download $asset v3.26.0 (500)"
check "  and does not look up the latest release" sh -c "! grep -q releases/latest '$t/curl.log'"

# Bootstrap mode (the default) installs the fallback and says which.
n=$((n + 1)); rm -rf "$t/inst"; : > "$t/curl.log"
set +e
env PATH="$t/stub:/usr/bin:/bin" HOME="$t/home" FAKE_STORE="$store" \
  CPB_INSTALL_DOWNLOAD_BASE=https://dl.test CPB_NPX_INSTALL_DIR="$t/inst" npm_package_version=3.26.0 \
  sh "$shim" --version > "$t/out" 2> "$t/err"
rc=$?
set -e
check "bootstrap falls back too" out_is "cpb v3.25.0 --version"
check "  and installs it as cpb, the only name" sh -c "[ -x '$t/inst/cpb' ] && [ ! -L '$t/inst/cpb' ] && [ \"\$(ls '$t/inst')\" = cpb ]"
check "  naming the release it installed" err_has "Installed cpb v3.25.0"

exit $fail

#!/bin/sh
# Tests deploy/autodeploy/autodeploy.sh against stub kubectl and curl, using
# the real deploy/deployment.yaml and deploy/ingress.yaml as the "main"
# manifests. CI runs it inside the CronJob's image (busybox sh/tar), see
# .github/workflows/ci.yml; it also runs under any POSIX sh with GNU tools:
#
#   sh hack/autodeploy-test.sh
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
SCRIPT="$ROOT/deploy/autodeploy/autodeploy.sh"
FAILS=0
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

NEW_IMAGE=$(sed -nE 's/^[[:space:]]+image:[[:space:]]*([^[:space:]]+)[[:space:]]*$/\1/p' "$ROOT/deploy/deployment.yaml")
NEW_VERSION=$(grep -B 1 -E '^[[:space:]]+image:' "$ROOT/deploy/deployment.yaml" | sed -nE 's/^[[:space:]]*# main ([0-9a-f]{40}).*/\1/p')
OLD_IMAGE=ghcr.io/hivecommons/dibs@sha256:0000000000000000000000000000000000000000000000000000000000000000
OLD_VERSION=1111111111111111111111111111111111111111
HEAD_SHA=2222222222222222222222222222222222222222

# --- stubs -----------------------------------------------------------------
mkdir -p "$TMP/bin"
cat >"$TMP/bin/kubectl" <<'STUB'
#!/bin/sh
S=$STUB_STATE
printf 'kubectl %s\n' "$*" >>"$S/calls"
[ "$1" = -n ] && shift 2
cmd=$1
shift
jp=
files=
dry=
while [ $# -gt 0 ]; do
	case "$1" in
	-o) jp=$2; shift ;;
	-o*) jp=${1#-o} ;;
	-f) files="$files $2"; shift ;;
	--dry-run=*) dry=${1#--dry-run=} ;;
	esac
	shift
done
case "$cmd" in
get)
	case "$jp" in
	*containers*) cat "$S/live_image" ;;
	*autodeploy-pause*) cat "$S/ann_pause" 2>/dev/null || true ;;
	*autodeploy-failed-image*) cat "$S/ann_failed" 2>/dev/null || true ;;
	*generation*) cat "$S/steady" 2>/dev/null || printf '7 7 1 1 1 1' ;;
	esac
	;;
create)
	case "$jp" in
	*backend.service.name*) printf 'dibs' ;;
	*serviceAccountName*) printf 'Deployment|dibs|dibs||false' ;;
	*) printf 'Ingress|dibs|dibs' ;;
	esac
	;;
apply)
	[ "$dry" = server ] && exit 0
	for f in $files; do
		if grep -q '^kind: Deployment' "$f"; then
			sed -nE 's/^[[:space:]]+image:[[:space:]]*([^[:space:]]+)[[:space:]]*$/\1/p' "$f" >"$S/live_image"
		fi
	done
	[ -f "$S/apply_rc" ] && exit "$(cat "$S/apply_rc")"
	;;
rollout) exit "$(cat "$S/rollout_rc" 2>/dev/null || echo 0)" ;;
set) # set image deployment/dibs dibs=IMAGE (args after "image" kept in $1..)
	;;
annotate) ;;
esac
exit 0
STUB
# `set image` and `annotate` need their positional args, which the generic
# loop above discards; handle them in a wrapper.
mv "$TMP/bin/kubectl" "$TMP/bin/kubectl-core"
cat >"$TMP/bin/kubectl" <<'STUB'
#!/bin/sh
S=$STUB_STATE
args="$*"
case "$args" in
*" set image "*)
	printf 'kubectl %s\n' "$args" >>"$S/calls"
	for a in "$@"; do last=$a; done
	printf '%s' "${last#*=}" >"$S/live_image"
	exit 0
	;;
*" annotate "*)
	printf 'kubectl %s\n' "$args" >>"$S/calls"
	for a in "$@"; do last=$a; done
	case "$last" in
	*autodeploy-failed-image=*) printf '%s' "${last#*=}" >"$S/ann_failed" ;;
	*autodeploy-previous-image=*) printf '%s' "${last#*=}" >"$S/ann_previous" ;;
	esac
	exit 0
	;;
esac
exec "$(dirname "$0")/kubectl-core" "$@"
STUB
cat >"$TMP/bin/curl" <<'STUB'
#!/bin/sh
S=$STUB_STATE
out=
wfmt=
url=
data=
method=GET
while [ $# -gt 0 ]; do
	case "$1" in
	-o) out=$2; shift ;;
	-w) wfmt=$2; shift ;;
	-X) method=$2; shift ;;
	--data-binary) data=${2#@}; shift ;;
	-H | --max-time) shift ;;
	-*) ;;
	*) url=$1 ;;
	esac
	shift
done
printf 'curl %s %s\n' "$method" "$url" >>"$S/calls"
emit() {
	if [ -z "$out" ]; then cat; else cat >"$out"; fi
}
case "$url" in
*/info/refs*) printf '003f%s refs/heads/main\n0000' "$(cat "$S/head_sha")" | emit ;;
*/deploy/deployment.yaml) emit <"$S/fixture-deployment.yaml" ;;
*/deploy/ingress.yaml) emit <"$S/fixture-ingress.yaml" ;;
*/healthz)
	if [ "$(cat "$S/health_mode")" = follow ] && [ "$(cat "$S/live_image")" = "$(cat "$S/new_image")" ]; then
		v=$(cat "$S/new_version")
	else
		v=$(cat "$S/old_version")
	fi
	printf '{"status":"ok","version":"%s"}\n' "$v" | emit
	;;
*/readyz)
	printf 'ok\n' | emit
	[ -n "$wfmt" ] && printf 200
	;;
*"/issues?"*)
	if [ -f "$S/open_issue" ]; then
		printf '[{"url":"u","id":1,"number":%s,"title":"t","milestone":{"number":99}}]' "$(cat "$S/open_issue")" | emit
	else
		printf '[]' | emit
	fi
	;;
*/issues* | */comments*)
	printf '%s %s %s\n' "$method" "$url" "$(cat "$data")" >>"$S/github"
	printf '{}' | emit
	;;
*) exit 22 ;;
esac
exit 0
STUB
chmod +x "$TMP/bin/kubectl" "$TMP/bin/kubectl-core" "$TMP/bin/curl"

# --- harness ---------------------------------------------------------------
# setup NAME: fresh state, data and backup dirs; live = OLD_IMAGE.
setup() {
	CASE=$1
	S="$TMP/$CASE"
	mkdir -p "$S/state" "$S/data/ideas" "$S/data/lost+found" "$S/backups" "$S/work"
	ST="$S/state"
	cp "$ROOT/deploy/deployment.yaml" "$ST/fixture-deployment.yaml"
	cp "$ROOT/deploy/ingress.yaml" "$ST/fixture-ingress.yaml"
	printf '%s' "$HEAD_SHA" >"$ST/head_sha"
	printf '%s' "$OLD_IMAGE" >"$ST/live_image"
	printf '%s' "$NEW_IMAGE" >"$ST/new_image"
	printf '%s' "$NEW_VERSION" >"$ST/new_version"
	printf '%s' "$OLD_VERSION" >"$ST/old_version"
	printf follow >"$ST/health_mode"
	: >"$ST/calls"
	: >"$ST/github"
	printf '[{"id":"a"}]\n' >"$S/data/ideas/index.json"
	printf '{"id":"a","title":"x y"}\n' >"$S/data/ideas/a.json"
	printf '[]\n' >"$S/data/repo-news.json"
	printf '[]\n' >"$S/data/notifications.json"
}

run_script() {
	set +e
	env PATH="$TMP/bin:$PATH" STUB_STATE="$ST" \
		DATA_DIR="$S/data" BACKUP_DIR="$S/backups" WORK_DIR="$S/work" \
		HEALTH_INTERVAL_SECONDS=1 HEALTH_TIMEOUT_SECONDS=3 HEALTH_REQUIRED_SUCCESSES=2 \
		GITHUB_TOKEN=test-token REPORT_ISSUE="${REPORT_ISSUE:-}" \
		BACKUP_RETENTION_COUNT="${RETENTION:-30}" DRY_RUN="${DRY:-false}" \
		sh "$SCRIPT" >"$S/out" 2>&1
	RC=$?
	set -e
}

fail() {
	printf 'FAIL [%s]: %s\n' "$CASE" "$*"
	sed 's/^/    /' "$S/out"
	FAILS=$((FAILS + 1))
}
ok() { printf 'ok   [%s]\n' "$CASE"; }

expect_rc() { [ "$RC" -eq "$1" ] || { fail "exit $RC, want $1"; return 1; }; }
expect_called() { grep -q -- "$1" "$ST/calls" || { fail "expected call matching '$1'"; return 1; }; }
expect_not_called() { if grep -q -- "$1" "$ST/calls"; then fail "unexpected call matching '$1'"; return 1; fi; }
# check CMD...: CMD must succeed, else the case fails with CMD in the message.
check() { "$@" || { fail "check failed: $*"; return 1; }; }
backups() { find "$S/backups" -maxdepth 1 -name 'dibs-data-*.tar' | wc -l | tr -d ' '; }

# --- cases -----------------------------------------------------------------
[ -n "$NEW_IMAGE" ] && [ -n "$NEW_VERSION" ] || {
	echo "cannot parse the pin in deploy/deployment.yaml"
	exit 1
}

setup in-sync
printf '%s' "$NEW_IMAGE" >"$ST/live_image"
run_script
expect_rc 0 && expect_not_called 'apply -f' && check [ "$(backups)" = 0 ] && check grep -q 'in sync' "$S/out" && ok || true

setup success
for i in 1 2 3; do : >"$S/backups/dibs-data-2020010${i}T000000Z-old.tar"; done
RETENTION=2 REPORT_ISSUE=567 run_script
if expect_rc 0 && expect_called "curl GET https://raw.githubusercontent.com/hivecommons/dibs/$HEAD_SHA/deploy/deployment.yaml" &&
	expect_called 'apply -f' && expect_not_called 'set image'; then
	tarf=$(find "$S/backups" -maxdepth 1 -name "dibs-data-*-1111111.tar")
	if [ -z "$tarf" ]; then
		fail "no new backup"
	elif ! tar -tf "$tarf" | grep -qx 'ideas/index.json' || tar -tf "$tarf" | grep -q lost; then
		fail "backup contents wrong: $(tar -tf "$tarf" | tr '\n' ' ')"
	elif [ "$(backups)" != 2 ]; then
		fail "retention kept $(backups) backups, want 2"
	elif [ "$(cat "$ST/live_image")" != "$NEW_IMAGE" ]; then
		fail "live image not updated"
	elif [ "$(cat "$ST/ann_previous")" != "$OLD_IMAGE" ]; then
		fail "previous image not recorded"
	elif ! grep -q 'POST .*/issues/567/comments' "$ST/github"; then
		fail "no success report on #567"
	else
		ok
	fi
fi

setup health-fails
printf stuck >"$ST/health_mode"
run_script
if expect_rc 1 && expect_called "set image deployment/dibs dibs=$OLD_IMAGE"; then
	if [ "$(cat "$ST/live_image")" != "$OLD_IMAGE" ]; then
		fail "not rolled back"
	elif [ "$(cat "$ST/ann_failed" 2>/dev/null)" != "$NEW_IMAGE" ]; then
		fail "pin not marked failed"
	elif ! grep -q 'POST https://api.github.com/repos/hivecommons/dibs/issues {"title":"Auto-rollout failed' "$ST/github"; then
		fail "no issue opened: $(cat "$ST/github")"
	elif grep -q 'test-token' "$S/out"; then
		fail "token leaked into the log"
	else
		ok
	fi
fi

setup rollout-fails-updates-issue
printf 1 >"$ST/rollout_rc"
printf 42 >"$ST/open_issue"
run_script
# The rollback's own rollout status also fails here; it must still annotate
# and comment on the open issue rather than opening another.
if expect_rc 1 && expect_called 'set image'; then
	if ! grep -q 'POST .*/issues/42/comments' "$ST/github"; then
		fail "did not comment on the open issue: $(cat "$ST/github")"
	elif grep -q 'POST https://api.github.com/repos/hivecommons/dibs/issues {' "$ST/github"; then
		fail "opened a second issue"
	else
		ok
	fi
fi

setup apply-error-rolls-back
printf 1 >"$ST/apply_rc"
run_script
expect_rc 1 && expect_called "set image deployment/dibs dibs=$OLD_IMAGE" && ok || true

setup already-failed
printf '%s' "$NEW_IMAGE" >"$ST/ann_failed"
run_script
expect_rc 0 && expect_not_called 'apply -f' && check [ "$(backups)" = 0 ] && ok || true

setup paused
printf true >"$ST/ann_pause"
run_script
expect_rc 0 && expect_not_called 'apply -f' && ok || true

setup scaled-to-zero
printf '7 7 0 0 0 0' >"$ST/steady"
run_script
expect_rc 0 && expect_not_called 'apply -f' && check [ "$(backups)" = 0 ] && ok || true

setup dry-run
DRY=true run_script
expect_rc 0 && expect_called 'apply --dry-run=server' && expect_not_called 'apply -f' && check [ "$(backups)" = 0 ] && ok || true

setup tag-pin-rejected
sed -i.bak -E "s#^([[:space:]]+image:).*#\1 ghcr.io/hivecommons/dibs:latest#" "$ST/fixture-deployment.yaml"
run_script
expect_rc 1 && expect_not_called 'apply' && check grep -q 'immutable' "$S/out" && ok || true

setup missing-source-comment
sed -i.bak -E '/^[[:space:]]*# main [0-9a-f]{40}/d' "$ST/fixture-deployment.yaml"
run_script
expect_rc 1 && expect_not_called 'apply' && ok || true

setup no-index-refuses
rm "$S/data/ideas/index.json"
run_script
expect_rc 1 && expect_not_called 'apply -f' && ok || true

if [ "$FAILS" -ne 0 ]; then
	echo "$FAILS autodeploy test case(s) failed"
	exit 1
fi
echo "all autodeploy cases passed"

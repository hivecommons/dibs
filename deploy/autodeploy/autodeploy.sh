#!/bin/sh
# Dibs pull-based auto-rollout. Runs in-cluster as the dibs-autodeploy
# CronJob (deploy/autodeploy/cronjob.yaml); see deploy/README.md
# "Automatic rollouts". No cluster credential ever leaves the cluster: this
# job reads the public repository and acts with a namespaced ServiceAccount.
#
# One run:
#   1. Resolve the head commit of SOURCE_BRANCH and fetch deploy/deployment.yaml
#      and deploy/ingress.yaml at exactly that commit.
#   2. Validate them (one immutable IMAGE_REPO@sha256 pin naming its source
#      commit; fixed kinds/names/namespace; no ServiceAccount token; ingress
#      routes only to the dibs Service; server-side dry run).
#   3. If the live image already equals the pin, or the Deployment is paused,
#      scaled to zero, mid-rollout, or the pin already failed once: stop.
#   4. Back up DATA_DIR into BACKUP_DIR and verify it; prune old backups.
#   5. Apply both manifests, wait for the rollout, then require /healthz to
#      report the pinned source commit and /readyz to answer 200.
#   6. On any failure after step 5 began: roll the image back to the previous
#      one, mark the pin failed, and open (or update) a GitHub issue.
#
# This file is the source of truth; script-configmap.yaml embeds a copy and a
# test keeps the two identical.
set -eu

# --- configuration (env, with defaults) -----------------------------------
: "${DIBS_NAMESPACE:=dibs}"
: "${DIBS_DEPLOYMENT:=dibs}"
: "${DIBS_CONTAINER:=dibs}"
: "${DIBS_SERVICE:=dibs}"
: "${DIBS_INGRESS:=dibs}"
: "${SOURCE_REPO:=hivecommons/dibs}"
: "${SOURCE_BRANCH:=main}"
: "${IMAGE_REPO:=ghcr.io/hivecommons/dibs}"
: "${HEALTH_URL:=http://dibs.dibs.svc.cluster.local/healthz}"
: "${READY_URL:=http://dibs.dibs.svc.cluster.local/readyz}"
: "${ROLLOUT_TIMEOUT:=300s}"
: "${HEALTH_TIMEOUT_SECONDS:=180}"
: "${HEALTH_INTERVAL_SECONDS:=10}"
: "${HEALTH_REQUIRED_SUCCESSES:=3}"
: "${DATA_DIR:=/data}"
: "${BACKUP_DIR:=/backups}"
# Number of verified pre-rollout backups kept in BACKUP_DIR; older ones are
# deleted after a new backup verifies.
: "${BACKUP_RETENTION_COUNT:=30}"
# Space-separated files the running pod rewrites continuously (derived
# caches); a checksum change between the manifest and the tar is tolerated
# for these only.
: "${BACKUP_VOLATILE_FILES:=repo-news.json}"
: "${ISSUE_LABEL:=auto-rollout}"
# Optional: issue number that gets a comment on every successful rollout.
: "${REPORT_ISSUE:=}"
# true: validate and report the plan, never back up or apply.
: "${DRY_RUN:=false}"
: "${WORK_DIR:=${TMPDIR:-/tmp}}"
: "${GITHUB_API:=https://api.github.com}"
: "${GITHUB_RAW:=https://raw.githubusercontent.com}"
: "${GITHUB_WEB:=https://github.com}"
: "${HTTP_TIMEOUT_SECONDS:=30}"
# GITHUB_TOKEN (optional, from the dibs-autodeploy-github Secret): used for
# the GitHub API and to file issues. Never printed.

ANN_PREFIX=dibs.hivecommons.dev
ANN_PAUSE="$ANN_PREFIX/autodeploy-pause"
ANN_FAILED="$ANN_PREFIX/autodeploy-failed-image"
ANN_PREVIOUS="$ANN_PREFIX/autodeploy-previous-image"
# jsonpath needs the dots of an annotation key escaped.
JP_PREFIX='dibs\.hivecommons\.dev'

SHA_RE='^[0-9a-f]{40}$'

phase=init
live_image=
desired_image=
expected_version=
previous_version=
head_sha=
backup_name=

log() { printf '%s autodeploy: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >&2; }
die() {
	log "ERROR: $*"
	exit 1
}

kc() { kubectl -n "$DIBS_NAMESPACE" "$@"; }

# gh_curl ACCEPT URL [curl args...]: GitHub request, authenticated when a
# token is configured.
gh_curl() {
	accept=$1
	url=$2
	shift 2
	if [ -n "${GITHUB_TOKEN:-}" ]; then
		curl -fsS --max-time "$HTTP_TIMEOUT_SECONDS" -H "Accept: $accept" \
			-H "Authorization: Bearer $GITHUB_TOKEN" \
			-H 'X-GitHub-Api-Version: 2022-11-28' "$@" "$url"
	else
		curl -fsS --max-time "$HTTP_TIMEOUT_SECONDS" -H "Accept: $accept" \
			-H 'X-GitHub-Api-Version: 2022-11-28' "$@" "$url"
	fi
}

# json_string TEXT: TEXT as a JSON string literal (newlines become \n).
json_string() {
	printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g; s/	/\\t/g' |
		awk 'BEGIN { ORS = "" } NR > 1 { print "\\n" } { print }' |
		sed 's/^/"/; s/$/"/'
	printf '\n'
}

# first_number: the first top-level "number" in a GitHub issue list.
first_number() { grep -oE '"number": *[0-9]+' | head -n 1 | grep -oE '[0-9]+$' || true; }

open_issue_number() {
	gh_curl application/vnd.github+json \
		"$GITHUB_API/repos/$SOURCE_REPO/issues?state=open&labels=$ISSUE_LABEL&per_page=1" | first_number
}

# report_failure REASON ROLLBACK_RESULT: open or update the single
# open auto-rollout failure issue. Best effort: never masks the failure.
report_failure() {
	if [ -z "${GITHUB_TOKEN:-}" ]; then
		log "no GITHUB_TOKEN configured; not filing an issue"
		return 0
	fi
	body="Automatic rollout of Dibs on the production cluster failed and was rolled back.

- Target: \`$desired_image\` (main \`$expected_version\`)
- Previous image (rolled back to): \`$live_image\` (version \`$previous_version\`)
- Failure: $1
- Rollback: $2
- Pre-rollout backup: \`$backup_name\` in the backup volume
- Job: \`${POD_NAME:-unknown}\`

The pin is marked failed (\`$ANN_FAILED\`), so later runs skip it until main pins a different digest or an operator removes that annotation. Investigate with runbooks/release-rollback.md, then revert or replace the pin in deploy/deployment.yaml."
	payload="$WORK_DIR/issue.json"
	number=$(open_issue_number) || number=
	if [ -n "$number" ]; then
		printf '{"body":%s}\n' "$(json_string "$body")" >"$payload"
		gh_curl application/vnd.github+json "$GITHUB_API/repos/$SOURCE_REPO/issues/$number/comments" \
			-X POST --data-binary "@$payload" -o /dev/null &&
			log "updated issue #$number" || log "could not comment on issue #$number"
	else
		printf '{"title":%s,"labels":[%s],"body":%s}\n' \
			"$(json_string "Auto-rollout failed: main ${expected_version%"${expected_version#???????}"}")" \
			"$(json_string "$ISSUE_LABEL")" "$(json_string "$body")" >"$payload"
		gh_curl application/vnd.github+json "$GITHUB_API/repos/$SOURCE_REPO/issues" \
			-X POST --data-binary "@$payload" -o /dev/null &&
			log "opened an issue" || log "could not open an issue"
	fi
}

# report_success: note the rollout on REPORT_ISSUE and close a still-open
# failure issue. Best effort.
report_success() {
	[ -n "${GITHUB_TOKEN:-}" ] || return 0
	body="Automatic rollout succeeded: \`$desired_image\` (main \`$expected_version\`) replaced \`$live_image\` (version \`$previous_version\`). Pre-rollout backup: \`$backup_name\`."
	payload="$WORK_DIR/comment.json"
	printf '{"body":%s}\n' "$(json_string "$body")" >"$payload"
	if [ -n "$REPORT_ISSUE" ]; then
		gh_curl application/vnd.github+json "$GITHUB_API/repos/$SOURCE_REPO/issues/$REPORT_ISSUE/comments" \
			-X POST --data-binary "@$payload" -o /dev/null || log "could not comment on issue #$REPORT_ISSUE"
	fi
	number=$(open_issue_number) || number=
	if [ -n "$number" ]; then
		gh_curl application/vnd.github+json "$GITHUB_API/repos/$SOURCE_REPO/issues/$number/comments" \
			-X POST --data-binary "@$payload" -o /dev/null || true
		printf '{"state":"closed","state_reason":"completed"}\n' >"$payload"
		gh_curl application/vnd.github+json "$GITHUB_API/repos/$SOURCE_REPO/issues/$number" \
			-X PATCH --data-binary "@$payload" -o /dev/null || log "could not close issue #$number"
	fi
}

# resolve_head: the commit SOURCE_BRANCH points at. Git's smart-HTTP ref
# advertisement first (no API quota), then the REST API.
resolve_head() {
	sha=$(curl -fsS --max-time "$HTTP_TIMEOUT_SECONDS" \
		"$GITHUB_WEB/$SOURCE_REPO.git/info/refs?service=git-upload-pack" 2>/dev/null |
		grep -aoE "[0-9a-f]{40} refs/heads/$SOURCE_BRANCH\$" | head -n 1 | cut -d' ' -f1) || sha=
	if [ -z "$sha" ]; then
		sha=$(gh_curl application/vnd.github.sha "$GITHUB_API/repos/$SOURCE_REPO/commits/$SOURCE_BRANCH") || return 1
	fi
	printf '%s\n' "$sha"
}

live_version() {
	curl -fsS --max-time 5 "$HEALTH_URL" 2>/dev/null |
		sed -nE 's/.*"version" *: *"([^"]*)".*/\1/p'
}

ready_code() {
	curl -sS -o /dev/null -w '%{http_code}' --max-time 5 "$READY_URL" 2>/dev/null || true
}

# wait_healthy VERSION: VERSION on /healthz and 200 on /readyz,
# HEALTH_REQUIRED_SUCCESSES times in a row, within HEALTH_TIMEOUT_SECONDS.
wait_healthy() {
	want=$1
	ok=0
	waited=0
	while [ "$waited" -le "$HEALTH_TIMEOUT_SECONDS" ]; do
		got=$(live_version) || got=
		code=$(ready_code)
		if [ "$got" = "$want" ] && [ "$code" = 200 ]; then
			ok=$((ok + 1))
			[ "$ok" -ge "$HEALTH_REQUIRED_SUCCESSES" ] && return 0
		else
			ok=0
			log "waiting for health: version=${got:-none} readyz=${code:-none}"
		fi
		sleep "$HEALTH_INTERVAL_SECONDS"
		waited=$((waited + HEALTH_INTERVAL_SECONDS))
	done
	return 1
}

rollback() {
	reason=$1
	phase=rolling-back
	log "rolling back to $live_image: $reason"
	result="rolled back to the previous image; health verified"
	if ! kc set image "deployment/$DIBS_DEPLOYMENT" "$DIBS_CONTAINER=$live_image"; then
		result="set image FAILED; manual rollback needed"
	elif ! kc rollout status "deployment/$DIBS_DEPLOYMENT" --timeout="$ROLLOUT_TIMEOUT"; then
		result="previous image did not finish rolling out; manual action needed"
	elif [ -n "$previous_version" ] && ! wait_healthy "$previous_version"; then
		result="previous image rolled out but health did not report $previous_version; manual action needed"
	fi
	kc annotate "deployment/$DIBS_DEPLOYMENT" --overwrite "$ANN_FAILED=$desired_image" >/dev/null ||
		log "could not mark $desired_image failed"
	log "rollback: $result"
	report_failure "$reason" "$result"
	phase=done
	exit 1
}

on_exit() {
	code=$?
	if [ "$code" -ne 0 ] && [ "$phase" = applying ]; then
		rollback "unexpected error (exit $code) during the rollout"
	fi
	rm -f "$WORK_DIR/issue.json" "$WORK_DIR/comment.json"
}
trap on_exit EXIT

is_volatile() {
	for v in $BACKUP_VOLATILE_FILES; do
		[ "$1" = "$v" ] && return 0
	done
	return 1
}

# backup: tar every file under DATA_DIR into BACKUP_DIR with a checksum
# manifest, verify the tar against it, then prune to BACKUP_RETENTION_COUNT.
backup() {
	short=$(printf '%.7s' "${previous_version:-unknown}")
	backup_name="dibs-data-$(date -u +%Y%m%dT%H%M%SZ)-$short.tar"
	tarfile="$BACKUP_DIR/$backup_name"
	sums="${tarfile%.tar}.sha256"
	list="$WORK_DIR/backup-files"
	partial="$BACKUP_DIR/.partial-$backup_name"
	# lost+found is the file system's, root-owned, and not Dibs data.
	(cd "$DATA_DIR" && find . -path ./lost+found -prune -o -type f -print | sed 's#^\./##' | sort) >"$list"
	grep -qx 'ideas/index.json' "$list" || die "backup: $DATA_DIR has no ideas/index.json; refusing to roll out"
	(cd "$DATA_DIR" && while IFS= read -r f; do sha256sum "$f"; done <"$list") >"$partial.sha256"
	tar -C "$DATA_DIR" -cf "$partial" -T "$list"
	members=$(tar -tf "$partial" | wc -l)
	files=$(wc -l <"$list")
	[ "$members" -eq "$files" ] || die "backup: tar has $members members, expected $files"
	while IFS= read -r line; do
		sum=${line%% *}
		path=${line#*  }
		got=$(tar -xOf "$partial" "$path" | sha256sum | cut -d' ' -f1)
		if [ "$got" != "$sum" ]; then
			is_volatile "$path" || die "backup: checksum mismatch for $path"
			log "backup: $path changed while copying (volatile cache, tolerated)"
		fi
	done <"$partial.sha256"
	mv "$partial.sha256" "$sums"
	mv "$partial" "$tarfile"
	log "backup verified: $backup_name ($files files)"
	# Prune oldest beyond retention (names sort by timestamp).
	count=$(find "$BACKUP_DIR" -maxdepth 1 -name 'dibs-data-*.tar' | wc -l)
	excess=$((count - BACKUP_RETENTION_COUNT))
	if [ "$excess" -gt 0 ]; then
		find "$BACKUP_DIR" -maxdepth 1 -name 'dibs-data-*.tar' | sort | head -n "$excess" |
			while IFS= read -r old; do
				rm -f "$old" "${old%.tar}.sha256"
				log "backup: pruned $(basename "$old")"
			done
	fi
}

main() {
	mkdir -p "$WORK_DIR"
	rm -f "$BACKUP_DIR"/.partial-*
	head_sha=$(resolve_head) || die "cannot resolve $SOURCE_REPO@$SOURCE_BRANCH"
	printf '%s\n' "$head_sha" | grep -Eq "$SHA_RE" || die "unexpected commit id for $SOURCE_BRANCH"
	for f in deployment ingress; do
		curl -fsS --max-time "$HTTP_TIMEOUT_SECONDS" -o "$WORK_DIR/$f.yaml" \
			"$GITHUB_RAW/$SOURCE_REPO/$head_sha/deploy/$f.yaml" || die "cannot fetch deploy/$f.yaml at $head_sha"
	done

	# Exactly one image, an immutable digest of IMAGE_REPO, with its source
	# commit on the comment line directly above it.
	images=$(sed -nE 's/^[[:space:]]+image:[[:space:]]*([^[:space:]]+)[[:space:]]*$/\1/p' "$WORK_DIR/deployment.yaml")
	[ "$(printf '%s\n' "$images" | grep -c .)" -eq 1 ] || die "deployment.yaml must have exactly one image"
	desired_image=$images
	repo_re=$(printf '%s' "$IMAGE_REPO" | sed 's/[.]/\\./g')
	printf '%s\n' "$desired_image" | grep -Eq "^$repo_re@sha256:[0-9a-f]{64}$" ||
		die "pin is not an immutable $IMAGE_REPO@sha256 digest: $desired_image"
	expected_version=$(grep -B 1 -E '^[[:space:]]+image:' "$WORK_DIR/deployment.yaml" |
		sed -nE 's/^[[:space:]]*# main ([0-9a-f]{40}).*/\1/p')
	printf '%s\n' "$expected_version" | grep -Eq "$SHA_RE" ||
		die "the image line must be preceded by '# main <full source sha>'"

	shape=$(kubectl create --dry-run=client -f "$WORK_DIR/deployment.yaml" -o \
		jsonpath='{.kind}|{.metadata.namespace}|{.metadata.name}|{.spec.template.spec.serviceAccountName}|{.spec.template.spec.automountServiceAccountToken}')
	[ "$shape" = "Deployment|$DIBS_NAMESPACE|$DIBS_DEPLOYMENT||false" ] ||
		die "deployment.yaml has an unexpected shape: $shape"
	shape=$(kubectl create --dry-run=client -f "$WORK_DIR/ingress.yaml" -o \
		jsonpath='{.kind}|{.metadata.namespace}|{.metadata.name}')
	[ "$shape" = "Ingress|$DIBS_NAMESPACE|$DIBS_INGRESS" ] || die "ingress.yaml has an unexpected shape: $shape"
	for svc in $(kubectl create --dry-run=client -f "$WORK_DIR/ingress.yaml" -o \
		jsonpath='{.spec.rules[*].http.paths[*].backend.service.name}'); do
		[ "$svc" = "$DIBS_SERVICE" ] || die "ingress.yaml routes to an unexpected service: $svc"
	done

	live_image=$(kc get "deployment/$DIBS_DEPLOYMENT" -o \
		jsonpath="{.spec.template.spec.containers[?(@.name==\"$DIBS_CONTAINER\")].image}")
	[ -n "$live_image" ] || die "cannot read the live image"
	if [ "$live_image" = "$desired_image" ]; then
		log "in sync: $desired_image (main $head_sha)"
		return 0
	fi
	paused=$(kc get "deployment/$DIBS_DEPLOYMENT" -o jsonpath="{.metadata.annotations.$JP_PREFIX/autodeploy-pause}")
	if [ "$paused" = true ]; then
		log "paused ($ANN_PAUSE=true); not rolling out $desired_image"
		return 0
	fi
	failed=$(kc get "deployment/$DIBS_DEPLOYMENT" -o jsonpath="{.metadata.annotations.$JP_PREFIX/autodeploy-failed-image}")
	if [ "$failed" = "$desired_image" ]; then
		log "$desired_image already failed and was rolled back; remove $ANN_FAILED to retry"
		return 0
	fi
	state=$(kc get "deployment/$DIBS_DEPLOYMENT" -o \
		jsonpath='{.metadata.generation} {.status.observedGeneration} {.spec.replicas} {.status.updatedReplicas} {.status.availableReplicas} {.status.replicas}')
	# shellcheck disable=SC2086 # deliberate word splitting of the jsonpath fields
	set -- $state
	if [ "$#" -ne 6 ] || [ "$1" != "$2" ] || [ "$3" = 0 ] || [ "$3" != "$4" ] || [ "$3" != "$5" ] || [ "$3" != "$6" ]; then
		log "deployment not steady (generation/observed/spec/updated/available/replicas: $state); skipping"
		return 0
	fi
	previous_version=$(live_version) || previous_version=
	log "rollout needed: $live_image (version ${previous_version:-unknown}) -> $desired_image (main $expected_version)"
	kc apply --dry-run=server -f "$WORK_DIR/ingress.yaml" -f "$WORK_DIR/deployment.yaml" >/dev/null ||
		die "server-side dry run rejected the manifests"
	if [ "$DRY_RUN" = true ]; then
		log "DRY_RUN: would back up, apply and verify"
		return 0
	fi

	backup

	phase=applying
	kc annotate "deployment/$DIBS_DEPLOYMENT" --overwrite "$ANN_PREVIOUS=$live_image" >/dev/null
	kc apply -f "$WORK_DIR/ingress.yaml" -f "$WORK_DIR/deployment.yaml"
	if ! kc rollout status "deployment/$DIBS_DEPLOYMENT" --timeout="$ROLLOUT_TIMEOUT"; then
		rollback "rollout did not complete within $ROLLOUT_TIMEOUT"
	fi
	if ! wait_healthy "$expected_version"; then
		rollback "/healthz did not report $expected_version with /readyz 200 within ${HEALTH_TIMEOUT_SECONDS}s"
	fi
	phase=done
	log "rolled out $desired_image (main $expected_version)"
	report_success
}

if [ "${AUTODEPLOY_SOURCE_ONLY:-}" != 1 ]; then
	main "$@"
fi

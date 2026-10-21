#!/usr/bin/env bash
# Container smoke test for a built dibs image.
#
# `go test` proves the packages; `docker build` proves the image compiles.
# Neither proves the image *starts* under the constraints deploy/deployment.yaml
# imposes (nonroot 65532, read-only root filesystem, only /data writable, all
# capabilities dropped) — a regression there only surfaces at `kubectl apply`.
# This script runs the image the way the Deployment does and checks the
# contract the manifests and runbooks rely on:
#
#   * `dibs --version` prints the ldflags-stamped commit (freshness probe)
#   * /healthz answers 200 with that version (livenessProbe)
#   * /readyz answers 200, i.e. /data is writable as 65532 (readinessProbe)
#   * GET / serves the landing page with HUB_URL substituted for the sign-in
#     links and the browser hardening headers set (Ingress-facing contract)
#   * the auth wall holds: public /api/credits answers 200, /api/me is 401
#     without a hub cookie and 502 when the hub cannot be reached
#   * DIBS_METRICS_ADDR exposes dibs_* metrics (ServiceMonitor scrape),
#     with the request counter labelled by the route groups just exercised
#   * SIGTERM drains and exits 0 inside terminationGracePeriodSeconds
#
# Usage: hack/smoke.sh IMAGE [EXPECTED_GIT_HASH]
#   IMAGE              image reference (must be loadable by the local docker)
#   EXPECTED_GIT_HASH  when given, `--version` must print exactly this hash;
#                      otherwise any 40-hex hash other than "unknown" passes.
#
# Environment:
#   DOCKER             container CLI to use (default: docker; podman works)
#   SMOKE_TIMEOUT      seconds to wait for /healthz (default: 30)
set -euo pipefail

IMAGE="${1:-}"
EXPECTED_HASH="${2:-}"
DOCKER="${DOCKER:-docker}"
SMOKE_TIMEOUT="${SMOKE_TIMEOUT:-30}"

if [ -z "$IMAGE" ]; then
  echo "usage: $0 IMAGE [EXPECTED_GIT_HASH]" >&2
  exit 2
fi

# Mirrors deploy/deployment.yaml: runAsUser/runAsGroup 65532,
# readOnlyRootFilesystem, drop ALL, no privilege escalation, /data mounted.
# HUB_URL points at a closed port so registry sync fails fast and the test
# does not depend on the network: the server must still come up (the
# registry keeps serving its last-known state).
HUB_URL="http://127.0.0.1:1"
RUN_ARGS=(
  --user 65532:65532
  --read-only
  --cap-drop ALL
  --security-opt no-new-privileges
  --tmpfs "/data:rw,mode=1777"
  -e DIBS_ADDR=:8080
  -e DIBS_BASE_PATH=/
  -e DATA_DIR=/data
  -e DIBS_METRICS_ADDR=:9090
  -e "HUB_URL=${HUB_URL}"
  -p 127.0.0.1::8080
  -p 127.0.0.1::9090
)

CTR=""
cleanup() {
  if [ -n "$CTR" ]; then
    "$DOCKER" rm -f "$CTR" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

fail() {
  echo "smoke: FAIL: $*" >&2
  if [ -n "$CTR" ]; then
    echo "--- container logs ---" >&2
    "$DOCKER" logs "$CTR" >&2 2>&1 || true
    echo "----------------------" >&2
  fi
  exit 1
}

step() { echo "smoke: $*"; }

# 1. --version: the ldflags stamp the Dockerfile applies must survive.
step "checking --version"
version_out="$("$DOCKER" run --rm --user 65532:65532 --read-only --cap-drop ALL "$IMAGE" --version)" \
  || fail "--version exited non-zero"
echo "  $version_out"
if ! [[ "$version_out" =~ ^dibs\ ([0-9a-f]{7}|unknown)\ \((.+)\)$ ]]; then
  fail "--version output does not match 'dibs <short> (<hash>)': $version_out"
fi
full_hash="${BASH_REMATCH[2]}"
if [ -n "$EXPECTED_HASH" ]; then
  [ "$full_hash" = "$EXPECTED_HASH" ] || fail "--version hash $full_hash != expected $EXPECTED_HASH"
else
  [[ "$full_hash" =~ ^[0-9a-f]{40}$ ]] || fail "--version hash is not stamped: $full_hash"
fi

# 2. Start under the Deployment's constraints.
step "starting container"
CTR="$("$DOCKER" run -d "${RUN_ARGS[@]}" "$IMAGE")" || fail "docker run failed"
http_port="$("$DOCKER" port "$CTR" 8080/tcp | head -n1 | sed 's/.*://')"
metrics_port="$("$DOCKER" port "$CTR" 9090/tcp | head -n1 | sed 's/.*://')"
[ -n "$http_port" ] && [ -n "$metrics_port" ] || fail "could not resolve published ports"
base="http://127.0.0.1:${http_port}"

# 3. /healthz (livenessProbe) within SMOKE_TIMEOUT.
step "waiting for /healthz on $base"
deadline=$((SECONDS + SMOKE_TIMEOUT))
healthz=""
while [ "$SECONDS" -lt "$deadline" ]; do
  if healthz="$(curl -fsS --max-time 2 "$base/healthz" 2>/dev/null)"; then
    break
  fi
  healthz=""
  if [ "$("$DOCKER" inspect -f '{{.State.Running}}' "$CTR" 2>/dev/null)" != "true" ]; then
    fail "container exited before /healthz answered"
  fi
  sleep 1
done
[ -n "$healthz" ] || fail "/healthz did not answer 200 within ${SMOKE_TIMEOUT}s"
echo "  $healthz"
[[ "$healthz" == *'"status":"ok"'* ]] || fail "/healthz body lacks status ok"
[[ "$healthz" == *"\"version\":\"${full_hash}\""* ]] || fail "/healthz version does not match --version ($full_hash)"

# 4. /readyz (readinessProbe): store.Ping must find /data writable as 65532.
step "checking /readyz"
readyz_tmp="$(mktemp)"
readyz_code="$(curl -sS -o "$readyz_tmp" -w '%{http_code}' --max-time 5 "$base/readyz")" || fail "/readyz request failed"
readyz_body="$(cat "$readyz_tmp")"
rm -f "$readyz_tmp"
echo "  $readyz_code $readyz_body"
[ "$readyz_code" = "200" ] || fail "/readyz returned $readyz_code (is /data writable for uid 65532?)"

# get_with_headers performs a GET and leaves the status code in `code`, the
# response headers in `headers` and the body in `body`.
get_with_headers() {
  local url="$1"; shift
  local hdr_tmp body_tmp
  hdr_tmp="$(mktemp)"; body_tmp="$(mktemp)"
  code="$(curl -sS -o "$body_tmp" -D "$hdr_tmp" -w '%{http_code}' --max-time 5 "$@" "$url")" \
    || { rm -f "$hdr_tmp" "$body_tmp"; fail "GET $url failed"; }
  headers="$(tr -d '\r' <"$hdr_tmp")"
  body="$(cat "$body_tmp")"
  rm -f "$hdr_tmp" "$body_tmp"
}

# 4b. GET /: the landing page is public (credit wall for logged-out
#     visitors). server.New substitutes HUB_URL into the embedded HTML at
#     startup, so every sign-in link must point at the configured hub and
#     no placeholder may leak; securityHeaders wraps every route.
step "checking landing page"
get_with_headers "$base/"
echo "  $code $(wc -c <<<"$body") bytes"
[ "$code" = "200" ] || fail "GET / returned $code"
grep -qi '^content-type: text/html' <<<"$headers" || fail "GET / is not text/html"
grep -qi '^x-frame-options: DENY' <<<"$headers" || fail "GET / lacks X-Frame-Options: DENY"
grep -qi '^x-content-type-options: nosniff' <<<"$headers" || fail "GET / lacks X-Content-Type-Options: nosniff"
grep -q "href=\"${HUB_URL}\"" <<<"$body" || fail "GET / has no sign-in link to HUB_URL ($HUB_URL)"
grep -q '__HUB_URL__' <<<"$body" && fail "GET / still contains the __HUB_URL__ placeholder"

# 4c. Auth wall: the public read-only API answers without a session; the
#     authenticated API rejects a missing cookie with 401 and, when the hub
#     cannot be reached (HUB_URL is a closed port), reports 502 rather than
#     letting the request through or hanging.
step "checking public API and auth wall"
get_with_headers "$base/api/credits"
echo "  /api/credits $code"
[ "$code" = "200" ] || fail "/api/credits (public) returned $code"
grep -qi '^content-type: application/json' <<<"$headers" || fail "/api/credits is not application/json"
get_with_headers "$base/api/me"
echo "  /api/me (no cookie) $code $body"
[ "$code" = "401" ] || fail "/api/me without a session returned $code (expected 401)"
[[ "$body" == *'"error":"not authenticated"'* ]] || fail "/api/me 401 body lacks the JSON error"
get_with_headers "$base/api/me" -H 'Cookie: hive_hub_user=smoke'
echo "  /api/me (hub down) $code $body"
[ "$code" = "502" ] || fail "/api/me with an unverifiable session returned $code (expected 502)"
[[ "$body" == *'"error":"auth backend unavailable"'* ]] || fail "/api/me 502 body lacks the JSON error"

# 5. /metrics on DIBS_METRICS_ADDR: the names deploy/monitoring alerts on,
#    and the bounded request-counter labels for the routes exercised above
#    (route_group + status_class only — never raw paths).
step "checking /metrics on :$metrics_port"
metrics="$(curl -fsS --max-time 5 "http://127.0.0.1:${metrics_port}/metrics")" || fail "/metrics request failed"
grep -q '^# TYPE dibs_http_requests_total counter' <<<"$metrics" || fail "/metrics lacks dibs_http_requests_total"
grep -q '^# TYPE dibs_http_request_duration_seconds histogram' <<<"$metrics" || fail "/metrics lacks dibs_http_request_duration_seconds"
for series in \
  'route_group="healthz",status_class="2xx"' \
  'route_group="readyz",status_class="2xx"' \
  'route_group="ui",status_class="2xx"' \
  'route_group="api/credits",status_class="2xx"' \
  'route_group="api/me",status_class="4xx"' \
  'route_group="api/me",status_class="5xx"'; do
  grep -Eq "^dibs_http_requests_total\{method=\"GET\",${series}\} [1-9][0-9]*\$" <<<"$metrics" \
    || fail "/metrics lacks a non-zero dibs_http_requests_total{method=\"GET\",${series}} series"
done

# 6. SIGTERM drain: serveUntil must shut down cleanly (exit 0) well inside
#    terminationGracePeriodSeconds (30s); 137 means the kubelet would SIGKILL.
step "sending SIGTERM"
"$DOCKER" kill -s TERM "$CTR" >/dev/null || fail "docker kill -s TERM failed"
exit_code="$(timeout 25 "$DOCKER" wait "$CTR")" || fail "container did not exit within 25s of SIGTERM"
echo "  exit code $exit_code"
[ "$exit_code" = "0" ] || fail "container exited $exit_code after SIGTERM (expected 0)"

echo "smoke: OK ($IMAGE, $full_hash)"

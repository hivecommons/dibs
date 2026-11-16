# Deploying Dibs at dibs.hivecommons.dev

Plain Kubernetes manifests — no helm. For the existing production service,
complete the backup and previous-image gates in [rollout #558](rollout-558.md)
**before** applying the pinned Deployment below. Merging a pin is not a rollout.
The weekly reviewed update cadence is tracked separately in
[#567](https://github.com/hivecommons/dibs/issues/567).

For a new installation, apply in order:

```sh
kubectl apply -f deploy/namespace.yaml
kubectl apply -f deploy/pvc.yaml
kubectl apply -f deploy/configmap.yaml
kubectl apply -f deploy/secret.yaml      # fill in real values first (see below)
kubectl apply -f deploy/deployment.yaml
kubectl apply -f deploy/service.yaml
kubectl apply -f deploy/ingress.yaml
kubectl apply -f deploy/legacy-redirect.yaml  # only where dibs.kubestellar.io redirects via the hub
```

`deploy/backup-pod.yaml` is the manual backup helper from
[data recovery](../runbooks/data-recovery.md), not part of the apply list.

## Prerequisites

1. **DNS**: a `dibs.hivecommons.dev` A/CNAME record pointing at the cluster's
   ingress load balancer (same target as `hive.hivecommons.dev`). The legacy
   `dibs.kubestellar.io` host is now a permanent redirect to
   `dibs.hivecommons.dev` (hivecommons/hive#5925, done).
2. **cert-manager** with a `letsencrypt` ClusterIssuer (the ingress requests
   the TLS cert via the `cert-manager.io/cluster-issuer` annotation; adjust
   the issuer name in `ingress.yaml` if yours differs).
3. **Image**: `ghcr.io/hivecommons/dibs` is published automatically by the
   `docker.yml` workflow on every push to `main` whose CI run is green (tags
   `latest` + commit sha). Production uses the reviewed immutable image-index
   digest in `deployment.yaml`; record its source sha and validation evidence
   when updating the pin. Never deploy `latest` to production.
4. **Hub session sharing**: Dibs authenticates by validating the hub's
   `hive_hub_user` session cookie, so Dibs and the hub must share a
   registrable domain — the hub scopes the cookie to `.hivecommons.dev`, and
   a browser will not send it to a host outside that scope. This is why Dibs
   is served at `dibs.hivecommons.dev` and why the legacy host is a redirect
   rather than serving the app (hivecommons/hive#5925).

## Weekly production image review

Until [automatic rollouts](#automatic-rollouts) are enabled, production
image updates are a reviewed, operator-executed handoff. Once enabled, the
review below happens on the bot pin-bump PR instead, and merging it is the
deployment decision.

- **Cadence:** review every Monday (UTC). Important fixes may be reviewed
  out of cycle.
- **Owner:** @clubanderson is the release-review coordinator and operator
  contact.
- **Deferral:** a review may defer deployment with a reason and the next
  review date. A deadline is not permission to deploy.
- **Where to record:** post each week's record on tracking issue
  [#567](https://github.com/hivecommons/dibs/issues/567), which stays open
  for the recurring obligation. Do not mark a field complete unless it has
  been verified; operator-only fields (running version, previous image,
  backup, rollout result) are confirmed by the operator.
- **Related work:** the one-time stale-production rollout is tracked in
  [#558](https://github.com/hivecommons/dibs/issues/558).

### Candidate gates

1. Use an exact commit on `main` that is green for CI, for Go Security (a
   separate workflow, check it separately) and for Docker publication.
2. Resolve the published multi-architecture digest (`linux/amd64` and
   `linux/arm64`) and use the immutable
   `ghcr.io/hivecommons/dibs@sha256:...` reference as the production pin,
   recording its source commit. Never pin `:latest` or another floating tag.
3. Review the actual persistence changes between the running version and the
   candidate (not just commit titles) and assess rollback compatibility; see
   [data recovery](../runbooks/data-recovery.md).
4. Keep the previous deployed image/digest and a verified data backup before
   the operator proceeds, per
   [release rollback](../runbooks/release-rollback.md) and
   [data recovery](../runbooks/data-recovery.md).
5. Pin changes to `deploy/deployment.yaml` go through a normal reviewed PR
   with OWNERS review and CI; no automatic merges.

### Review / handoff record

```text
Review date (UTC) / coordinator:
Running version (/healthz) and previous image/digest:   (operator-confirmed)
Candidate commit:
Immutable digest (amd64 + arm64): ghcr.io/hivecommons/dibs@sha256:...
Evidence: CI / Go Security / Docker publication (links):
Persistence changes vs running version and rollback assessment:
Backup requirement / verified backup:                   (operator-confirmed)
Disposition: deploy | defer (reason, next review date):
Operator rollout result: /healthz expected version, /readyz, or rollback:
```

## Automatic rollouts

Every change flows: merge to `main` -> CI -> `docker.yml` publishes the image
-> `pin-bump.yml` opens or updates **one** bot PR (`bot/pin-bump`) repinning
`deployment.yaml` to that image's digest -> review + CI -> merge -> the
in-cluster `dibs-autodeploy` CronJob rolls it out.

**Pull-based, in-cluster.** No cluster credential lives in GitHub. The
CronJob (`deploy/autodeploy/`) reads the public repository and acts through
the `dibs-autodeploy` ServiceAccount, whose Role can only get/patch (and
watch, by name) the `dibs` Deployment and Ingress in this namespace. It
triggers on the **pinned digest on `main`**, not on every published image, so
the reviewed pin PR stays the release gate (a GitHub Actions deploy job with
OIDC would also need the cluster API reachable from GitHub and a
cluster-side trust for GitHub's issuer; this avoids both).

Each run (every 10 minutes, `concurrencyPolicy: Forbid`):

1. Resolves `main`, fetches `deploy/deployment.yaml` and `deploy/ingress.yaml`
   at that exact commit and validates them: one `ghcr.io/hivecommons/dibs@sha256:`
   pin with a `# main <full sha>` line above it, fixed kinds, names and
   namespace, no ServiceAccount or token on the app pod, ingress backends only
   `dibs`, and a server-side dry run.
2. Does nothing if the live image already matches, the Deployment is annotated
   `dibs.hivecommons.dev/autodeploy-pause=true`, it is scaled to zero or
   mid-rollout, or this digest already failed
   (`dibs.hivecommons.dev/autodeploy-failed-image`).
3. Backs up `/data` (read-only mount) to the `dibs-backups` volume, verifies
   every file's checksum against the tar, and keeps the newest
   `BACKUP_RETENTION_COUNT` (default 30) backups. A failed backup stops the run.
4. Records the previous image (`dibs.hivecommons.dev/autodeploy-previous-image`),
   applies both manifests, waits for `rollout status`, then requires `/healthz`
   to report the pinned sha and `/readyz` to return 200, three times in a row.
5. On failure, sets the image back to the previous one, verifies it,
   marks the digest failed, and opens or updates a single open issue
   labelled `auto-rollout` (needs the optional token below). On success it
   comments on [#567](https://github.com/hivecommons/dibs/issues/567) and
   closes any open `auto-rollout` issue.

Only `deployment.yaml` and `ingress.yaml` are applied. Changes to the
ConfigMap, Service, PVCs or Secrets are applied by hand before the pin PR
merges (the bot PR flags `deploy/` changes). Pin PRs also flag changes in
the persistence packages for the review in
[candidate gates](#candidate-gates).

Settings (CronJob env, all with defaults in `autodeploy.sh`):
`BACKUP_RETENTION_COUNT` (30), `REPORT_ISSUE` (567), `DRY_RUN` (false),
`ROLLOUT_TIMEOUT` (300s), `HEALTH_TIMEOUT_SECONDS` (180),
`HEALTH_INTERVAL_SECONDS` (10), `HEALTH_REQUIRED_SUCCESSES` (3),
`BACKUP_VOLATILE_FILES` (`repo-news.json`, a cache the app rewrites often,
whose checksum may change during the copy).

### Enable (after the first manual rollout is live)

The CronJob ships `suspend: true`. With the production context selected:

```sh
kubectl apply -f deploy/autodeploy/rbac.yaml -f deploy/autodeploy/pvc.yaml
kubectl -n dibs create configmap dibs-autodeploy-script \
  --from-file=autodeploy.sh=deploy/autodeploy/autodeploy.sh \
  --dry-run=client -o yaml | kubectl apply -f -
# Optional, for failure issues: a fine-grained token limited to
# hivecommons/dibs with Issues read/write only.
kubectl -n dibs create secret generic dibs-autodeploy-github --from-literal=GITHUB_TOKEN=<token>
kubectl apply -f deploy/autodeploy/cronjob.yaml          # still suspended

# One supervised run: the cluster is already at the pin, so it must log "in sync".
kubectl -n dibs create job --from=cronjob/dibs-autodeploy dibs-autodeploy-check
kubectl -n dibs wait --for=condition=complete job/dibs-autodeploy-check --timeout=300s
kubectl -n dibs logs job/dibs-autodeploy-check
kubectl -n dibs delete job dibs-autodeploy-check

kubectl -n dibs patch cronjob dibs-autodeploy -p '{"spec":{"suspend":false}}'
```

Re-run the `create configmap ... | kubectl apply` line whenever
`autodeploy.sh` changes on `main`. The job never updates itself.

### Pause, retry, disable

```sh
kubectl -n dibs annotate deploy/dibs dibs.hivecommons.dev/autodeploy-pause=true --overwrite   # pause
kubectl -n dibs annotate deploy/dibs dibs.hivecommons.dev/autodeploy-pause-                   # resume
kubectl -n dibs annotate deploy/dibs dibs.hivecommons.dev/autodeploy-failed-image-            # retry a failed pin
kubectl -n dibs patch cronjob dibs-autodeploy -p '{"spec":{"suspend":true}}'                  # disable
```

After an automatic rollback, `main` still pins the failed digest. Revert or
replace the pin in a PR. Until then the job skips that digest, but a manual
`kubectl apply -f deploy/deployment.yaml` would roll it out again.

## Environment variables

| Variable | Source | Default | Purpose |
| --- | --- | --- | --- |
| `DIBS_ADDR` | ConfigMap | `:8080` | Listen address |
| `DIBS_BASE_PATH` | ConfigMap | `/` | URL prefix (root on the subdomain) |
| `HUB_URL` | ConfigMap | `https://hive.hivecommons.dev` | Hub origin for auth + registry sync |
| `DIBS_METRICS_ADDR` | ConfigMap | unset (off); `:9090` in `configmap.yaml` | Internal Prometheus `/metrics` listener |
| `DATA_DIR` | ConfigMap | `/data` | JSON store root (backed by the PVC) |
| `DIBS_LLM_BASE_URL` | Secret (optional) | unset | litellm gateway; unset ⇒ deterministic fallback matcher |
| `DIBS_LLM_API_KEY` | Secret (optional) | unset | Gateway API key |
| `DIBS_LLM_MODEL` | Secret (optional) | gateway default | Model name |
| `DIBS_GITHUB_TOKEN` | Secret (optional) | unset | **Legacy mode only**: when set, Dibs opens the credited issue server-side on accept. Unset (default), the ideator files a prefilled GitHub issue themselves. Also authenticates the GitHub lookup that verifies a confirmed issue URL (anonymous otherwise) |
| `DIBS_ADMINS` | Secret (optional) | unset | Comma-separated, case-insensitive GitHub logins granted admin access (idea-moderation endpoints) |
| `DIBS_CLANKER_MARKERS` | ConfigMap (optional) | `hive: agent=,hivecommons-hive[bot],kubestellar-hive[bot]` | Comma-separated, case-insensitive markers matched against PR branch/labels/body/author to classify agent activity |
| `DIBS_INTAKE_MAX_MB` | ConfigMap (optional) | `25` | Max upload size (MB) accepted for idea-intake file uploads. `ingress.yaml` pins `nginx.ingress.kubernetes.io/proxy-body-size: "30m"` to stay above this cap; raise both together or ingress-nginx returns 413 first |
| `DIBS_INTAKE_EXTRACT_WORKER` | ConfigMap (optional) | _(on)_ | Set to `off` to extract uploaded documents in-process instead of in a memory-limited, time-limited child worker (not recommended) |
| `DIBS_STT_URL` | Secret (optional) | unset | Speech-to-text gateway; unset ⇒ voice/audio idea submission disabled |
| `DIBS_STT_KEY` | Secret (optional) | unset | API key for the STT gateway |
| `DIBS_STT_MODEL` | Secret (optional) | unset | Model name routed by the STT gateway |
| `REPOS_SEED_FILE` | — (optional) | unset | Static repo-registry seed for dev/demo |

The legacy `IDEATE_*` names (the product's pre-rename prefix) are still
honored as fallbacks for every `DIBS_*` variable.

Create the secret with real values instead of applying the placeholder:

```sh
kubectl -n dibs create secret generic dibs-secrets \
  --from-literal=DIBS_LLM_BASE_URL=https://... \
  --from-literal=DIBS_LLM_API_KEY=sk-... \
  --from-literal=DIBS_GITHUB_TOKEN=ghp_...   # optional; legacy server-side issue creation
```

## Notes

- The container runs as non-root (distroless `nonroot`, uid 65532) with a
  read-only root filesystem; only `/data` (the PVC) and `/tmp` (an
  `emptyDir`, for in-progress voice recordings) are writable.
- Single replica: the JSON file store is single-writer by design. The PVC is
  `ReadWriteMany` (OCI FSS) so `RollingUpdate` (maxSurge=1, maxUnavailable=0)
  can overlap the old and new pods — rollouts are zero-downtime.
- Health/readiness: `GET /healthz` is a liveness-only check (process is up)
  returning `{"status":"ok","version":"<git sha>"}`. `GET /readyz` additionally
  verifies the `/data` store directory is accessible and writable, returning
  503 if not — this is what the readiness probe uses, so a pod is never
  routed traffic before (or after) its store dependency is usable.

## Metrics and alerts (optional)

Dibs serves cumulative, bounded counters at `/metrics` (Prometheus text
format) on a separate internal listener, `DIBS_METRICS_ADDR` (`:9090`). The
Service exposes it as port `metrics`; the ingress only routes `http`, so the
endpoint is not reachable from the public host. Metrics:
`dibs_http_requests_total{method,route_group,status_class}`,
`dibs_http_request_duration_seconds{method,route_group,status_class}` (histogram, fixed buckets),
`dibs_background_job_runs_total{job,result}`,
`dibs_background_job_last_success_timestamp_seconds{job}` and
`dibs_match_llm_calls_total{op,outcome}`.

`deploy/monitoring/` holds a `ServiceMonitor` and a `PrometheusRule`. They
need the Prometheus Operator CRDs and are deliberately not part of the apply
list above:

```sh
kubectl apply -f deploy/monitoring/
```

The ServiceMonitor endpoint sets `honorLabels: true` to preserve the `job`
label from `dibs_background_job_*` metrics, so background-job alerts and
dashboard panels retain job identity (without it, Prometheus would rewrite
the exporter's `job` label to `exported_job`).

A Grafana dashboard for the same metrics lives at
`docs/grafana/dibs-dashboard.json` (import it and pick your Prometheus data
source; no endpoint is baked in).

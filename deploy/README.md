# Deploying Dibs at dibs.hivecommons.dev

Plain Kubernetes manifests — no helm. Apply in order (or all at once):

```sh
kubectl apply -f deploy/namespace.yaml
kubectl apply -f deploy/pvc.yaml
kubectl apply -f deploy/configmap.yaml
kubectl apply -f deploy/secret.yaml      # fill in real values first (see below)
kubectl apply -f deploy/deployment.yaml
kubectl apply -f deploy/service.yaml
kubectl apply -f deploy/ingress.yaml
```

## Prerequisites

1. **DNS**: a `dibs.hivecommons.dev` A/CNAME record pointing at the cluster's
   ingress load balancer (same target as `hive.hivecommons.dev`). The legacy
   `dibs.kubestellar.io` record stays until it is demoted to a redirect
   (hivecommons/hive#5925).
2. **cert-manager** with a `letsencrypt` ClusterIssuer (the ingress requests
   the TLS cert via the `cert-manager.io/cluster-issuer` annotation; adjust
   the issuer name in `ingress.yaml` if yours differs).
3. **Image**: `ghcr.io/hivecommons/dibs` is published automatically by the
   `docker.yml` workflow on every push to `main` whose CI run is green (tags
   `latest` + commit sha). Pin the Deployment to a sha for production.
4. **Hub session sharing**: Dibs authenticates by validating the hub's
   `hive_hub_user` session cookie, so Dibs and the hub must share a
   registrable domain — the hub scopes the cookie to `.hivecommons.dev`, and
   a browser will not send it to a host outside that scope. This is why Dibs
   is served at `dibs.hivecommons.dev` and why the legacy host must become a
   redirect rather than keep serving the app (hivecommons/hive#5925).

## Environment variables

| Variable | Source | Default | Purpose |
| --- | --- | --- | --- |
| `DIBS_ADDR` | ConfigMap | `:8080` | Listen address |
| `DIBS_BASE_PATH` | ConfigMap | `/` | URL prefix (root on the subdomain) |
| `HUB_URL` | ConfigMap | `https://hive.hivecommons.dev` | Hub origin for auth + registry sync |
| `DATA_DIR` | ConfigMap | `/data` | JSON store root (backed by the PVC) |
| `DIBS_LLM_BASE_URL` | Secret (optional) | unset | litellm gateway; unset ⇒ deterministic fallback matcher |
| `DIBS_LLM_API_KEY` | Secret (optional) | unset | Gateway API key |
| `DIBS_LLM_MODEL` | Secret (optional) | gateway default | Model name |
| `DIBS_GITHUB_TOKEN` | Secret (optional) | unset | Token used to open credited settlement issues; unset ⇒ accepts recorded, issues not opened |
| `DIBS_ADMINS` | Secret (optional) | unset | Comma-separated, case-insensitive GitHub logins granted admin access (idea-moderation endpoints) |
| `DIBS_CLANKER_MARKERS` | ConfigMap (optional) | `hive: agent=,kubestellar-hive[bot]` | Comma-separated, case-insensitive markers matched against PR branch/labels/body/author to classify agent activity |
| `DIBS_INTAKE_MAX_MB` | ConfigMap (optional) | `25` | Max upload size (MB) accepted for idea-intake file uploads |
| `DIBS_STT_URL` | Secret (optional) | unset | Speech-to-text gateway; unset ⇒ voice/audio idea submission disabled |
| `DIBS_STT_KEY` | Secret (optional) | unset | ****** for the STT gateway |
| `DIBS_STT_MODEL` | Secret (optional) | unset | Model name routed by the STT gateway |
| `REPOS_SEED_FILE` | — (optional) | unset | Static repo-registry seed for dev/demo |

The legacy `IDEATE_*` names (the product's pre-rename prefix) are still
honored as fallbacks for every `DIBS_*` variable.

Create the secret with real values instead of applying the placeholder:

```sh
kubectl -n dibs create secret generic dibs-secrets \
  --from-literal=DIBS_GITHUB_TOKEN=ghp_... \
  --from-literal=DIBS_LLM_BASE_URL=https://... \
  --from-literal=DIBS_LLM_API_KEY=sk-...
```

## Notes

- The container runs as non-root (distroless `nonroot`, uid 65532) with a
  read-only root filesystem; only `/data` (the PVC) is writable.
- Single replica: the JSON file store is single-writer by design. The PVC is
  `ReadWriteMany` (OCI FSS) so `RollingUpdate` (maxSurge=1, maxUnavailable=0)
  can overlap the old and new pods — rollouts are zero-downtime.
- Health/readiness: `GET /healthz` is a liveness-only check (process is up)
  returning `{"status":"ok","version":"<git sha>"}`. `GET /readyz` additionally
  verifies the `/data` store directory is accessible and writable, returning
  503 if not — this is what the readiness probe uses, so a pod is never
  routed traffic before (or after) its store dependency is usable.

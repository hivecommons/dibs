# Deploying Dibs at dibs.kubestellar.io

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

1. **DNS**: a `dibs.kubestellar.io` A/CNAME record pointing at the cluster's
   ingress load balancer (same target as `hive.kubestellar.io`).
2. **cert-manager** with a `letsencrypt` ClusterIssuer (the ingress requests
   the TLS cert via the `cert-manager.io/cluster-issuer` annotation; adjust
   the issuer name in `ingress.yaml` if yours differs).
3. **Image**: `ghcr.io/kubestellar/dibs` is published automatically by the
   `docker.yml` workflow on every push to `main` (tags `latest` + commit sha).
   Pin the Deployment to a sha for production.
4. **Hub session sharing**: Dibs authenticates by validating the hub's
   `hive_hub_user` session cookie. For the browser to send it to
   dibs.kubestellar.io, the hub must scope the cookie to `.kubestellar.io`
   (hub-side follow-up).

## Environment variables

| Variable | Source | Default | Purpose |
| --- | --- | --- | --- |
| `DIBS_ADDR` | ConfigMap | `:8080` | Listen address |
| `DIBS_BASE_PATH` | ConfigMap | `/` | URL prefix (root on the subdomain) |
| `HUB_URL` | ConfigMap | `https://hive.kubestellar.io` | Hub origin for auth + registry sync |
| `DATA_DIR` | ConfigMap | `/data` | JSON store root (backed by the PVC) |
| `DIBS_LLM_BASE_URL` | Secret (optional) | unset | litellm gateway; unset ⇒ deterministic fallback matcher |
| `DIBS_LLM_API_KEY` | Secret (optional) | unset | Gateway API key |
| `DIBS_LLM_MODEL` | Secret (optional) | gateway default | Model name |
| `DIBS_GITHUB_TOKEN` | Secret (optional) | unset | Token used to open credited settlement issues; unset ⇒ accepts recorded, issues not opened |
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
- Single replica: the JSON file store is single-writer by design
  (`ReadWriteOnce` PVC, `Recreate` strategy).
- Health/readiness: `GET /healthz` returns `{"status":"ok","version":"<git sha>"}`.

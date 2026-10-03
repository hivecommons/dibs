# Runbook: rolling back a bad Dibs release

`docker.yml` publishes `ghcr.io/hivecommons/dibs` on every push to `main`
whose CI run is green, tagged both `:latest` and `:<full-commit-sha>`. The
Deployment in `deploy/deployment.yaml` runs `:latest`, so a bad merge can
reach the cluster as soon as the pod is restarted or rescheduled. This
runbook covers getting back to a known-good build.

## Detect

- `kubectl -n dibs rollout status deploy/dibs` hangs, or the new pod never
  becomes Ready: `/readyz` returns 503 when the store's data directory is not
  accessible and writable (see `pkg/server/server.go`).
- `kubectl -n dibs logs deploy/dibs` shows `readyz: store ping failed: ...`
  with the underlying filesystem error.
- `/healthz` reports the running `version` (the commit hash); compare it with
  the commit you expect.
- Users report broken pages or API errors after a merge to `main`.

Because the rollout uses `maxUnavailable: 0`, a pod that never becomes Ready
leaves the previous pod serving traffic. Confirm which pods are serving before
acting: `kubectl -n dibs get pods -l app=dibs`.

## Contain

1. Find the last good commit SHA (the one whose image was serving correctly):
   `git log --oneline origin/main`, or `/healthz` on the old pod.
2. Pin the Deployment to that immutable tag instead of `:latest`:
   ```sh
   kubectl -n dibs set image deploy/dibs dibs=ghcr.io/hivecommons/dibs:<good-sha>
   kubectl -n dibs rollout status deploy/dibs
   ```
   Do not use `kubectl rollout undo` on its own: the previous revision is
   also `:latest`, which resolves to the bad image on the next pull.
3. Verify: `curl -fsS <base>/readyz` returns `{"status":"ok","version":"<good-sha>"}`.

## Data safety

The store is a single JSON file tree on the `dibs-data-rwx` volume, shared by
the old and new pod during a rollout. A bad release may have written data the
older build cannot read. Before rolling back across a store-format change,
snapshot the volume contents (for example
`kubectl -n dibs exec deploy/dibs -- tar -C /data -cf - . > dibs-data.tar`),
and do not delete the PVC.

## Fix forward

1. Branch from `main`, fix or revert the offending commit, and add a
   regression test per `CONTRIBUTING.md`.
2. Land it through the normal PR process; `docker.yml` publishes the new
   `:latest` and `:<sha>` after CI is green.
3. Re-point the Deployment at the fixed SHA (or back to `:latest`) and confirm
   rollout.

## After

- `/healthz` and `/readyz` report the expected version and `ok`.
- Note the incident and the SHA that was pinned in the tracking issue/PR so
  the next reader can see what was actually done.
- Consider keeping the Deployment pinned to a SHA until the fix is verified.

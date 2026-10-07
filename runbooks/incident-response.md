# Runbook: responding to a Dibs incident

Use this when users report errors or the service looks unhealthy and the cause
is not yet known. If a recent merge is the obvious cause, go straight to
[release-rollback.md](release-rollback.md).

## 1. Triage (first 5 minutes)

1. Confirm user impact: which routes fail, since when, for whom.
2. Check the pods: `kubectl -n dibs get pods -l app=dibs`.
3. Probe both endpoints (see `pkg/server/server.go`):
   - `curl -fsS <base>/healthz`: process is alive; reports `version`.
   - `curl -fsS <base>/readyz`: 503 means the store's data directory is not
     accessible and writable, so the pod is not serving traffic.
4. Read the logs: `kubectl -n dibs logs deploy/dibs --since=30m`.
   - `readyz: store ping failed: ...` carries the filesystem error.
   - `metrics http` JSON lines (`method`, `route`, `status`=5xx, `count`, `avg_ms`) are
     flushed periodically and show which route groups are failing or slow.

## 2. Decide

| Symptom | Likely cause | Action |
|---|---|---|
| `/readyz` 503, pod not Ready | data volume unavailable or read-only | Check the `dibs-data-rwx` PVC and mount (`kubectl -n dibs describe pod`, `kubectl -n dibs get pvc`). Do not delete the PVC. |
| Errors began right after a rollout | bad release | Follow [release-rollback.md](release-rollback.md). |
| Pod restarting | crash or OOM | `kubectl -n dibs describe pod` for last state; `kubectl -n dibs logs --previous`. |
| 4xx on authenticated routes only | auth or secret misconfiguration | Verify the `dibs` secret and configmap in `deploy/` match what is deployed. |

## 3. Communicate

Record in the tracking issue: start time, user impact, current hypothesis, and
each action taken with its time. Update it whenever the state changes.

## 4. Close out

- Verify `/healthz` and `/readyz` are `ok` and the 5xx `metrics http` log lines have
  stopped.
- If data was lost or corrupted, follow [data-recovery.md](data-recovery.md).
- Open a postmortem from [postmortem-template.md](postmortem-template.md) for
  any user-visible outage or data-affecting event.

# Runbook: backing up and recovering Dibs data

Dibs persists everything as JSON files under `DATA_DIR` (`/data` in the pod,
the `dibs-data-rwx` volume): ideas in `ideas/<id>.json` with an
`ideas/index.json`, plus notifications and repo profiles. There is no
database and no built-in backup, so recovery depends on copies taken here.
The backing `PersistentVolume` uses `Retain`, so deleting the PVC does not
delete the data, but nothing protects against bad writes or deleted files.

## Take a backup

Before any risky change (migration, manual edit, release with store changes).
The Dibs image is distroless (no shell, no `tar`), so `kubectl exec deploy/dibs
-- tar` fails. Mount the claim **read-only** in a short-lived helper pod and
stream a tar out of it:

```sh
kubectl apply -f deploy/backup-pod.yaml
kubectl -n dibs wait --for=condition=Ready pod/dibs-backup-manual --timeout=120s
TS=$(date -u +%Y%m%dT%H%M%SZ)
kubectl -n dibs exec dibs-backup-manual -- sh -c 'cd /data && find . -type f -exec sha256sum {} + | sort -k2' > dibs-data-$TS.sha256
kubectl -n dibs exec dibs-backup-manual -- tar -C /data -cf - . > dibs-data-$TS.tar
kubectl -n dibs delete pod dibs-backup-manual
```

Verify before relying on it:

```sh
tar -tf dibs-data-$TS.tar | grep -x './ideas/index.json'
tar -tvf dibs-data-$TS.tar | awk '{print $3":"$4}' | sort -u   # 65532:65532
mkdir v && tar -C v -xf dibs-data-$TS.tar && (cd v && find . -type f -exec shasum -a 256 {} + | sort -k2) | diff dibs-data-$TS.sha256 -
```

Every file must match. `repo-news.json` is a derived cache the running pod
rewrites often, so it alone may differ between the two passes; rerun if
anything else differs. Writes are atomic (temp file then rename), so a copy
taken while the pod is running is consistent per file. Keep copies outside
the cluster, access-controlled (they contain private ideas), and never attach
them to GitHub.

## Decide

| Symptom | Action |
|---|---|
| `/readyz` 503, volume missing or read-only | Not a data loss yet. Fix the mount first ([incident-response.md](incident-response.md)). |
| One idea or the index is corrupt or wrong | Restore only those files (below). |
| Many files missing or bad data written by a release | Roll back first ([release-rollback.md](release-rollback.md)), then restore. |

## Restore

1. Stop writers so nothing overwrites the restore:
   `kubectl -n dibs scale deploy/dibs --replicas=0`.
2. Keep the current state: take a backup of whatever is on the volume now.
3. Restore from a backup tar by running a one-off pod that mounts the
   `dibs-data-rwx` claim, then extract the needed paths (or all) into `/data`
   and keep ownership at uid/gid `65532`.
4. Restart: `kubectl -n dibs scale deploy/dibs --replicas=1`.
5. If only individual `ideas/<id>.json` files were restored and `index.json`
   was not, restore `index.json` from the same backup so listings match.

## Verify

- `curl -fsS <base>/readyz` returns `ok` and the pod is Ready.
- Spot-check restored ideas through the API and confirm `metrics http` log lines show
  no sustained `status=5xx`.
- Record what was lost (the window between the backup and the incident) in the
  postmortem ([postmortem-template.md](postmortem-template.md)).

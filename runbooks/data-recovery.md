# Runbook: backing up and recovering Dibs data

Dibs persists everything as JSON files under `DATA_DIR` (`/data` in the pod,
the `dibs-data-rwx` volume): ideas in `ideas/<id>.json` with an
`ideas/index.json`, plus notifications and repo profiles. There is no
database and no built-in backup, so recovery depends on copies taken here.
The backing `PersistentVolume` uses `Retain`, so deleting the PVC does not
delete the data, but nothing protects against bad writes or deleted files.

## Take a backup

Before any risky change (migration, manual edit, release with store changes):

```sh
kubectl -n dibs exec deploy/dibs -- tar -C /data -cf - . > dibs-data-$(date +%Y%m%dT%H%M%S).tar
tar -tf dibs-data-*.tar | head   # confirm it lists ideas/index.json
```

Writes are atomic (temp file then rename), so a copy taken while the pod is
running is consistent per file. Keep copies outside the cluster.

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
- Spot-check restored ideas through the API and confirm `metrics:` lines show
  no sustained `status=5xx`.
- Record what was lost (the window between the backup and the incident) in the
  postmortem ([postmortem-template.md](postmortem-template.md)).

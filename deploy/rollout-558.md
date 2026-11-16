# Operator handoff: production update #558

**Prepared, not deployed.** Only an authorized OKE operator may execute this
rollout. Keep [#558](https://github.com/hivecommons/dibs/issues/558) open until
the operator records the results below. The owner-approved weekly reviewed
cadence and out-of-cycle important fixes are tracked separately in
[#567](https://github.com/hivecommons/dibs/issues/567); neither tracker
permits deploying `latest` or unattended cluster writes.

## Selected release and evidence

The earlier preparation pinned `d411cb9` (#570); it was never applied. The
pin is now `main` `70a2db52dcc3878f66d11f34e5f5d3dccfd3e900` (includes the
#672 recorder upload and #673 mic meter). Successful runs for that exact sha:

- [CI](https://github.com/hivecommons/dibs/actions/runs/43956278861)
- [Go Security](https://github.com/hivecommons/dibs/actions/runs/43956278856)
- [OpenSSF Scorecard](https://github.com/hivecommons/dibs/actions/runs/43956280536)
- [FOSSA license scan](https://github.com/hivecommons/dibs/actions/runs/43956278838)
- [Docker publication](https://github.com/hivecommons/dibs/actions/runs/43956604071)

GHCR's full-sha tag resolves to this OCI index; the response digest matches
SHA-256 of the raw index body:

```text
ghcr.io/hivecommons/dibs@sha256:b65c4f332415c1c68c4812e0948d9356180e5f48c6ae2f7443346698c6745079
linux/amd64: sha256:435091659577f6b51a180ae1efe3082d85dc008fd71a29b0aaff0934142b94a8
linux/arm64: sha256:f4a2142abfa354f5718e95ae3c173a2c01d0fb970c09f39940fd76a252cc2c2c
```

Production nodes are `linux/arm64`, so the rolled-out pod's `imageID` should
be the arm64 manifest above.

### Running release (rollback target)

Read-only inspection of the production cluster before the rollout:

- Deployment `dibs/dibs` revision 61 runs the moving-sha tag
  `ghcr.io/hivecommons/dibs:f55e314c8662cbeb1056e01a76602c5e46f2df86`;
  `/healthz` reports that sha.
- That tag resolves to index
  `sha256:563ff9a9373ffff745765fb3c0b1ec0de9ff4aab00c03a410a5832b24c10250b`;
  the pod's `imageID` is its arm64 manifest
  `sha256:455e0d876ca3322985176db67314d82f46e06c4dae841bbe0824e841d99434b9`.
- **Rollback image:**
  `ghcr.io/hivecommons/dibs@sha256:563ff9a9373ffff745765fb3c0b1ec0de9ff4aab00c03a410a5832b24c10250b`

### Live drift corrected by this pin

The live objects were last applied from an older manifest and hand-edited
since. Against the cluster, `kubectl diff` of this pin shows:

| Object | Live | This pin |
|---|---|---|
| Deployment image | sha **tag** | immutable **digest** |
| readiness probe | `/healthz` | `/readyz` (#178) |
| SA token | mounted | `automountServiceAccountToken: false` (#316) |
| `/tmp` | read-only root fs, not writable | `emptyDir` `sizeLimit: 512Mi` (new, for #672) |
| memory limit | 256Mi | 512Mi (new, for #595 worker + uploads) |
| metrics port / `DIBS_METRICS_ADDR` / Service port | absent | `:9090`, internal only (#310) |
| Ingress `dibs` annotations | **none** | `proxy-buffering: off` (#25), `proxy-body-size: 30m` (#624), `proxy-read-timeout: 330` (#672) |

`ingress.yaml` previously also routed `dibs.kubestellar.io` to the app; in
production that host is a separate redirect Ingress, now captured as
`legacy-redirect.yaml` (no diff against live). The `cert-manager` annotation
was dropped from `ingress.yaml`: it has no `tls[]` entry to act on.

## Actual persistence/rollback review

Reviewed the source delta from the running `f55e314` to `d411cb9` (the earlier preparation),
including `pkg/store`, `pkg/notify`, `pkg/registry`, `pkg/history`, `pkg/news`,
`pkg/catalog`, and the shared `pkg/fsutil` writer, against
[release-rollback §2](../runbooks/release-rollback.md#2-check-for-data-changes-first).
This is not a claim that all persistence behavior is unchanged:

- Ideas remain `ideas/<id>.json` with `ideas/index.json`; the index entry
  layout is unchanged. `Idea` adds optional `cncfCatalogHash` (#521). Missing
  hashes invalidate derived suggestions; old readers use `json.Unmarshal`
  and tolerate unknown fields. Old writers can drop the hash, causing
  recomputation after re-upgrade, not an idea schema migration.
- #562 rewrites the index at startup to prune entries whose idea files are
  missing, as well as adopting orphans. #564 skips removed siblings in the
  duplicate-issue scan. **Inspect unexplained missing files before starting
  the new image**: this repair is not undone by an image rollback.
- Idea deletion now restores the previous file/index entry on index-write
  failure. Unicode length validation and case-insensitive offer lookup
  change behavior, not the persisted layout.
- Notifications and repo profiles retain their JSON arrays and paths;
  persistence now uses the shared atomic JSON writer. History/news retain
  their layouts and restore in-memory state after failed writes. News may
  regenerate fallback cards; catalog refresh carries enrichment forward
  and rejects an empty replacement. These are derived-data changes, not
  evidence that old code reproduces the new matching results.

No incompatible required-field or path migration was found. Image-only
rollback is expected to remain readable, but is **not byte-for-byte data
rollback**: the new process can rewrite indexes and caches, and old writers
can remove optional fields. Back up the entire data volume, not just ideas.
If the operator finds a different running version, extra writers, or an
unexplained inconsistent store, stop and re-review before proceeding.

### Delta d411cb9..70a2db5 (60 commits)

No new persisted fields, files or paths (`pkg/store`, `notify`, `registry`,
`history`, `news`, `catalog` diffs are GitHub-response decoding, pagination
coverage and in-memory casefolding: #618, #620, #648). New behavior that
needs deployment changes:

- **#672 chunked recorder**: `POST /api/intake/audio` creates a temp file
  with `os.CreateTemp("", ...)`, i.e. in `/tmp`. The root filesystem is
  read-only, so without the new `/tmp` emptyDir every recording start fails
  with 500. Sessions are in process memory: a recording in progress during
  the rollout is lost (the browser gets 404 and must re-record).
- **#672** STT finish can block up to 5 min; needs the 330 s ingress read
  timeout. **#624** uploads above 1 MB need `proxy-body-size`.
- **#595/#602/#606** document extraction re-execs the binary as a child
  with a 96 MiB soft / ~160 MiB `RLIMIT_DATA` budget, in the same pod memory
  limit as the parent that holds the upload; hence 512Mi.
- **#629** idle (120 s) and header (10 s) server timeouts; `/mcp` SSE
  unaffected (no server-wide read/write timeout).
- **#613** `HUB_URL` trailing slash normalised (live value has none).
- **#654/#664** `dibs ingest plaud|wisprflow` are CLI subcommands; nothing
  runs in the server.
- Optional env (defaults fine, none required): `DIBS_INTAKE_MAX_MB`,
  `DIBS_INTAKE_EXTRACT_WORKER`, `DIBS_CLANKER_MARKERS`. The live Secret
  already carries the `DIBS_STT_*` keys, so the recorder is live after the
  rollout. No new Secrets or PVCs.

Store state at backup time: index entries and idea files match one to one
(no missing files, no orphans), so the #562 startup prune is a no-op.

## Operator gates (before any image change)

1. Verify the intended cluster/context and namespace. Capture the current
   Deployment YAML, rollout history, actual container name/image, pod
   `imageID`s, replica count and public health/readiness output in the
   restricted operational record. Do not post secrets or user data to GitHub.
   For example, these are read-only:

   ```sh
   kubectl config current-context
   kubectl -n dibs get deploy/dibs -o yaml
   kubectl -n dibs rollout history deploy/dibs
   kubectl -n dibs get pods -l app=dibs -o wide
   kubectl -n dibs get pods -l app=dibs -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.containerStatuses[*].imageID}{"\n"}{end}'
   ```

2. Resolve and retain the **actual previous immutable image**, including
   architecture. Ensure it remains pullable and record a tested rollback
   command using that digest. A health sha alone is not the previous image
   digest. Do not assume this handoff knows the cluster's image or revision.
3. Follow [data recovery](../runbooks/data-recovery.md) and obtain a verified,
   access-controlled backup outside the cluster, including all `/data`
   files, permissions and ownership (65532). Validate archive contents and
   recovery procedure. The production image is distroless: **do not assume
   `kubectl exec ... tar` works**. Use the operator's approved storage
   snapshot/backup tooling or an approved volume-mounted backup pod.
   Atomic writes only ensure per-file consistency; use a quiesced writer or
   the storage team's application-consistent snapshot procedure for a
   coherent cross-file backup. Coordinate any maintenance window explicitly.
   Do not delete/recreate the PVC or restore over a live writer.
4. Confirm the backup and store inspection are satisfactory, normal replica
   count is one, and no other rollout is active. Existing RollingUpdate can
   briefly overlap old/new processes on RWX storage; it is not a cross-process
   locking mechanism. Avoid user mutations during overlap, or use an
   operator-approved quiesced maintenance procedure. Do not introduce extra
   steady-state replicas for this single-writer JSON store.

## Execute and verify (operator only)

After all gates pass, from a checkout of `main` at this pin, with the
production cluster's context selected:

```sh
kubectl -n dibs diff -f deploy/configmap.yaml -f deploy/service.yaml -f deploy/ingress.yaml -f deploy/deployment.yaml
kubectl -n dibs apply -f deploy/configmap.yaml -f deploy/service.yaml -f deploy/ingress.yaml
kubectl -n dibs apply -f deploy/deployment.yaml
kubectl -n dibs rollout status deploy/dibs --timeout=180s
kubectl -n dibs get pods -l app=dibs -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.containerStatuses[*].imageID}{"\n"}{end}'
curl -fsS https://dibs.hivecommons.dev/healthz   # version 70a2db52dcc3878f66d11f34e5f5d3dccfd3e900
curl -fsS https://dibs.hivecommons.dev/readyz
curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' https://dibs.kubestellar.io/
```

ConfigMap first so the new pod starts with `DIBS_METRICS_ADDR`. Do not
apply `secret.yaml` (placeholder) or `pvc.yaml`. Stop on any failed command.
`/healthz` must report the full selected sha and the pod `imageID` must be
the arm64 manifest above. Spot-check existing ideas without creating data;
inspect logs for sustained 5xx.

### Rollback

```sh
kubectl -n dibs set image deploy/dibs dibs=ghcr.io/hivecommons/dibs@sha256:563ff9a9373ffff745765fb3c0b1ec0de9ff4aab00c03a410a5832b24c10250b
kubectl -n dibs rollout status deploy/dibs --timeout=180s
curl -fsS https://dibs.hivecommons.dev/healthz   # version f55e314c8662cbeb1056e01a76602c5e46f2df86
```

The ingress annotations, `/tmp` volume, memory and probe changes are safe for
the old image and can stay. Then revert the pin in a PR so a later apply does
not reintroduce the failed release. If data was damaged, stop writers and
follow [data recovery](../runbooks/data-recovery.md).

## Required completion record on #558

- Operator and UTC execution time/context confirmation.
- Actual previous image/digest and expected previous health version.
- Backup reference and successful verification (no private data attached).
- Target sha/index digest and observed platform imageID.
- Rollout result, final replica/readiness state, public health version and
  readyz HTTP status, and post-rollout error/data spot checks.
- Rollback result if needed, or explicit successful acceptance.

Only that operator evidence completes #558. A merged handoff, green CI, or
an approved owner decision alone does not establish deployment.

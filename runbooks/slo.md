# Service level objectives for Dibs

These SLIs are measurable today without a metrics backend: readiness probe
state from Kubernetes and the periodic `metrics:` log lines emitted by
`pkg/server/metrics.go`. When a backend is chosen (issue #184), translate each
SLI into a recorded query and add burn-rate alerts that link back to
[incident-response.md](incident-response.md).

| SLI | Definition | Source | Objective (30 days) |
|---|---|---|---|
| Availability | Share of time at least one pod is Ready (`/readyz` 200) | Kubernetes pod Ready condition | 99.5% |
| Request success | Share of requests not answered `5xx` | `metrics:` lines, `status` class | 99.5% |
| Latency | Average `avg_ms` for API routes stays under 500 ms in each 5-minute flush | `metrics:` lines, `avg_ms` | 99% of flushes |

Probe semantics: `/healthz` is liveness only (process is up). `/readyz`
checks that the store's data directory is accessible and writable, which is
required to serve traffic, so it is the signal for availability.

## Measuring from logs

```sh
kubectl -n dibs logs deploy/dibs --since=24h | grep 'metrics:' | grep 'status=5xx'
```

Sum `count` by `status` class to compute the success ratio. Flushes reset the
counters, so each line covers one 5-minute window.

## Error budget policy

- Budget at 99.5% over 30 days is about 3.6 hours of unavailability, or 0.5%
  of requests.
- Above 50% of the budget spent: prefer fixes and reliability work over new
  features until the window recovers.
- Budget exhausted: release only fixes; follow
  [release-rollback.md](release-rollback.md) for any suspect rollout.
- Any budget-consuming incident gets a postmortem from
  [postmortem-template.md](postmortem-template.md).

## Review

Revisit these targets after the first month of measured data. Targets may be
tightened freely; loosening one requires a written reason in the tracking issue.

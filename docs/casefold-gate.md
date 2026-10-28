# GitHub identifier comparison audit

GitHub owner, repository and login identifiers are case-insensitive. Preserve
stored/display spelling; use `strings.EqualFold` for identity comparisons or
`strings.ToLower` for ephemeral set/map keys on both insertion and lookup.
Registry lookups already resolve to the registered spelling.

`TestRepoIDCasefoldGate` scans non-test Go source under `pkg/` and `cmd/` in the
existing test suite. It reports raw identifier equality/inequality, switch tags,
and map-key-shaped index expressions. Empty-string checks are safe. Reviewed
exact sites have a `file:line` allowlist with individual reasons; line drift
requires re-review, not a broad exemption. This is syntactic, not type/data-flow
analysis: aliases, wrappers, already-normalised locals and canonical registry
values still need human review.

Allowlist entries are pinned to `file:line`, so an edit above a reviewed site
moves it off its entry. The gate then **fails in every mode** with the stale
entries listed: re-pin each one to the current line (re-reading the reason
against the new code) or delete it. Without that check the site resurfaces as
an unreviewed hit that an advisory `go test` run without `-v` never prints.

The gate is **advisory by default**. See its diagnostics with:

```sh
go test -count=1 -v ./pkg/store -run '^TestRepoIDCasefoldGate$'
```

Opt in locally to failing on unreviewed hits:

```sh
DIBS_CASEFOLD_GATE=strict go test -count=1 -v ./pkg/store -run '^TestRepoIDCasefoldGate$'
```

## CI observation and ratchet

CI runs the audit again with `-count=1 -v` directly after the test suite and
copies its full output to both the CI log and job summary. This is a step in
the existing job. A passing non-verbose `go test` discards `t.Logf` output, so
runs since the original 2026-10-23 rollout supplied no usable observations.
The observation clock restarts after #508 merged on 2026-10-28, once this
visible CI step lands; do not count the earlier period as zero hits.

The recorded baseline is **0 unreviewed sites**, measured at `main@1938d81`
(#508), and stored as `casefoldBaseline` in the gate test. New unreviewed sites
must be reviewed or fixed, rather than accepted by raising the baseline.

Per the decision on #509, CI uses `DIBS_CASEFOLD_GATE=ratchet-warning` for one
release. Counts above the baseline print `WARNING` and the individual sites
without failing. Stale allowlist entries, scanner errors and test failures
still fail the job; the reporting pipeline preserves the test exit status.

After the first release containing this step, open a follow-up PR changing the
workflow environment to `DIBS_CASEFOLD_GATE=ratchet`. That mode fails only when
the unreviewed count exceeds the recorded baseline (equal or lower passes).
It does not change the local advisory default. `strict` continues to fail on
any unreviewed site, independently of the baseline. Verify the visible CI
observations before enabling enforcement; the old two-week period without
diagnostics is not evidence for that switch.

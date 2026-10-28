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

The badge fix is deliberately a separate PR under #461. Until it lands, its
three map keys remain unreviewed hits, not allowlisted bugs. No workflow or CI
job is added. After approximately two weeks with zero unreviewed hits, open a
follow-up PR to make the gate failing by default; do not silently enable it as
part of this advisory rollout.

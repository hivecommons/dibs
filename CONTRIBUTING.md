# Contributing to Ideate

Thanks for contributing! A few ground rules:

## Developer Certificate of Origin (DCO)

All commits **must** be signed off, certifying the
[Developer Certificate of Origin](https://developercertificate.org/):

```sh
git commit -s -m "your message"
```

This adds a `Signed-off-by: Your Name <you@example.com>` trailer. PRs with
unsigned commits will not pass CI.

## Workflow

1. Fork / branch from `main`.
2. Make your change with tests.
3. Ensure `go build ./...`, `go vet ./...`, and
   `go test -race -count=1 ./...` are green.
4. Open a PR with a clear description. Prow manages approvals via
   `/lgtm` and `/approve`.

## Code conventions

- Standard library first; no external DB — the JSON file store is deliberate.
- Every HTTP route must work behind the `IDEATE_BASE_PATH` prefix.
- Private ideas must never appear in any listing other than the author's own —
  add a test if you touch listing code.

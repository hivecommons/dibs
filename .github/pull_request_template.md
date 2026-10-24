## Summary

<!-- What does this PR change, and why? Describe any user-visible change (web UI, HTTP API, MCP tools, or credit-wall API). -->

Fixes #<!-- issue number, if this PR resolves one -->

## Validation

<!-- Which commands did you run, and what did you check by hand? -->

## Checklist

See [CONTRIBUTING.md](https://github.com/hivecommons/dibs/blob/main/CONTRIBUTING.md) for the exact commands and the coverage floor.

- [ ] `go build ./...`, `go vet ./...` and `go test -race -count=1 -coverpkg=./... -coverprofile=coverage.out ./...` pass locally
- [ ] `gofmt -l .` prints nothing and `go mod tidy` leaves `go.mod` and `go.sum` unchanged
- [ ] Total coverage (`go tool cover -func=coverage.out`) is still at least the floor in CONTRIBUTING.md
- [ ] If the Go version changed: `go.mod` and the `golang:` tag in `Dockerfile` still match
- [ ] New or changed HTTP routes work behind the `DIBS_BASE_PATH` prefix
- [ ] If listing code changed: private ideas still appear only in the author's own listing, with a test
- [ ] Commits are signed off (`git commit -s`)

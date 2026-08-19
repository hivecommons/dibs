# Multi-stage build mirroring hive's pattern: the git hash is stamped into
# the binary via ldflags so `ideate --version` prints the running commit
# (freshness-probe friendly).
FROM golang:1.24-alpine AS builder
RUN apk add --no-cache git
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CI passes GIT_HASH explicitly (checkouts are often detached); local builds
# fall back to the working tree.
ARG GIT_HASH=unknown
RUN GH="$GIT_HASH" && \
    if [ "$GH" = "unknown" ] || [ -z "$GH" ]; then GH=$(git rev-parse HEAD 2>/dev/null || echo "unknown"); fi && \
    GS=$(echo "$GH" | cut -c1-7) && \
    CGO_ENABLED=0 go build -ldflags "-X main.gitHash=${GH} -X main.gitShort=${GS}" -o /ideate ./cmd/ideate

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /ideate /usr/local/bin/ideate
# JSON idea store + repo registry live here; mount a volume in production.
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["ideate"]

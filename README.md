# Ideate — a marketplace of ideas

> **Have ideas? Repos need them.**

Ideate is a two-sided marketplace that connects **ideators** — people with
great ideas but no time, tooling, or inclination to write code — with
**hive-managed repositories** whose AI agent swarms are hungry for well-formed
work.

Think of it as *Tinder for source repos*:

1. **Ideators post ideas** — public or private — in plain language / markdown.
2. **Repos opt in** to receive ideas, declaring topics and appetite.
3. **An LLM matches** ideas to repos that want them.
4. On **mutual acceptance**, the idea becomes a credited GitHub issue and the
   hive's AI agents implement it.

## Ideators are first-class contributors

Open source has always had a quiet contributor class with no outlet:
researchers, operators, designers, power users — people who know exactly what
a project needs but will never open a pull request. Ideate gives them one.

**Creators get credit. Agents do the work. Projects take the bow.**

Every implemented idea traces back to its ideator: the GitHub issue credits
the idea's author, and the resulting work carries that provenance. An idea is
a contribution.

## Architecture

Single Go binary (`cmd/ideate`) serving both the JSON API and an embedded
static UI, designed to be reverse-proxied at `hive.kubestellar.io/ideas`.

| Package | Purpose |
|---|---|
| `pkg/server` | HTTP server, base-path routing, embedded UI |
| `pkg/auth` | Auth bridge — validates the hive hub session cookie against the hub |
| `pkg/store` | Idea model + JSON file store (atomic writes, no external DB) |
| `pkg/api` | Idea CRUD API (author-scoped, private ideas never leak) |
| `pkg/registry` | Hive-managed repo profiles + "accepting ideas" opt-in |

### Configuration

| Env var | Default | Meaning |
|---|---|---|
| `IDEATE_BASE_PATH` | `/ideas` | Base path every route/asset is served under |
| `IDEATE_ADDR` | `:8080` | Listen address |
| `HUB_URL` | `https://hive.kubestellar.io` | Hive hub for session validation & repo sync |
| `DATA_DIR` | `/data` | JSON store directory |
| `REPOS_SEED_FILE` | (unset) | Static JSON seed of repo profiles for dev/demo |

### Roadmap

- **Wave 1 (this repo, now):** scaffold, hub auth bridge, idea CRUD, repo registry.
- **Wave 2:** LLM matching, swipe UX ("offer" / "pass"), TL;DR generation.
- **Wave 3:** issue settlement (credited GitHub issues), notifications, deploy.

## Development

```sh
go build ./...
go test -race -count=1 ./...
go run ./cmd/ideate            # serves on :8080 under /ideas
./ideate --version             # prints the embedded commit
```

## License

Apache-2.0 — see [LICENSE](LICENSE).

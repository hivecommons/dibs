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
static UI, deployed at its own subdomain: **idea.kubestellar.io**. The base
path is configurable (`IDEATE_BASE_PATH`), so path-prefixed reverse-proxy
deployments also work.

| Package | Purpose |
|---|---|
| `pkg/server` | HTTP server, base-path routing, embedded UI |
| `pkg/auth` | Auth bridge — validates the hive hub session cookie against the hub |
| `pkg/store` | Idea model + JSON file store (atomic writes, no external DB), offer/settlement state machine |
| `pkg/api` | Idea CRUD + matching/offer/feed/decide/notification API (author-scoped, private ideas never leak) |
| `pkg/registry` | Hive-managed repo profiles + "accepting ideas" opt-in |
| `pkg/match` | LLM idea↔repo scoring via litellm gateway, cached TLDRs, deterministic keyword fallback |
| `pkg/settle` | Settlement — credited GitHub issue on accept (`ideated` label) |
| `pkg/notify` | In-app notification feed (bell): matches, offers, decisions, issues |

### Configuration

| Env var | Default | Meaning |
|---|---|---|
| `IDEATE_BASE_PATH` | `/` | Base path every route/asset is served under (set e.g. `/ideas` for path-prefixed proxying) |
| `IDEATE_ADDR` | `:8080` | Listen address |
| `HUB_URL` | `https://hive.kubestellar.io` | Hive hub for session validation & repo sync |
| `DATA_DIR` | `/data` | JSON store directory |
| `REPOS_SEED_FILE` | (unset) | Static JSON seed of repo profiles for dev/demo |
| `IDEATE_LLM_BASE_URL` | (unset) | OpenAI-compatible gateway (hive litellm), e.g. `http://litellm:4000/v1`. Unset → deterministic keyword matcher |
| `IDEATE_LLM_API_KEY` | (unset) | Bearer token for the LLM gateway |
| `IDEATE_LLM_MODEL` | `gpt-4o-mini` | Model name routed by the gateway |
| `IDEATE_GITHUB_TOKEN` | (unset) | GitHub PAT for opening credited issues on accept. Unset → accepts are recorded, issues deferred |

### Roadmap

- **Wave 1 (done):** scaffold, hub auth bridge, idea CRUD, repo registry.
- **Wave 2 (done):** LLM matching + TLDRs, swipe UX ("offer" / "pass"), issue settlement (credited GitHub issues), notifications.
- **Wave 3:** deploy at idea.kubestellar.io, hive GitHub App settlement, ideator credit wall.

## Development

```sh
go build ./...
go test -race -count=1 ./...
go run ./cmd/ideate            # serves on :8080 at /
./ideate --version             # prints the embedded commit
```

## License

Apache-2.0 — see [LICENSE](LICENSE).

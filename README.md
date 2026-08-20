# Dibs — a marketplace of ideas

> **Your idea, your credit, their code.**
>
> The missing contributor layer for the AI-agent era.

Dibs is a two-sided marketplace that connects **ideators** — people who call
dibs on ideas: great insight, but no time, tooling, or inclination to write
code — with **hive-managed repositories** whose AI agent swarms are hungry
for well-formed work.

1. **Ideators post ideas** — public or private — in plain language / markdown.
2. **Repos opt in** to receive ideas, declaring topics and appetite.
3. **An LLM matches** ideas to repos that want them; both sides must say yes.
4. On **mutual acceptance**, Dibs hands the ideator a **prefilled GitHub
   issue** — they file it themselves, under their own GitHub account, so the
   issue is natively attributed to them (label `ideated`). The hive's AI
   agents implement it. Dibs is just the matchmaker: it never files the
   issue for you.

## Ideators are first-class contributors

Open source has always had a quiet contributor class with no outlet:
researchers, operators, designers, power users — people who know exactly what
a project needs but will never open a pull request. Open source has also
always known ideas are contributions — the all-contributors spec has a 💡
"ideas" emoji, and the academic CRediT taxonomy credits "Conceptualization"
as a first-class role. Dibs makes that credit real, machine-readable, and
public.

**Creators get credit. Agents do the work. Projects take the bow.**

Every implemented idea traces back to its ideator: the GitHub issue credits
the idea's author, the resulting work carries that provenance, and the public
**credit wall** (`/api/credits`, "Credit wall" in the UI) lists every ideator
whose idea shipped. An idea is a contribution.

## Architecture

Single Go binary (`cmd/dibs`) serving both the JSON API and an embedded
static UI, deployed at its own subdomain: **dibs.kubestellar.io**. The base
path is configurable (`DIBS_BASE_PATH`), so path-prefixed reverse-proxy
deployments also work.

| Package | Purpose |
|---|---|
| `pkg/server` | HTTP server, base-path routing, embedded UI |
| `pkg/auth` | Auth bridge — validates the hive hub session cookie against the hub |
| `pkg/store` | Idea model + JSON file store (atomic writes, no external DB), offer/settlement state machine |
| `pkg/api` | Idea CRUD + matching/offer/feed/decide/notification API (author-scoped, private ideas never leak) |
| `pkg/registry` | Hive-managed repo profiles + "accepting ideas" opt-in |
| `pkg/match` | LLM idea↔repo scoring via litellm gateway, cached TLDRs, "✨ Embellish" draft refinement, deterministic keyword fallback |
| `pkg/settle` | Settlement — prefilled GitHub new-issue URL the ideator files themselves (`ideated` label, 🐝 Dibs footer, URL-length budget) + issue-URL confirmation; legacy token mode. Ideas can target ANY GitHub repo: non-hive targets skip acceptance and their issues carry a "request a hive" growth CTA |
| `pkg/notify` | In-app notification feed (bell): matches, offers, decisions, issues |

### Configuration

| Env var | Default | Meaning |
|---|---|---|
| `DIBS_BASE_PATH` | `/` | Base path every route/asset is served under (set e.g. `/ideas` for path-prefixed proxying) |
| `DIBS_ADDR` | `:8080` | Listen address |
| `HUB_URL` | `https://hive.kubestellar.io` | Hive hub for session validation & repo sync |
| `DATA_DIR` | `/data` | JSON store directory |
| `REPOS_SEED_FILE` | (unset) | Static JSON seed of repo profiles for dev/demo |
| `DIBS_LLM_BASE_URL` | (unset) | OpenAI-compatible gateway (hive litellm), e.g. `http://litellm:4000/v1`. Unset → deterministic keyword matcher |
| `DIBS_LLM_API_KEY` | (unset) | Bearer token for the LLM gateway |
| `DIBS_LLM_MODEL` | `gpt-4o-mini` | Model name routed by the gateway |
| `DIBS_GITHUB_TOKEN` | (unset) | **Legacy mode only.** When set, Dibs opens the credited issue server-side on accept. Unset (default), the ideator files a prefilled GitHub issue themselves — native attribution |

Legacy `IDEATE_*` names (the pre-rename prefix) are still honored as
fallbacks for every `DIBS_*` variable.

### Roadmap

- **Wave 1 (done):** scaffold, hub auth bridge, idea CRUD, repo registry.
- **Wave 2 (done):** LLM matching + TLDRs, swipe UX ("offer" / "pass"), issue settlement (credited GitHub issues), notifications.
- **Wave 3 (done):** deployment (GHCR image + [`deploy/` manifests](deploy/README.md) for dibs.kubestellar.io), public landing page, public credit wall, ideator profile stats.
- **Wave 4 (done):** matchmaker settlement — the ideator files a prefilled GitHub issue themselves (native attribution, `ideated` label) and confirms the issue URL; LLM-assisted idea refinement on posting and repo-tailored expansion on submission; token-based settlement demoted to legacy.
- **Later:** cookie-domain/hub-OAuth session sharing, non-hive receptor repos, idea economics.

## Deployment

Every push to `main` publishes `ghcr.io/kubestellar/dibs` (`latest` +
commit sha) via `.github/workflows/docker.yml`; the image is distroless and
runs as non-root. Plain Kubernetes manifests for **dibs.kubestellar.io**
(Deployment, Service, cert-manager TLS Ingress, PVC, ConfigMap/Secret) live
in [`deploy/`](deploy/README.md), including the required DNS record and
hub-cookie prerequisites.

## Public surface (no sign-in required)

| Route | Purpose |
|---|---|
| `GET /` | Landing page (pitch + sign-in) — signed-in users get the full app |
| `GET /api/credits` | Credit wall data: settled ideas only (handle, title, TLDR, repo, issue URL) |
| `GET /healthz` | Health + embedded git version |

Everything else requires a hive hub session; signed-in ideators additionally
get `GET /api/me/stats` (ideas posted / offered / accepted / settled).

## Development

```sh
go build ./...
go test -race -count=1 ./...
go run ./cmd/dibs            # serves on :8080 at /
./dibs --version             # prints the embedded commit
```

## License

Apache-2.0 — see [LICENSE](LICENSE).

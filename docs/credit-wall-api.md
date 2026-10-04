# Credit-wall consumer API

This reference describes the existing HTTP API for dashboards and agents that
need credit-wall eligibility, settlement state, and rewards. It documents the
implementation, not a new eligibility service or a payments API. Rewards are
non-monetary points, levels, and badges; settlement means a GitHub issue was
recorded, **not** that the idea was implemented or its issue closed.

## Stability and transport

- **Experimental:** implemented, but unversioned; fields and behavior may change.
  Consumers should pin a deployment revision, tolerate additional fields, and
  recheck this reference when upgrading.
- **Stable:** reserved for a deliberately supported compatibility contract. No
  endpoint in this reference is designated stable yet. Promotion should include
  an agreed compatibility/deprecation policy and contract tests; documenting the
  current schema alone does not make that promise.

| Endpoint | Access | Stability | Purpose |
|---|---|---|---|
| `GET /api/credits` | Public | Experimental | Eligible credit-wall entries |
| `GET /api/leaderboard` | Public | Experimental | Ranked aggregate rewards |
| `GET /api/me` | Session | Experimental | Caller identity |
| `GET /api/me/stats` | Session | Experimental | Caller's lifecycle counts and rewards |
| `GET /api/ideas?scope=mine` | Session | Experimental | Discover caller's idea IDs and states |
| `GET /api/ideas/{id}` | Session + read permission | Experimental | Individual settlement state |
| `POST /api/ideas/{id}/offer` | Session + author | Experimental | Offer to a target repository |
| `POST /api/repos/{org}/{repo}/decide` | Session + repo owner | Experimental | Accept, decline, or pass |
| `POST /api/ideas/{id}/launch` | Session + author | Experimental | Prepare the GitHub issue form |
| `POST /api/ideas/{id}/confirm-issue` | Session + author | Experimental | Record the filed issue URL |

Prepend the configured `DIBS_BASE_PATH` to **every** route (for example,
`/dibs/api/credits` when the prefix is `/dibs`). An unset prefix means `/api/...`.
Successful responses below are HTTP 200 with `Content-Type: application/json`.
Times are JSON strings in Go's RFC 3339 format, potentially with fractional
seconds. `?` in the schema notation below means a field may be omitted, not that
it is JSON `null`. Arrays without entries are `[]` unless noted otherwise.

Authenticated HTTP routes use the hub's `hive_hub_user` session cookie. The
`Authorization: Bearer` flow described for [MCP](../README.md#submit-ideas-from-your-agent)
is **not** authentication for these REST routes. Browser writes must be
same-origin: the CSRF guard rejects cross-origin requests, including sibling
subdomains. Never embed session credentials in a public dashboard.

API errors have shape `{"error":"message"}`. Relevant statuses are:

| Status | Meaning |
|---|---|
| 400 | Invalid JSON/input, invalid URL, or disallowed lifecycle operation |
| 401 | Missing/invalid session on a protected route |
| 403 | Not the author/repo owner, or CSRF rejection |
| 404 | Missing idea/repo, or a private idea hidden from this caller |
| 413 | Request body exceeds the input limit (1 MiB) |
| 500 | Store/internal failure |
| 502 | Hub authentication backend unavailable |

Treat status codes as the primary error signal; message strings are not error
identifiers. Routing errors such as 405 need not use the JSON error envelope.
There is no pagination, cursor, webhook, or snapshot-consistency guarantee for
these endpoints. Poll reads as needed; do not infer a deletion or loss of credit
from a failed request.

## Eligibility and the public wall

### `GET /api/credits`

Response schema:

```text
{ credits: CreditEntry[] }
CreditEntry = {
  author: string, authorDisplay: string, title: string,
  symbol?: string, tldr?: string,
  repoID: string, issueURL: string, settledAt: timestamp
}
```

Example:

```json
{"credits":[{"author":"octocat","authorDisplay":"Octocat","title":"Improve onboarding","symbol":"$ONBOARD","repoID":"example/project","issueURL":"https://github.com/example/project/issues/42","settledAt":"2026-10-01T12:00:00Z"}]}
```

An idea is eligible exactly when its stored `status` is `settled`, regardless of
`visibility`. Entries are newest `updatedAt` first; ties have no defined order.
`settledAt` currently comes from the idea's **last update time**, not a separate,
immutable settlement timestamp. There is no idea ID in this response: do not
assume title, symbol, or issue URL is a durable primary key.

The wall intentionally exposes only attribution, title/TLDR, display symbol,
target repository, issue URL, and time. It does not expose idea bodies, offers,
or unsettled private ideas. Settling a private idea makes these credit facts
public; it does not change its visibility or make its detail endpoint public.
Use this endpoint, not authenticated idea listings, to build a public wall.

There is no standalone `/eligibility` endpoint. Before settlement, use an
authorized idea read to determine which action is currently available. Local
eligibility checks are hints; the write handlers remain authoritative.

## Identity and settlement-state reads

### `GET /api/me`

```text
{
  username: string, display_name?: string, email?: string,
  avatar_url?: string, admin?: boolean
}
```

Compare `username` with an idea's `author` to identify author-only actions.
Identity keys are GitHub logins or hub `provider:sub` keys; do not assume every
`author` can be linked to `github.com/{author}`. Display names are not identity
keys. Do not republish the caller's email.

### `GET /api/ideas?scope=mine` and `GET /api/ideas/{id}`

The list returns `Idea[]` (the default scope is also `mine`); the
detail route returns an `Idea` directly, not an envelope. Detail reads require
a session even for public ideas. A private idea is readable only by its author
or an owner of a repository to which it was explicitly offered; everyone else
gets 404. Writes below remain author-only even when a repo owner can read.

The settlement-relevant projection of `Idea` is:

```text
{
  id: string, author: string, authorDisplay: string,
  title: string, body: string, visibility: "public" | "private",
  status: "draft" | "offered" | "accepted" | "declined" |
          "issue_launched" | "settled",
  createdAt: timestamp, updatedAt: timestamp,
  targetRepo?: string, issueURL?: string, offers?: Offer[]
}
Offer = {
  repoID: string, status: "pending" | "accepted" | "declined",
  createdAt: timestamp, decidedAt?: timestamp, external?: boolean
}
```

This is a projection, not the entire idea schema: responses also include
matching and display data. The full JSON model is in
[`store.Idea`](../pkg/store/store.go). A missing `external` means false.

| Idea state | Meaning / next step |
|---|---|
| `draft` | Author can offer; repo owner can directly accept a public candidate |
| `offered` | Registered target needs acceptance; external target can launch directly |
| `declined` | Author may re-offer |
| `accepted` | Target selected; author can launch or directly confirm an issue |
| `issue_launched` | Prefilled form prepared; author must file the issue and confirm |
| `settled` | Issue URL recorded; eligible for the wall, terminal lifecycle state |

Launch requires a nonempty `targetRepo`. An `offered` idea may launch only if
that target is outside the current registry. `accepted` and `issue_launched`
may launch; confirmation requires `accepted` or `issue_launched`. Neither
acceptance nor launch alone qualifies an idea for the public wall.

## Settlement writes

### `POST /api/ideas/{id}/offer`

Request: `{ "repoID": "org/repo" }`. Response: the updated `Idea`.
A registered repo must be accepting ideas; the offer becomes `pending`. For a
private idea this explicitly reveals it to that repo's owner. An unregistered
GitHub-shaped target records `external: true` and sets `targetRepo` immediately,
without repo-owner acceptance. This validates the target's syntax, not its
existence. Duplicate non-declined offers and illegal transitions return 400.

### `POST /api/repos/{org}/{repo}/decide`

Request: `{ "ideaID": "idea-id", "decision": "accept" }`, where `decision`
is `accept`, `decline`, or `pass`. Only the registered repo's owner may decide.
Private ideas require an offer to that repository; decline requires a pending
offer. Accept sets `targetRepo` and moves an available idea to `accepted`.

Response shapes:

```text
accept:  { result: "accepted", idea: Idea, warning?: string }
decline: { result: "declined", idea: Idea }
pass:    { result: "passed" }
legacy successful accept: { result: "settled", idea: Idea, issueURL: string }
```

In optional legacy mode (`DIBS_GITHUB_TOKEN` configured), acceptance attempts to
open the issue server-side and settle immediately. Consumers must inspect the
returned result/status rather than assume every accept is already settled.
A legacy issue-opening failure returns 200 with `result: "accepted"` and a
`warning` string, leaving the author able to use the normal settlement flow.

### `POST /api/ideas/{id}/launch`

Request: `{ "title": "optional override", "body": "optional override" }`.
Send `{}` to use the idea's defaults; empty strings also select defaults.
Response:

```text
{
  url: string, repoID: string, title: string,
  fullBody: string, truncated: boolean
}
```

`url` is a prefilled GitHub **new-issue form**, not a filed issue URL. The author
files it under their own GitHub account. `fullBody` contains the untruncated
issue text; if `truncated` is true, the URL's body was shortened to fit its
length budget. Launch records `issue_launched`; re-launching from that state
rebuilds the URL without awarding another milestone. Overrides do not edit the
stored idea. Do not publish the returned body for a private idea.

### `POST /api/ideas/{id}/confirm-issue`

Request: `{ "issueURL": "https://github.com/org/repo/issues/42" }`.
Response: `{ result: "settled", idea: Idea }`.

The validator requires HTTPS, a GitHub host (`github.com` or `www.github.com`),
the target repo (case-insensitive), and a positive numeric issue number. It
validates **URL shape only**, not issue existence, authorship, implementation,
or merge state. This is author-attested settlement, not external verification.
A repeated confirmation after settlement returns 400; after an ambiguous
network failure, read the idea before retrying. Do not replay all writes
blindly: offer/accept/confirm are not generally idempotent.

## Reward reads

### `GET /api/me/stats`

Returns the caller's counts and derived progress directly:

```text
{
  posted: integer, offered: integer, accepted: integer, settled: integer,
  score: integer, level: Level, nextLevel?: Level,
  toNext: integer, pct: integer, badges: Badge[]
}
Level = { name: string, emoji: string, min: integer }
Badge = { id: string, name: string, emoji: string, desc: string }
```

Counts and rewards include **all of the caller's ideas**, including private
ones. `posted` counts existing ideas; `offered` counts ideas with offers or a
non-draft state; `accepted` counts `accepted`, `issue_launched`, and `settled`;
`settled` counts only `settled`. These are derived current-state counts, not an
immutable event ledger. External launches also count as accepted for rewards.
`nextLevel` is omitted at the top level, where `toNext` is 0 and `pct` is 100.

### `GET /api/leaderboard`

```text
{ leaderboard: LeaderboardEntry[] }
LeaderboardEntry = {
  rank: integer, author: string, authorDisplay: string,
  score: integer, level: Level, settled: integer, badges: Badge[]
}
```

Only authors with at least one settled idea are listed. Rank is one-based;
ordering is score descending, then settled count descending, then author
ascending. Scores and badges derive from **all** that author's ideas, not just
settled/public ones. This exposes aggregates, never private titles or bodies.
It does not expose `nextLevel`, `toNext`, or `pct`.

Current reward rules (experimental, not financial entitlements):

- Each idea earns 10 points for existing, plus 25 for offers/non-draft state,
  plus 50 for accepted/launched/settled state, plus 100 for settled: 185 total
  for a settled idea. Repeated reads do not award additional points.
- Levels: Bronze at 0, Silver at 100, Gold at 300, Platinum at 750.
- Badge IDs: `first-dibs` (at least one idea), `pollinator` (at least five
  distinct idea/repo pairings), `hivemind` (at least three distinct accepting
  target repos), `rainmaker` (at least one settled idea).

Render the returned level/badge metadata instead of hard-coding labels or
thresholds. Rules and derivation are implemented in [`pkg/game`](../pkg/game/game.go).

## Implementation and regression references

- Public wall/rewards: [`pkg/api/wave3.go`](../pkg/api/wave3.go),
  [`pkg/server/wave3_test.go`](../pkg/server/wave3_test.go).
- Settlement writes: [`pkg/api/settlement.go`](../pkg/api/settlement.go),
  [`pkg/api/wave2.go`](../pkg/api/wave2.go),
  [`pkg/server/settlement_test.go`](../pkg/server/settlement_test.go).
- Access and prefix routing: [`pkg/api/api.go`](../pkg/api/api.go),
  [`pkg/server/server.go`](../pkg/server/server.go).
- Reward derivation: [`pkg/game/game_test.go`](../pkg/game/game_test.go),
  [`pkg/server/gamification_test.go`](../pkg/server/gamification_test.go).

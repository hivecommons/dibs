# Trust and abuse controls for idea intake

Status: proposed design, not implemented. Tracks [issue #264](https://github.com/hivecommons/dibs/issues/264).
Implementation should follow in separate issues; this note does not change API behavior.

## Goals and boundaries

Keep spam and expensive intake work bounded without turning repository acceptance
into a popularity contest. Use authenticated identity, explicit moderation, and
an explainable quality hint. Maintainers retain acceptance and merge authority.
No anonymous uploads, payments, external database, or automatic account bans.

Today ideas have public/private visibility and a settlement lifecycle (`draft`,
`offered`, `accepted`, `declined`, `issue_launched`, `settled`), but no separate
moderation state. Authentication, CSRF checks, idea-size bounds, and the upload
size cap remain prerequisites, not replacements for abuse controls. The existing
admin access boundary is `DIBS_ADMINS`.

## Moderation states

Add a separate `moderationState` field; never reuse lifecycle `accepted` to mean
moderation approval. In this note, moderation approval is called `approved`.

| State | Meaning | Exposure and actions |
| --- | --- | --- |
| `pending` | New idea or materially edited content awaiting review | Author and authorized moderation review only; no discovery, matching, offers, acceptance, or issue launch |
| `approved` | Admin cleared this content for normal use | Existing visibility and explicit-offer rules still apply; normal lifecycle actions allowed |
| `flagged` | Admin quarantined suspected abuse for review | Same restrictions as pending; retain lifecycle/history rather than cancelling it |
| `rejected` | Admin confirmed spam, abuse, or unsuitable intake | Author can see the reason, revise, or delete; no discovery or new downstream work |

Transitions:

- Creation (including private ideas) starts `pending`.
- Only an admin may move `pending` to `approved` or `rejected`, or move
  `approved` to `flagged`. An admin can also flag pending content.
- Review resolves `flagged` to `approved` or `rejected`. A report alone does
  not quarantine an idea: otherwise coordinated reports could silence authors.
- An author edit to title, body, or tags returns the idea to `pending` from
  any state, invalidates matching caches, and recomputes the quality hint.
  An admin can reopen a rejection to `pending` on appeal. Visibility changes
  must never grant approval or escape quarantine.
- Authors cannot approve themselves. Repository owners can still decline offers,
  but cannot moderate globally unless they are also configured admins.

Store review events (actor, time, old/new state, short reason, content revision)
atomically alongside the idea using the JSON store. Review must name the revision;
reject a stale decision after an edit. Give the author a reason and a way to
request admin reconsideration; keep reporter identity and internal notes private.
Private content remains excluded from every listing except the author's own and
existing authorized admin review. An approved explicit private offer is still
visible only to its intended repository owner, never a public listing.

Enforce eligibility server-side on all readers and writers, including direct
reads, public discovery, credit/leaderboard/news aggregates, matching, offers,
acceptance, issue launch, and legacy server-side settlement. Suppressed content
should return the same not-found response as inaccessible private content to
unrelated callers. Do not revoke already-filed GitHub issues or erase settlement
history; quarantine blocks new work and removes public Dibs exposure. Reapproval
restores eligibility, not visibility, and does not replay old notifications.

## Per-ideator rate limits

Proposed configurable starting defaults (tune from rejection counts and queue age):

| Operation | Rolling limits per authenticated ideator |
| --- | --- |
| Create idea, public or private combined | 5 attempts / hour and 20 / 24 hours |
| Upload extraction or transcription (`POST /api/intake`) | 10 attempts / hour and 30 / 24 hours |
| Refinement (`POST /api/refine`) | 10 attempts / hour and 30 / 24 hours |
| Author content edits / resubmissions, across ideas | 20 attempts / hour and 60 / 24 hours |

Key counters by the server-verified canonical author identity (GitHub login
normalization or provider-qualified subject), never client-supplied author,
session cookie, IP alone, or display name. Sessions for the same identity share
budgets; separate accounts remain a known evasion risk. No reputation exemption.

After authentication/CSRF checks, reserve quota atomically before parsing an upload,
calling an LLM/STT provider, or writing an idea. Authenticated attempts consume
quota even on malformed input or downstream failure; clients must not retry
blindly. Check both windows together without partially charging a denied request.
Return HTTP 429 with `Retry-After` seconds until all relevant windows allow a
retry and a clear user-facing message; do not create an idea or start background
work. An upload followed by creation spends one unit in each separate budget.

Persist bounded timestamp windows in the local JSON store so restart or session
rotation cannot reset quotas. Prune expired entries and serialize checks with
writes. Initially assume one server process owns a data directory, as with the
current file store; multiple independent replicas would need a shared limiter
before claiming these limits are global. Storage failure should return 503,
not silently admit unmetered expensive work. Keep request/upload size limits;
these per-identity budgets do not solve fleet-wide provider exhaustion or Sybil
attacks. Deployment-level concurrency/spend caps are a separate follow-up.

## One quality signal: actionable detail

Compute a deterministic `actionableDetail` hint from the submitted title/body:
`complete` if the trimmed title is nonempty and the body contains nonempty
`Problem` and `Proposed change` Markdown sections; otherwise `needs_detail`,
with the missing sections named. Ignore case in headings and whitespace-only
section content. Evaluate before optional refinement and again on saved edits.
Show authors a suggested template, but allow submission without it.

This is a writing aid and review-queue hint, not a spam verdict, ranking score,
or measure of ideator worth. It never auto-approves, rejects, changes quotas,
or overrides repository decisions. Templates can be gamed and legitimate ideas
may use other formats; admins must inspect content. Do not send private text to
an additional third-party scoring service or publish per-author quality scores.

## Delivery and verification

Open separate implementation issues for (1) persisted moderation/revision audit
and admin/author UX, (2) eligibility checks across API, MCP, and derived feeds,
(3) persistent rate limiting and retry UX, and (4) the quality hint/template.
Before enforcing moderation, migrate existing ideas to `pending` and arrange an
admin review queue; do not silently grandfather content as trusted. Roll out
with queue capacity and author-facing notice, not an unexplained disappearance.

Each implementation should include tests for unauthorized/stale review decisions,
all state transitions, privacy across direct reads and derived listings, edits
invalidating approval, and quarantine during settlement. Limiter tests should
cover exact rolling-window boundaries, parallel requests, shared sessions,
restart persistence, failure paths, and no provider work after 429. Hint tests
should cover missing/empty sections and non-template submissions. Exercise routes
both at root and behind `DIBS_BASE_PATH`; report aggregate queue age and 429 counts
without recording idea text, credentials, or identity as metric labels.

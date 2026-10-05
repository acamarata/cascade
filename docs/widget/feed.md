# Status widget feed

The contract between the daemon and every widget client (the macOS widget,
the lock-screen snapshot, QA legs). Two parts: a request, `status.widget`,
and a stream, `status.widget_changed`. This file is the canonical copy of
`contract:status-widget-rpc`.

## status.widget

`POST /rpc`, JSON-RPC 2.0, method `status.widget`. Params are `{}` or
`{"scope": {"kind": "...", "id": "..."}}`. With no scope the daemon uses the
global scope (it has one owner and no per-connection session identity).

Result, a `WidgetSnapshot`:

| Field | Type | Meaning |
|---|---|---|
| `rows` | `WidgetRow[]` | one row per provider in `providers.db`, sorted by provider name |
| `attention_count` | int | unacked attention items in the resolved scope |
| `active_jobs_count` | int or null | leased plus running jobs; null only when the daemon has no jobs domain |
| `nodes` | `NodePresenceSummary[]` | `{id, presence, reachable, trust_tier}`; `presence` is `reachable`, `unavailable`, `remote-via-route` or `unknown` |
| `projects` | `[]` | always empty in P1 |
| `generated_at` | RFC 3339 | when this answer was composed |
| `seq` | uint64 | the seq of the last frame published on the stream (0 before any) |

`WidgetRow`:

| Field | Type | Meaning |
|---|---|---|
| `ref` | string | opaque stable handle, `WidgetRef(provider name)` (below) |
| `label` | string | the provider name, PII-scrubbed; `"redacted"` when the name is PII-shaped |
| `kind` | `"profile"` or `"pool"` | P1 always sends `"profile"` (the slot carries no pool signal yet) |
| `five_hour`, `seven_day` | `WidgetWindow` | always present as objects |
| `state` | string | `available`, `constrained`, `exhausted`, `auth-required` or `unknown` |
| `reauth_required` | bool | true exactly when `state` is `auth-required` |
| `updated_at` | RFC 3339 | when the daemon last read this row's source successfully |

`WidgetWindow` is `{utilization_pct: number or null, resets_in: int64
nanoseconds or null}`. A field with no source is `null`, never `0`.

### How a row is built

- **state** is the worst state over the provider's lanes, taken per capacity
  bucket (interactive usage, agent SDK credit, API credit) and then over the
  buckets that have a lane. A bucket with no lane takes no part. `unknown` only
  means the provider has no lane, a lane that is itself `unknown`, or a row whose
  last good read is older than one minute. Severity order, least to most:
  available, unknown, constrained, exhausted, auth-required.
- **windows** come from the bucket that drives `state`. `five_hour.resets_in`
  is the lane's reset estimate minus `generated_at`, or `null` when the estimate
  is absent, zero or already past. The estimate is the one on the lane in the
  worst state, so a stale estimate on a healthier lane is never shown.
  `seven_day.resets_in` is always `null` until per-window reset data exists
  (P1-TOP-13). `utilization_pct` is `null` for every provider until a quota
  source is wired (P1-GWY-04); it is never `0`.
- **updated_at** is the instant of the last successful read of `providers.db`.
  When reads fail the rows are kept, so `updated_at` falls behind
  `generated_at`. After a minute without a good read a row reads `unknown` with
  null windows. A source that has never been read is an error
  (`KindUnavailable`), not an empty list.
- Lane state comes from real call outcomes (a 401 sets `auth-required`, a 429
  sets `exhausted` with `Retry-After` as the reset), written by the provider
  resolver. Nothing here reads a credential.

### ref and reauth

`WidgetRef(name)` is `name` when it matches `^[A-Za-z0-9._-]{1,64}$` and none of
the PII patterns (email, URL, absolute path, hostname, `@handle`), else
`"ref-"` plus the first 12 hex digits of `sha256(name)`. The ref never carries an
address or host into the App Group file or a lock-screen snapshot.

`cascade provider reauth <arg>` accepts a ref. It collects the provider named
`<arg>` and every provider whose `WidgetRef` equals `<arg>`: exactly one
distinct provider is re-authorized; none is `KindNotFound`; more than one
(including a provider literally named `ref-<12 hex>` shadowing another
provider's ref) is `KindConflict` naming each, and nothing is written. No daemon
verb maps a ref to a name; the CLI resolves it over the registry it already
opens, before it selects custody or reads stdin. A name the vault refuses (an
email, which has an `@`) can be shown and resolved but cannot be re-authorized,
because the credential name is `provider.<name>.key`.

### Errors

`KindUnavailable` before the compositor exists, or for a source never read;
`KindInvalidInput` for malformed params. The socket gives HTTP 403 before
dispatch to a non-owner peer, and to any browser-shaped request (an `Origin`
header, with any value, on `/rpc` or `/events`).

## status.widget_changed

`GET /events` with no topic (`?filter=status.widget_changed` is accepted). One
SSE record per frame:

```
id: <resume token>
data: {"seq":N,"kind":"status.widget_changed","source":"status.widget","payload":"<base64 of the WidgetSnapshot JSON>"}

```

The outer `seq` is the bus sequence; the `seq` inside the payload is the
snapshot's own counter and moves by exactly 1 per frame.

A supervised loop runs every 10 seconds (`status-widget-refresh` in
`status.get`). Each tick re-reads `providers.db`, the node records, unacked
attention (global scope) and active jobs. It publishes at most one frame per
tick, and only when the redacted snapshot differs from the last frame, ignoring
`seq`, `generated_at`, every `updated_at` and the `resets_in` countdown (a reset
is compared as the instant it counts down to, so a row with a reset does not
publish every tick). A failed read keeps the last rows and publishes nothing.
The first tick after start publishes the first frame. A push through the
widget's own attention store also goes through the same path, so it publishes
once. A client that wants current state calls `status.widget` on connect and
treats frames as changes after that.

## Fixtures and the seeder

- `internal/daemon/testdata/fixture_status_widget_rpc.json`: a `status.widget`
  request and result over five providers (available, auth-required in a pool,
  exhausted with a 2 hour reset, no lane, email-named), one node, one attention
  item and two active jobs.
- `internal/daemon/testdata/fixture_status_widget_changed_sse.txt`: the raw bytes
  of one frame read from a real unix socket.
- `internal/daemon/testdata/widgetseed`: a test-fixture program that writes
  key-authenticated providers and lanes into a `providers.db` with no
  credential, and flips one lane's state. Provenance for all three is in
  `internal/daemon/testdata/widgetseed/README.md`.

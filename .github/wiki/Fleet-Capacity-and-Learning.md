# Fleet Capacity and Learning

This page documents the fleet-topology layer added by P1-E40-W9-S77-T1
(`internal/fleet/topology`): the five entities the executive-economy work
schedules lanes over, the invariants that keep them consistent, and how
they are derived from the existing provider registry.

## Fleet topology

Five entities, stored inside the existing `config` storage domain (no new
domain is ever added; the closed domain list stays exactly as ratified):

| Entity | Purpose |
|---|---|
| `Account` | One row per provider account: which provider, its billing kind (subscription/api/free/pool), and its role (executive/workforce/specialist). |
| `Credential` | One row per usable credential: which account and quota domain it draws from, a **vault-key reference only** (never a credential value), which runtime profile it authenticates against, and its health. |
| `QuotaDomain` | One row per billing boundary: an API project, a subscription window, or a shared pool, with a billing tier and a quarantine flag. |
| `RuntimeProfile` | One row per (provider, runtime) pair: which CLI/harness runtime reaches this account, its config home, and its optional network endpoint. |
| `Lane` | One row per dispatchable lane: which runtime profile and quota domain it uses, an optional credential, its model, effort, roles, lane class, health, and its immutable offering snapshot. |

### Credential != QuotaDomain != Lane

The three are deliberately distinct rows, never folded into one another:
a credential can be quarantined without quarantining every lane that
shares its quota domain's health independently; a quota domain can hold
more than one credential (an api_project domain with several rotating
keys); and a lane's own health can diverge from both (a lane can be
`auth-required` while its domain is otherwise healthy).

### The credential-domain-profile join invariant

A lane's non-nil `credential_ref` must resolve to a credential whose quota
domain, account, and runtime profile all match the ones the lane itself
reaches, otherwise a call would authenticate against one credential
while charging a different domain's quota. A **nil** `credential_ref` is
legal only for a `subscription_window` domain on a CLI-driven runtime
(`claude-cli`, `codex`, `antigravity`, `opencode`) or for the `ollama`
runtime; every other nil is a violation. The check runs on every store
write and on every reconcile pass, and an offending lane is written with
health `quarantined` rather than silently repaired by guessing a
reference.

### Derived lane identity

A lane's id is never chosen by a caller: it is the first 16 hex
characters of `sha256(runtime_profile_ref | quota_domain_ref |
canonical_id-or-model_id | effort | interaction_class)`. Every writer,
including the reconcile pass, upserts by this derived key, so a lane that
is retired and later reappears (the same runtime/domain/model/effort
combination) keeps its reservation history, scheduler decisions, and
learned statistics rather than starting over under a new id.

### Model identity and the persisted offering snapshot

A lane carries a provider-declared `model_identity{canonical_id, family}`
(parsed only inside a `providers/<vendor>/` discovery pass, never inside
the topology package itself, which holds no vendor or model names at all).
An empty `canonical_id` is treated as the **same model** (fail-closed) for
model-exclusion and family-correlation purposes, never as "unknown,
therefore distinct."

Alongside that, a lane persists an **immutable, versioned**
`offering_snapshot`: modalities, context/output token limits, tools,
supported efforts and interaction classes, the model identity, and a
provider-policy block. A reconcile pass replaces the whole snapshot
atomically (never one field at a time) and bumps its version. Ranking,
health summaries, and learned statistics read only this persisted
snapshot, never a live provider response, so every one of those
computations is reproducible from the store alone.

### Lane classes and base shadow prices

Every lane belongs to one of seventeen closed lane classes (`executive`,
`executive-xhigh`, `critic`, `specialist`, `advisor`, `lead`, `deep`,
`harness-sub`, `worker-dedicated`, `worker-fast`, `pool-premium`,
`pool-cheap`, `api-paid`, `api-free`, `api-batch`, `local`, `unranked`),
each with a ratified base shadow price used by the scheduling economy. A
newly reconciled lane starts as `unranked` at price `1.0` until a later
discovery pass or a personal-config override assigns it a real class.
`executive-overflow` is not a class in this table: it is a *derived*
effective class the scheduler computes at dispatch time, never a stored
value.

### What the registry reconcile produces

The topology store is rebuilt from the existing provider registry
(providers and their lanes) rather than being populated by hand:

- **Account**: one per provider, with billing kind derived from whether
  any of its lanes belong to a pool, whether it authenticates via OAuth
  (subscription), or otherwise (api).
- **QuotaDomain**: one per (account, billing kind): a pool lane maps to
  a shared-pool domain named after the pool, an OAuth account maps to a
  subscription-window domain, and a key-based account maps to an
  api-project domain.
- **RuntimeProfile**: one per (provider, driver) pair, created **before**
  any credential or lane row so the "one profile per lane" invariant
  holds on the very first pass.
- **Credential**: one per registry lane record, copying its vault-key
  reference by name only, never reading the value it names.
- **Lane**: one per registry lane record, health mapped straight across,
  starting `unranked` at base price `1.0` until a discovery pass assigns
  a real class.

The reconcile pass runs inside one transaction and re-validates every
invariant before committing; a violation aborts the whole pass and leaves
the prior rows untouched. Running it twice over unchanged input writes
byte-identical rows and never moves a lane's `discovered_at` timestamp.

## Quota domains and buckets

P1-E40-W9-S77-T3 gives every `QuotaDomain` real capacity numbers: a
`Bucket` per dimension, a closed per-kind dimension vocabulary, a
source-precedence ladder, and the `fleet.quota.snapshot` RPC.

### Bucket fields

A `Bucket` carries a name, a limit (`-1` is the *discover* sentinel,
capacity not yet observed), a remaining fraction, a reset time, a window
(`5h`/`day`/`week`/`month`/`rolling`), a source
(`provider-status`/`cli-observation`/`user-estimate`/`unknown`), a
confidence, and an observed-at time. An out-of-range fraction or
confidence, or an unrecognised window or source, is refused
(`ErrTopologyInvariant`) rather than clamped or defaulted.

Alongside the R-21.26 fields, a bucket carries R-21.96's absolute
accounting: `limit_scope_id`, `capacity_observed` (absolute, `-1` before
the first reconciliation), `committed_since_observation`, `window_id`,
and `version`. `remaining_fraction` becomes a *derived* view once
`capacity_observed` is known (`1 - committed/capacity`), never an
independently written number.

**Unknown is not infinite.** A bucket whose limit is exactly `0` refuses
a new reservation; a bucket whose limit is the discover sentinel (`-1`)
or whose source is `unknown` remains reservable and keeps its lane's
concurrency limits and base shadow price. Neither ever reads as unlimited
capacity anywhere in this package.

### Per-kind dimension names (closed)

| Domain kind | Permitted dimensions |
|---|---|
| `api_project` | `rpm`, `tpm`, `rpd` |
| `subscription_window` | `session_5h`, `weekly_shared`, `weekly_model_fraction`, `monthly` |
| `shared_pool` | `window_5h`, `weekly`, `monthly` |

A dimension name outside its kind's set is `ErrTopologyInvariant`. Two
gauge names, `concurrent_requests` and `enqueued_tokens`, gate batch
eligibility only (`reservable iff gauge + estimate <= limit`) and are
refused by the window map below if fed to it as a pressure input.

### Dimension-to-window map

Mandatory and closed: `rpm`/`tpm` roll over 60 seconds; `rpd` resets
daily; `session_5h`/`window_5h` reset every 5 hours; `weekly_shared`,
`weekly_model_fraction`, and `weekly` reset weekly; `monthly` resets
monthly. An unmapped dimension is refused, so a window length is never
zero or absent and no pressure computation can divide by zero.

### Limit scopes

Every bucket carries a `limit_scope_id`. Sharing the account-level scope
is the fail-closed default; a distinct per-domain scope is granted only
to an `api_project` domain that explicitly proves provider-documented
independence. An unproven claim can never split a real provider limit.

### Observation freshness

Each source has an expiry: `provider-status` 10 minutes,
`cli-observation` 30 minutes, `user-estimate` 24 hours; `unknown` never
expires. The highest-precedence *non-expired* observation wins, so a
stale provider-status reading loses to a live cli-observation. When every
observation has expired, the bucket keeps its concurrency and request
caps but its source becomes `unknown` and its reserve status reports as
unknown rather than as stale full capacity.

### quota_bucket is a read-only observation cache

`quota_bucket` (in the existing `sessions` storage domain) is written only
by observation reconciliation, never by a dispatch or reservation path.
Availability is *derived*: `capacity_observed - committed_since_observation
- sum(every held/committed reservation's estimate on that scope)`. This
package ships no counter decrement and no rollback path -- the
reservation row itself lives in the jobs domain, inserted first, which is
what makes a crash mid-flight safe without an un-decrement step here.

### The reserve barrier

`preserve_weekly_reserve` clamps to `[0.0, 0.60]`; a value outside that
range still returns a usable, clamped value alongside a typed error, so a
misconfigured reserve of `1.0` can never make a lane permanently
ineligible. The barrier binds to exactly one named bucket per domain
kind: `weekly_shared` for `subscription_window`, `weekly` for
`shared_pool`, `rpd` for `api_project`. The R-21.34 executive-role
eligibility caps (`executive_model_fraction_soft`/`_hard`) are a
different quantity entirely and are never a barrier bucket.

### fleet.quota.snapshot

The JSON-RPC 2.0 method `fleet.quota.snapshot` (no params) returns a
snapshot of every quota domain's current buckets. A domain's confidence
is the **minimum** confidence across its dimensions -- an unobserved
dimension contributes `0` -- so a domain is never presented as better
known than its weakest bucket. `internal/fleet/capacity.FleetSnapshot`
embeds this payload additively: an older snapshot with no `quota` key
decodes to the zero value (confidence `0`, no domains) rather than
failing to decode.

## Shadow price and quota pressure

`internal/fleet/economics` prices a lane with the four-term formula
`SP = B x Q x P_mode x R`. Every term is fixed, none is tuned:

- **B**, the base shadow price, is the lane's R-21.30 lane-class price
  (`topology.BaseShadowPrice`), or the executive-overflow price when a
  workforce account's lane is running in its overflow class.
- **Q**, the pressure scalar, is owned entirely by
  `topology.BucketPressure`/`DomainPressure` (this page's dimension-window
  map section, above) -- `internal/fleet/economics` consumes it unchanged
  and never recomputes it. An unknown-source bucket's pressure is not a
  flat `1.0`: it rises with the scope's `ewma_429` toward a clamp of 25,
  so an all-unknown lane under sustained rate-limiting prices strictly
  above the same lane with no 429 history.
- **P_mode**, the mode multiplier, is a plain caller-supplied number --
  the mode-multiplier tables themselves are DATA owned elsewhere and this
  package ships none.
- **R**, the reserve barrier, is `1.0` while the barrier bucket's
  remaining fraction is more than 0.10 above the account's reserve,
  interpolates linearly to `4.0` across that 0.10 band, and holds at
  `4.0` at or below the reserve (`BelowHardReserve`).

Alongside R, `BelowAbsoluteFloor` is a third, independent signal: true
when the remaining fraction falls below half the reserve. It is
mode-independent -- an `incident` override that clears `BelowHardReserve`
never clears it, and only an elevated `--allow-reserve` verb can. The two
booleans are never collapsed into one: a lane between the absolute floor
and the reserve reads `BelowHardReserve=true, BelowAbsoluteFloor=false`.

This reserve barrier is a distinct concept from the account-role
`ReserveState` (abundant/soft/hard) used for the R-21.92 hard-reserve
role ladder, and from a per-request tier policy's own `reserve` flag --
all three sit in the same package family, share the same 0.10 band width
by coincidence of the underlying rule, and are never merged into one
type.

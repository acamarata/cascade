# Audit Log

The `internal/audit` package is the append-only, hash-chained security trail for every
policy decision, routing decision, approval-queue transition, config reload, and elevation
attempt in Cascade. This document describes its public contract.

---

## Event schema

An `Event` is what a caller passes to `Append`. Every field is a plain string or a JSON
body, there is no field for a secret value or a raw parameter blob.

| Field | Type | Notes |
|---|---|---|
| `Kind` | `Kind` | One of the eleven ratified kinds. Required. |
| `Actor` | `string` | Who or what took the action, a user id, plugin id, "scheduler". Required. |
| `Action` | `string` | What was attempted, in the vocabulary of the subsystem. Required. |
| `ParamsHash` | `string` | Digest of action parameters. Use `audit.HashParams(params)`. |
| `RiskLevel` | `string` | Risk classification as a plain string from the deciding subsystem. |
| `Verdict` | `string` | Decision as a plain string ("allow", "deny", …). |
| `Explain` | `json.RawMessage` | JSON rationale, profile, overlays, reason. Must be valid JSON or empty. |
| `PolicySnapshot` | `json.RawMessage` | Resolved policy at decision time. Must be valid JSON or empty. |
| `Outcome` | `string` | What actually happened after the decision. |

`HashParams` is the one supported way to record "these were the parameters" without putting
the parameters in the record. Pass the raw parameter bytes; it returns a BLAKE3 hex digest.

---

## Event kind enum (closed)

The kind enum is closed at eleven values. Adding a kind requires amending the contract, not
minting a string at a call site.

| Constant | Wire value |
|---|---|
| `KindPolicyDecide` | `policy.decide` |
| `KindPolicyRoute` | `policy.route` |
| `KindApprovalEnqueue` | `approval.enqueue` |
| `KindApprovalDedup` | `approval.dedup` |
| `KindApprovalExpire` | `approval.expire` |
| `KindConfigReload` | `config.reload` |
| `KindApprovalGrant` | `approval.grant` |
| `KindApprovalDeny` | `approval.deny` |
| `KindElevationAttempt` | `elevation.attempt` |
| `KindElevationGrant` | `elevation.grant` |
| `KindElevationDeny` | `elevation.deny` |

---

## Append-only guarantee

`Append` is the only write path. There is no update method and no delete method.

At the storage layer, the write is a `CompareAndSwap` against a nil prior value, a
conditional create. A second writer racing for the same sequence number gets
`ErrAlreadyRecorded`, not a silent overwrite.

Every record carries two chain links:

- **Per-record hash**: a BLAKE3 digest of every field in the record, including the previous
  record's hash. Changing any field invalidates the record's own hash.
- **Prev-hash link**: each record embeds the hash of the record before it. Replacing a record
  with one whose own hash is correct still breaks the chain, because the next record's
  `PrevHash` no longer matches.

A full-walk (`Query`, `Verify`) enforces:

1. Gapless sequence numbers: a missing record is a gap.
2. Chain consistency: `rec[n].PrevHash == rec[n-1].Hash`.
3. Tail match: the stored head pointer names the same sequence number and hash as the last
   record the walk found.

Any violation returns an error with `cascade.KindIntegrity` and no records are returned
from the failed walk.

---

## Ordering and determinism

Records are ordered by the log's own sequence number, not by their timestamp and not by
their id. Two records appended within a single clock tick therefore still have exactly one
defined order, and both are readable. Reads return records oldest first.

The clock is injected. Nothing in the package reads the wall clock, so a test with a frozen
clock sees the same ordering the production build does.

---

## Query filter

`Filter` controls what `Query` returns. A nil slice means "no constraint on this field".

| Field | Type | Default | Notes |
|---|---|---|---|
| `Since` | `time.Time` | zero (no lower bound) | Inclusive lower bound on the record timestamp. |
| `Until` | `time.Time` | zero (no upper bound) | Exclusive upper bound on the record timestamp. |
| `Kinds` | `[]Kind` | nil (all kinds) | If non-nil, admits only these kinds. Every entry must be one of the eleven. |
| `Actors` | `[]string` | nil (all actors) | If non-nil, admits only these actors. Exact match. |
| `Verdicts` | `[]string` | nil (all verdicts) | If non-nil, admits only these verdicts. Exact match. |
| `Limit` | `int` | 100 | Records per page. Maximum 1000. |
| `Cursor` | `string` | "" | Opaque cursor from a previous `Page.NextCursor`. |

**Fail-closed.** A filter that cannot be honoured exactly as written is REFUSED with
`ErrInvalidFilter` (`cascade.KindInvalidInput`) and returns no records. It never widens into
"match everything", which is how a malformed filter would otherwise disclose records the
caller never asked to see. Refused: a kind outside the eleven, a present-but-empty
constraint list (`[]Kind{}` is not a wildcard, omit the field instead), an empty entry
inside a list, an upper bound that is not after the lower bound, a negative or out-of-range
limit, and a malformed cursor.

### Parsing a filter from the command line

`ParseFilter` builds a `Filter` from `key=value` tokens. The key set is closed:
`kind`, `actor`, `verdict`, `since`, `until`, `limit`, `cursor`. Repeating `kind`, `actor`,
or `verdict` extends that list. `since` and `until` are RFC3339. An unrecognised key is
refused rather than ignored, because ignoring it would silently drop a constraint the caller
asked for and answer with a wider result set than was requested.

---

## Verify

```go
err := log.Verify(ctx)
```

Walks the whole log and returns the first integrity failure, or nil when every record
verifies, the chain is unbroken, no sequence number is missing, and the tail matches the
head pointer.

Scope limit, stated plainly: the chain and the head pointer detect a record altered,
replaced, or removed underneath the API. They do not defend against an attacker who can
rewrite the entire log, head pointer included, in one pass. That needs an anchor outside
the store (a signature or an external witness), which this build does not have.

---

## Cursor pagination

`Query` returns a `Page`:

```go
type Page struct {
    Records    []Record
    NextCursor string
}
```

When `NextCursor` is non-empty, pass it back as `Filter.Cursor` to fetch the next page.
Cursors are sequence-number-keyed: adding new records between pages does not cause records
to appear twice or be skipped.

---

## Explain

```go
explanation, err := log.Explain(ctx, recordID)
```

Returns the full `Explanation` for one record id. If no record carries that id, the error
wraps `ErrNoSuchRecord` with `cascade.KindNotFound`. An empty id returns `ErrInvalidEvent`.

```go
type Explanation struct {
    Record         Record
    Explain        json.RawMessage
    PolicySnapshot json.RawMessage
}
```

Every explain call verifies the record's content hash before returning it.

---

## Writer interface

```go
type Writer interface {
    Append(ctx context.Context, event Event) (Record, error)
}
```

Policy, the approval queue, config reload, and the elevation middleware take a `Writer`, not
a `*Log`. This keeps the dependency direction one-way: subsystem → audit.

---

## Error taxonomy

| Sentinel | Kind | When |
|---|---|---|
| `ErrTampered` | `KindIntegrity` | A record's hash is wrong, the chain is broken, or the tail pointer disagrees with the log. |
| `ErrAlreadyRecorded` | `KindConflict` | Two writers raced for the same sequence number. |
| `ErrNoSuchRecord` | `KindNotFound` | `Explain` called with an id no record carries. |
| `ErrUnknownKind` | `KindInvalidInput` | `Append` called with a kind outside the eleven. |
| `ErrInvalidEvent` | `KindInvalidInput` | A required field is missing, a field exceeds 512 bytes, or a JSON field is not valid JSON. |
| `ErrInvalidFilter` | `KindInvalidInput` | A filter the query engine cannot honour exactly: an unknown kind or filter field, a present-but-empty list, an inverted time window, an out-of-range limit, or a malformed cursor. |
| `ErrStoreUnavailable` | `KindUnavailable` | The backing store failed. |

All errors satisfy `errors.Is` by `cascade.Kind`, not pointer identity.

---

## What records do not contain

- Secret values, plaintext credentials, or raw parameter blobs. Use `HashParams`.
- Free-form text over 512 bytes in any single field.
- Control characters (`U+0000`–`U+001F`, `U+007F`) in any field.
- Non-JSON bodies in `Explain` or `PolicySnapshot`.

These are refused at `Append` time, not silently stored.

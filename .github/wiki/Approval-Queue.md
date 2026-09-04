# Approval Queue

The approval queue is where a person says yes. The policy engine sends it
every action the layers decided to ASK about; it groups those actions into
prompts, records the answers, and lets an approved action run exactly once.

Two properties govern the design. An approval binds to exactly what was
shown, and no approval outlives its window or its scope. Everything below is
one of those two, made mechanical.

## The shape of one approval

| Stage | What happens |
|---|---|
| `Enqueue` | An ask-tier action is admitted, hashed, and filed in the open batch. |
| `GetPending` | The three-field payload a surface renders. |
| `Decide` | The person's answer, bound to the exact description and rung shown. |
| `ConsumeToken` | One redemption, against the action about to run. |
| `Cancel` | Withdraws a pending or approved entry; terminal. |
| `Expire` | The rate-limited sweep that retires whatever lapsed. |

## What may be queued

Only L2 and L3. L0 and L1 need no approval, so queueing one would ask a
question with no meaning. L4 is refused with `ErrLocalOnly`, and so is any
elevation-class verb from the 06-FORGE-SPEC §5.14 table and anything the
deny-list matches: those are authorized on the machine they run on, in the
same turn, via the elevation helper. They never acquire a request id, so
they can never appear in a remote-approvable prompt.

The verb classification is not a second copy of the §5.14 list. It calls
`internal/rpc.IsElevated`, which is the already-canonical table, verified
against the spec by that package's own tests.

## Batching

Two numbers, both from the `[policy]` config block (08-INIT-CONFIG-SPEC §3,
ratified by R-14.29):

- `approval_batch_window_s` — how long the queue keeps collecting before it
  presents a prompt. Default 10.
- `approval_batch_cap` — the largest number of entries one batch holds.
  Default 20.

A batch closes at the cap or when the window elapses, whichever comes first.
The whole subsystem is clock-driven rather than timer-driven, so a seeded
clock reproduces every flush exactly.

**A batch carries no verdict, no rung and no approval of its own.** It is a
presentation grouping. Each member keeps its own rung and its own token, and
decisions are recorded one member at a time — which is what stops a batch
from laundering risk. A prompt shown as L2 cannot carry an L3 member through
on the L2 answer, and a denial recorded on one member leaves its neighbours
exactly where they were.

## Deduplication

An entry is keyed on `(action_hash, params_hash)`. A duplicate arriving
while the batch is still open returns the EXISTING request id, mints no
second token, and lands an `approval.dedup` audit row. The same action
arriving after that batch closed is a new question: the person has not been
asked about it yet.

The key includes the parameters on purpose. The same verb with different
arguments is a different question.

## Binding: an approval is for one action, not a category

The queue hashes the action text and the parameter bytes itself. It does not
believe a digest a caller hands it — a supplied `ParamsHash` is verified
against the bytes and refused on a mismatch, because a believed digest would
let a caller bind an approval to something other than what it is about to
run.

At redemption the action about to run is re-hashed and compared. A request
that changed between display and execution fails the comparison and is
refused with `ErrApprovalMismatch`. There is no approval for a *kind* of
action and none for a *session*.

The same binding applies to the answer itself: `Decide` refuses an approval
whose presented description differs from the entry's, and refuses one
recorded at a lower rung than the entry carries. The rung lives inside the
summary string rather than beside it, so the text a person reads is the same
text the decision is checked against.

## Expiry, cancellation, revocation

A token's `exp` is capped at five minutes (§5.24). The ceiling is not
configurable and is enforced by the queue, not trusted from the minter.

Three things invalidate an approval, whether it is still pending or has
already been granted:

1. **Expiry.** Pruned on every `GetPending` and on a sweep that runs at most
   once a minute. Expiry is terminal — the queue does NOT re-ask.
2. **Cancellation.** A withdrawn entry can never be redeemed, whatever its
   `exp` says.
3. **A revoked grant underneath.** If the entry was admitted while a
   standing grant covered it, redemption re-reads the grant store. Revoking
   that grant refuses the redemption. This is why the grant model has no
   cache: an approval must not survive the permission it stood on.

## Single use, across restarts

`ConsumeToken` claims the token's nonce in a durable ledger in the `audit`
storage domain, through the B-layer store abstraction. The claim is a
conditional create, so two concurrent redemptions of one token cannot both
succeed. A replayed nonce is refused with `ErrTokenReplayed` regardless of
`exp` and regardless of whether the daemon restarted in between.

A stored row that cannot be decoded is still a spent nonce. Re-opening a
nonce because its record was damaged is precisely the state an attacker
would manufacture.

Pending entries live in daemon memory and do not survive a restart, which is
correct: an approval nobody is waiting on is an approval nobody gave. The
ledger is the part that must survive everything.

## The remote-approvable payload

`GetPending` returns three fields and nothing else, to any caller, bridge
paths included:

```json
{ "request_id": "...", "action_summary": "...", "exp": "..." }
```

The token, the nonce and the action hash stay in daemon memory. Bridges
carry the request id alone (§5.24). A dedicated test asserts this
structurally, over the type, because a value-level check would pass the day
somebody adds a field and leaves it empty in that one case. Narrowing the
bridge-visible set further to the §5.24 L1–L2 remote-approvable bound is the
bridge's own job (W/S-48.T4); this queue exposes no wider payload.

## Wiring

The engine holds the queue behind the `ApprovalQueue` interface:

```go
engine := policy.NewEngine(registry, grants, autonomy).
    WithApprovalQueue(queue)
```

An engine with no queue attached evaluates identically; it simply files
nothing and the outcome carries no request id. When a queue IS attached and
it refuses to take the action, the outcome is downgraded from ask to DENY —
an action nobody can ever approve must not be presented as merely awaiting
approval.

## Refusals

Every one of these denies. None of them defaults to approved.

| Sentinel | Meaning |
|---|---|
| `ErrLocalOnly` | Elevation-class verb, deny-listed action, or L4. |
| `ErrNotAskTier` | L0/L1 — nothing to ask about. |
| `ErrUnknownRequest` | No such request id. |
| `ErrTokenExpired` | Past `exp`, on a decision or a redemption. |
| `ErrTokenReplayed` | The nonce is already in the ledger. |
| `ErrApprovalMismatch` | The action, parameters, nonce or description changed. |
| `ErrApprovalRungMismatch` | Approved at a lower rung than the entry carries. |
| `ErrApprovalNotApproved` | Pending or denied; only an approved entry redeems. |
| `ErrApprovalDecided` | A repeated decision. |
| `ErrApprovalCanceled` | Withdrawn. |
| `ErrInvalidParamsHash` | A supplied digest that does not describe its bytes. |
| `ErrApprovalQueueFull` | The pending set is at its ceiling. |

They are `*ApprovalError` values rather than bare taxonomy errors because
several share a taxonomy `Kind`, and `cascade.Error` compares kinds alone: a
caller that cannot tell "expired" from "the action changed" cannot tell the
user what happened.

## Audit

The queue writes `approval.enqueue`, `approval.dedup`, `approval.expire`,
`approval.grant` and `approval.deny` rows through the injected recorder
(`*audit.Log` satisfies it directly). A recorder failure never denies an
action: the decision was already made, and letting the log deny actions
would make the audit sink a policy layer.

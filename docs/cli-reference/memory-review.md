# `cascade memory review`

Review the candidate memories the promotion ladder has not promoted, and
act on exactly one of them.

Promotion at the threshold is mechanical: three references across two
distinct sessions, no model asked and no one prompted. This command is the
human lane beside it. See
[`docs/memory-architecture.md`](../memory-architecture.md) for the model.

## Listing

```
$ cascade memory review
review queue as of 2026-09-04T12:00:00Z
promotion is mechanical at 3 reference(s) across 2 session(s); nothing below that has been promoted

PENDING - below the threshold, awaiting your decision (not recommendations)
  ADDRESS        KIND     REFS  SESSIONS  SNOOZED UNTIL
  project/below  project  1     1         -

PROMOTED - already written to the store; --revert takes one back
  ADDRESS        KIND  REFS  SESSIONS  PROMOTED AT
  user/standing  user  3     2         2026-09-03T12:00:00Z
```

The header states the instant the queue was read and the thresholds the
counts are being judged against, because every row below is a claim
relative to those two.

Listing writes nothing. Viewing the queue cannot promote, retire or hide
anything.

Anything the two tables do not show is still accounted for underneath: how
many pending candidates a live defer is hiding, which candidates have
crossed the threshold and belong to the mechanical lane, and the address of
any candidate record that could not be read.

## Acting

```
$ cascade memory review project/below --auto-approve
promoted project/below ahead of the threshold; it is now a durable record (promoted). Use --revert on it to take that back.
```

An action needs an address. `cascade memory review --auto-approve` with no
address is refused: there is no bulk mode.

| Flag | Action |
|---|---|
| `--auto-approve` | Promote the addressed candidate now, ahead of the threshold. |
| `--auto-skip` | Leave it as it is. Recorded, changes nothing. |
| `--defer-days N` | Hide it from the queue for N days (default 7, maximum 365). |
| `--revert` | Take back its promotion. |
| `--section pending\|promoted` | Listing only: restrict to one section. |

Naming two actions at once is a refusal, not a precedence rule.

## Non-interactive use

Nothing here prompts, so `CASCADE_NO_INPUT` changes nothing. For a caller
that cannot pass flags, `CASCADE_MEMORY_REVIEW_ACTION` selects the action:

```sh
CASCADE_MEMORY_REVIEW_ACTION=approve cascade memory review project/below
```

Flags win over the variable. A value outside `approve`, `skip`, `defer` and
`revert` is refused rather than ignored, and the variable still needs an
address: it cannot act on the whole queue.

## `--json`

`--json` emits the standard versioned envelope. The payload is the RPC
result's own shape, so the table and the JSON cannot drift apart:

```json
{
  "version": 1,
  "ok": true,
  "data": {
    "at": "2026-09-04T12:00:00Z",
    "min_ref_count": 3,
    "min_sessions": 2,
    "pending": [
      {"id": "project/below", "kind": "project", "ref_count": 1, "sessions": 1, "status": "pending"}
    ],
    "promoted": [],
    "snoozed": 0
  }
}
```

## Exit codes

Standard taxonomy codes: invalid input for an unknown action, an unusable
address or an out-of-range defer window; not found for an address with no
candidate; conflict for an action the candidate's status does not admit
(approving one that is already promoted, reverting one that never was,
deferring one that is not pending).

## RPC

The verb calls `memory.review.list` and `memory.review.act` on the daemon.
`memory.review.list` is a pure read.

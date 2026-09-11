# Fleet CLI

The `cascade fleet` command group inspects the local fleet: harness
sessions (`fleet sessions`) and an entity's durable journal (`fleet
journal show|replay`).

> This page currently documents `fleet journal show|replay` (P1-E13-W3-
> S27-T4). `fleet sessions` ships in the same command group; see its own
> `--help` output and `cmd/cascade/fleet.go`/`fleet_watch.go` for its
> flags until this page is extended to cover it.

## `cascade fleet journal show <entity>`

Reads `<entity>`'s journal entries from the daemon, in sequence order.

| Flag | Meaning |
|---|---|
| `--after <seq>` | Only entries with a sequence number greater than `<seq>`. Omit to read from the start. A value past the entity's current head is refused (typed error), not silently treated as "no entries". A negative value is refused before the request ever reaches the daemon. |
| `--limit <n>` | Cap the number of entries returned. The daemon clamps any value above **500** down to 500 — this is never an error to the caller, just a silent cap. Omit for no client-requested limit (still subject to the 500 clamp only if you ask for more). |
| `--json` (global flag) | Emit the [versioned `--json` envelope](cli-output-contract) instead of a table. |

**Output formats:**

- **TTY table** (default on an interactive terminal): columns `SEQ`,
  `KIND`, `OPERATION_ID`, `TIMESTAMP`, `PAYLOAD`.
- **`--json`**: the standard envelope (`version`, `ok`, `data`), where
  `data` is an array of `{seq, kind, operation_id, timestamp, payload}`
  objects — the same fields as the table, one JSON object per entry.

**`payload` is always redacted** before either output format renders it:
every entry's payload passes through this repo's existing §D-31 secret
detector (`internal/doctor.RedactText`) first. A secret-shaped value
never reaches the terminal or the JSON output, table or `--json` alike.

**Error codes:**

| Condition | Result |
|---|---|
| Entity has never had anything appended to its journal | Typed `not-found` error — never an empty list. |
| Entity is known but has no entries past the cursor | Exit 0, empty list — not an error. |
| `--after` names a sequence past the entity's current head | Typed `invalid-input` error. |
| `--after` is negative | Typed `invalid-input` error, caught before the daemon is ever dialed. |
| No daemon reachable | Typed `unavailable` error suggesting `cascade daemon run` — the journal has no offline/embedded fallback; it is a daemon-owned durable log. |

## `cascade fleet journal replay <entity>`

Re-emits `<entity>`'s journal entries, in the same sequence order `show`
would return. **Replay never re-executes anything**: it reconstructs and
displays what already happened (escalations, notifications, anything the
journal recorded) — it never re-runs an escalation, re-sends a
notification, or writes anything back to the journal it reads. Calling it
twice in a row returns the identical result both times.

| Flag | Meaning |
|---|---|
| `--from <seq>` | Same semantics as `show`'s `--after`. |
| `--stream` | Render one NDJSON line per entry instead of a single batched result. **This is not a live subscription** — see the note below. |

> **`--stream` is not a live event feed.** Journal replay is a historical
> read of entries that already happened; there is nothing "live" for a
> future event to report that a second `replay` call would not already
> see. `--stream` renders the same batched `fleet.journal_replay`
> response as one NDJSON line per entry (the same wire format `fleet
> sessions --watch`'s non-TTY path uses) rather than opening a live event
> stream. If a future ticket adds a genuine live journal-change
> subscription, it will be a distinct, separately documented capability.

## Hidden alias

`cascade journal show|replay <entity>` is a hidden top-level alias for
`cascade fleet journal show|replay <entity>` (it does not appear in
`--help`, but resolves identically — it is built from the exact same
command constructor, not a second implementation).

## Examples

```console
$ cascade fleet journal show task-42
SEQ  KIND    OPERATION_ID  TIMESTAMP                     PAYLOAD
1    intent  op-1          2026-09-11T00:00:00Z           {"step":"start"}
2    ack     op-1          2026-09-11T00:00:01Z           {"step":"start"}

$ cascade fleet journal show task-42 --after 1 --json
{
  "version": 1,
  "ok": true,
  "data": [
    {"seq": 2, "kind": "ack", "operation_id": "op-1", "timestamp": "2026-09-11T00:00:01Z", "payload": "{\"step\":\"start\"}"}
  ]
}

$ cascade journal replay task-42
SEQ  KIND    OPERATION_ID  TIMESTAMP                     PAYLOAD
1    intent  op-1          2026-09-11T00:00:00Z           {"step":"start"}
2    ack     op-1          2026-09-11T00:00:01Z           {"step":"start"}
```

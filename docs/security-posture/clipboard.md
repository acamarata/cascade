# Clipboard delivery: `vault get --clipboard`

`cascade vault get NAME --clipboard` puts a secret on the system clipboard
instead of printing it, and takes it off again after a short time. This page
says how the value is delivered, how long it stays, how a missed clear is
recovered, and when the command refuses. The code is
`cmd/cascade/vault_clipboard.go` and `internal/secrets/clipboard*.go`.

## Delivery

The command runs these steps in order. Any failure stops it.

1. Read `[secrets].clipboard_ttl` through `runtime.SecretsClipboardTTL`, the
   guarded-family accessor, and check its range. A bad value refuses here,
   before any prompt, audit record or vault read.
2. Open the process audit log and its effect log.
3. Record the `vault.access` row, then pass the elevation gate (local
   presence, P1-SEC-08). A refusal reads nothing.
4. Mint an in-process approval: a fresh Ed25519 key signs one record
   (`vault.get.clipboard`, sha256 of NAME, requester `cli`, sensitivity
   `secret`). The key is zeroed when the command returns and never stored.
5. The writer verifies that approval. A random token or a record signed by
   any other key refuses with `ELEVATION_REQUIRED`, and the refusal is
   audited.
6. The writer probes for the clipboard tool, then writes the intent effect
   record, then the pending-clear row, then the value, then the confirm.
7. The command blocks until the clear at the TTL has run and been confirmed.
   Ctrl-C (SIGINT), SIGTERM and SIGHUP (the terminal closing) clear at once.

The value never reaches stdout, stderr, an audit record, a log line or a
process argument. pbcopy and xclip read it from stdin.

The clipboard records go to the same audit log the daemon uses. When the
daemon is running, the CLI sends each record through the owner-only
`audit.clipboard_effect` verb. That verb accepts only clipboard intents,
their settlements and the refusal row. With no daemon, each record opens the
profile store, writes, and closes it again. The store is never held for the
TTL, so the elevation gate's own rows and other commands are not locked out.

## TTL

| Setting | Value |
|---|---|
| Default (key absent) | 30s |
| Allowed range | 5s to 10m |
| Outside the range, or not a duration | refused, `INVALID_INPUT`, naming `secrets.clipboard_ttl` |

A raise of the TTL is a config loosening (P1-SEC-36). The daemonless CLI
reads the on-disk value through the same accessor; the 10m ceiling bounds
what any edit can reach.

## The pending clear and the re-arm

Each write leaves a row in `<data_dir>/secrets/pending-clears.json`
(directory 0700, file 0600, written atomically). The row carries a random
reference, the platform and two timestamps. It never carries the value, a
hash of it or its length.

- The row is written before the value reaches the clipboard, and removed only
  after a clear has run and its confirm record is written.
- A failed clear keeps the row and records the clear as unknown-outcome.
- When the daemon starts, it reads every row: a due row is cleared now, a row
  not yet due is scheduled for the time left. A scheduled clear that fails is
  retried three times a minute apart; after that the row waits for the next
  start. A daemon that stops cancels its scheduled clears; their rows wait
  for the next start too. The daemon also marks every clipboard intent left open by a crash as
  unknown-outcome. It writes all of this through its one audit log.
- A clear always runs, even when its intent could not be recorded. Taking
  the secret off the clipboard matters more than the record, so the row is
  kept and the next re-arm repeats the clear under a record.

So a CLI killed with `kill -9` mid-TTL leaves the secret on the clipboard
only until the daemon next runs.

Put reads its row back before the value is written. Calls in one process are
serialised; two processes writing the file at the same instant can still race,
and the read-back turns a lost row into a refused write.

## Refusals

| Case | Kind | What happened |
|---|---|---|
| Bad `clipboard_ttl` | `INVALID_INPUT` | nothing (no prompt, no read) |
| Gate refused (no presence, `CASCADE_NO_INPUT=1`) | `ELEVATION_REQUIRED` | access row and deny row only |
| Approval does not verify | `ELEVATION_REQUIRED` | refusal row only; clipboard untouched |
| No clipboard tool (pbcopy or xclip not found) | `UNSUPPORTED` | no effect record, no row, clipboard untouched |
| Windows | `UNSUPPORTED` | tier-2: no clipboard delivery; use `vault get` |
| Audit log or effect log unavailable | `UNAVAILABLE` | nothing written |
| Pending-clears file corrupt | `INTEGRITY` | nothing written; inspect or remove the file |
| Tool present but failing | `UNAVAILABLE` | an unknown-outcome record and an immediate clear |

Wayland (`wl-copy`) is not supported yet (DEF-P2-wayland-clipboard).

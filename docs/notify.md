# Notifications: the daemon runtime and addressed read-back

`internal/notify` routes notifications to subscribers and keeps an in-memory
Inbox of everything it has processed. The daemon builds exactly one
`notify.Service` and runs it as two supervised loops.

## Runtime

The `notify` daemon registration (`cmd/cascade/wire_notify.go`) runs once per
daemon start. It calls `notify.NewService(notify.DefaultConfig(), ...)` once,
wraps the result in `daemon.NotifyRuntime`, and publishes it on the wiring as
`Deps.Notify` for every later registration.

| Subsystem | Loop |
|---|---|
| `notify.dispatcher` | Drains the four priority queues every 250 ms and fans each item out to eligible subscribers. |
| `notify.router` | Reads the `notify` bus namespace from its head and enqueues each notification event. |

Both loops start through `Manifest.GoSupervised` with the daemon run context.
Daemon stop cancels that context and joins both loops before the bus and the
store close. A panic in either loop is recovered and recorded as a failed
subsystem with a `panic:` detail; the other loop keeps running.

## Addressed notifications

An `Addressed` notification names exactly one target:

| Field | Reaches |
|---|---|
| `TargetSession` | The session with that exact ID, through subscriber fan-out and the session Inbox. |
| `TargetPrincipal` | One caller principal ID, or `owner-cli` for the owner CLI, through the addressed read-back only. |

`Notifier.Deliver` refuses an Addressed item that names both targets or
neither, and any other class that names a principal, with `KindInvalidInput`.
A principal-addressed item is never fanned out to a session subscriber and
never listed, read, acknowledged or counted through the session Inbox,
whatever its visibility: to a session it is an unknown id.

`Kind` is an open vocabulary (for example `delegation.result`). Routing never
reads it; an unknown value is carried unchanged and readers that do not know
it ignore it. Only `Notifier.Deliver` producers set it: the bus
notification payload carries no kind, so a bus-routed item has an empty `Kind`.

The Inbox stores items by producer ID. A later item that reuses an ID updates
the stored item only when it has the same class and targets, so a reused ID
can never re-address an item to another caller.

## Read-back

`Service.ReadAddressed(ctx, correlationID)` and
`Service.ReadAddressedByID(ctx, id)` answer for the verified caller bound to
the request context (`principal.CallerFrom`), never for an ID taken from the
request.

- No caller, the principal-less owner view, a principal with an empty ID, or a
  principal whose ID is `owner-cli` is refused `KindPermissionDenied`.
- A principal reads items whose `TargetPrincipal` is its ID, plus items whose
  `TargetSession` is the session it is bound to. It never reads an `owner-cli`
  item.
- The owner CLI reads only `owner-cli` items.
- `ReadAddressed` returns the caller's items with that correlation ID, oldest
  first, or an empty list. `ReadAddressedByID` returns `KindPermissionDenied`
  for another caller's item and `KindNotFound` for an unknown or expired one.

An item reaches the Inbox when the dispatcher drains it, so a read issued
before that drain returns nothing. The Inbox is in memory only: it does not
survive a daemon restart.

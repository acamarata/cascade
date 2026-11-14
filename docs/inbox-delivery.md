# Inbox delivery

## Session delivery

The daemon builds one `notify.Service`. The `inbox-delivery` registration
(`cmd/cascade/wire_inbox_delivery.go`) subscribes every live harness session
to that service's registry, so an item addressed to a session reaches only
that session's hydration queue.

- **Start.** Every record in the fleet sessions store whose state is not
  closed is subscribed. The id always comes from the store, never from the
  environment, and an id outside the registration rule is skipped.
- **Follow.** A supervised loop reads the `fleet.sessions` topic. An active
  upsert subscribes the session once (idempotent by id). A closed session is
  unsubscribed before it leaves the subscribed list. A lost subscription is
  recorded as a failed `inbox.delivery` entry.
- **Stop.** The loop runs on the daemon run context. When that context
  ends, the loop unsubscribes every session and exits, so daemon stop leaves
  no live subscription.
- **Global-critical items.** A global-critical notification raises one
  attention item (no body, id-keyed, once per notification id). The item is
  also listed by `cascade inbox`.
- **Push.** The channel push path is off. The manifest records `inbox.push`
  as skipped with the reason "flag-off: no server-observable delivery
  signal".

# Inbox send authorization

Every Addressed or Scoped notification passes the daemon's send authorizer
before it reaches a queue, on both `Notifier.Deliver` and
`Notifier.DeliverNow`. The `inbox-send-auth` registration
(`cmd/cascade/wire_inbox_send_auth.go`, phase supervised, order 8) installs
the authorizer once with `Notifier.SetSendAuthorizer`. A second call returns
`KindConflict`, so no later registration can replace or loosen it.

## Capabilities

| Capability               | Default class          | Checked when                                   |
| ------------------------ | ---------------------- | ---------------------------------------------- |
| `inbox.send`             | local-dev              | every Addressed or Scoped send, on the origin scope |
| `inbox.cross_scope_send` | external-side-effect (ask) | `TargetScope` is set and differs from `OriginScope` |

Both capabilities are added to the daemon's one capability registry and
evaluated through the one policy engine. The subject is the caller principal
in the request context, or the daemon's own service subject (`daemon-notify`)
when a send carries no principal.

## Outcomes

- Only an allow verdict lets a delivery proceed.
- An ask verdict files its approval through the policy approval queue and the
  send is refused with `KindPermissionDenied`. The send is not held or retried;
  the sender sends again once a grant exists.
- A deny verdict refuses with `KindPermissionDenied`.
- A policy engine error refuses the send. It never falls through to allow.

When no authorizer is installed (a daemon without a policy engine, or a
test-built wiring), the Notifier itself refuses a cross-scope send with
`KindUnavailable` ("no send authorizer"). Same-scope sends proceed.

Global-critical notifications are not policy-evaluated; the Notifier accepts
them only from the router's closed `GlobalCriticalSources()` list and refuses
any other source with `KindInvalidInput`.

## Not in scope

P1 adds no send RPC verb and no MCP send tool. The only senders are in-process
daemon producers.

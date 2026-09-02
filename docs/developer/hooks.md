# Hooks engine (`internal/hooks`)

Status: landed Wave 1 (P1-E03-W1-S05-T1). Ticket contract:
`.claude/planning/p1/phase/epics/E-C/waves/W-1/sprints/S-05/tickets/T-1.yaml`.

The hooks engine dispatches configured actions in response to typed
events on the daemon's event bus (`internal/events`, C/S-04.T3), auditing
every fire outcome. It is **restricted by design at W1** to exactly two
action types: `plugin-call` and `agent-note`. Shell actions are refused —
permanently, at this wave — pending I/S-18.T5's policy-routed risk
ladder (04-PEWS-PLAN-W1-W3.md §Epic C S-05.T1, the plan's own security
ruling).

## Config shape

See `docs/cli-reference/config.md`'s `[hooks]` section for the
`config.toml` schema (`id`, `trigger`, `action_type`, `action_params`;
08-INIT-CONFIG-SPEC.md §3, reload class hot). Decoding config.toml into
`internal/hooks.HookConfig` and calling `Registry.Register` for each
entry is composition-root work — **not yet wired** — this ticket defines
and validates the shape only.

## Package shape

- **`HookConfig`** (`hooks.go`) — the config row: `ID`, `Trigger`,
  `ActionType`, `ActionParams map[string]string`.
- **`Registry`** (`registry.go`) — `Register`/`Deregister`/`List`/
  `MatchTriggers`. Refuses `shell` and any unrecognised `ActionType`
  **before** a hook is stored, with a `cascade.KindPolicyDenied` error
  (see "Error kind" below). If `ID` is omitted, `DeriveHookID` derives a
  deterministic slug from `trigger` + `action_type` + a hash of
  `action_params`, so a hook's identity survives a daemon restart without
  hand-assignment. A hook's `Trigger` may never name the engine's own
  audit event kind (`hooks.fire`) — refused at registration, the first of
  two re-entrancy guards (see "Re-entrancy" below).
- **`Dispatcher`** (`dispatcher.go`) — subscribes to a namespace on an
  `*events.Bus`, matches incoming events against a `Registry` by exact
  trigger string, and dispatches each match's action bounded by a
  configurable timeout. Construct via `NewDispatcher(DispatcherConfig)`;
  every field is required — this package invents no default namespace,
  cursor name, or timeout value.
- **`PluginDispatcher`** / **`NoteWriter`** (`hooks.go`) — the two W1
  action-type seams, injected interfaces only. `PluginDispatcher` is
  wired to C/S-05.T7's plugin registry once that ticket lands;
  `NoteWriter` is wired to the journal/memory domain once G/S-13 ships.
  **Neither has a real production implementation anywhere in this tree
  yet** — stated plainly (Art.1), not left implicit. Only test fakes
  exist, confined to `_test.go`.
- **`HookFire`** / audit (`audit.go`) — every fire attempt (success,
  dispatch error, timeout, panic, or refusal) publishes exactly one
  `HookFire` to the event bus. See "Audit contract" below.

## Bounded execution — the "must not hang" guarantee

`Dispatcher.dispatchHook` bounds every action by `ActionTimeout`, even
when the action ignores `context` cancellation and blocks forever. The
mechanism: the action runs in its own goroutine, sending its outcome on a
**buffered** (capacity 1) channel so that send can never block regardless
of whether anyone is still listening. `dispatchHook` itself waits on a
`select` between that channel and the timeout's `context.Done()`. If the
timeout fires first, `dispatchHook` synthesizes a `ResultTimeout` outcome,
audits it, and returns immediately — the dispatch loop is never blocked
longer than `ActionTimeout` by any single hook, no matter what that
hook's action does. The abandoned goroutine is not (and cannot be)
killed — Go has no such primitive — so a permanently-blocking action
leaks one goroutine per fire; if it later does return, its outcome is
silently discarded (nobody is listening on the channel any more), never
producing a second audit record for the same fire.

## Failure policy

A hook's own failure — dispatch error, timeout, or panic — is **recorded,
never propagated to the triggering event's publisher**. `Dispatcher.Run`
consumes events from its own subscription loop; nothing about a hook
failing changes whether the event that triggered it was "handled" from
the bus's perspective (delivery to this subscription already succeeded
by the time `handleEvent` runs). This is the documented choice, not
silently one of two possible behaviours: a hook is inherently
best-effort infrastructure layered on top of already-delivered events,
and letting one mis-configured or failing hook affect the source event's
delivery to every other consumer would be a far worse failure mode than
recording the failure and moving on.

## Re-entrancy and loops

Two, explicitly bounded, guards:

1. **Direct self-loop**: a hook can never be configured to fire on the
   engine's own audit trail — `Registry.Register` refuses a `HookConfig`
   whose `Trigger` names `EventKindHookFire` ("hooks.fire"), and
   `Dispatcher.handleEvent` independently skips any incoming event of
   that kind before even consulting the registry (defense-in-depth,
   mirroring the shell-refusal pattern).
2. **Longer cycles are NOT prevented.** `PluginDispatcher`/`NoteWriter`
   are opaque injected interfaces with no real implementation in this
   ticket — this package has no visibility into what a dispatched action
   ultimately publishes. A cycle across independently-triggered hooks
   (hook A's plugin-call causes some plugin to publish an event matching
   hook B's trigger, whose action causes an event matching A's trigger
   again) is possible and unbounded by anything in this package today.
   Call-depth tracking or a per-event hook-fire budget is unimplemented —
   stated as an explicit gap (Art.1), not silently assumed away. Whichever
   ticket wires a real `PluginDispatcher` should read this section before
   assuming the engine bounds fan-out on its behalf.

## Idempotency

`internal/events`' delivery guarantee is **at-least-once**: a crash
between an event landing in the bus's store and its subscription cursor
committing redelivers that event on the next `Subscribe` with the same
cursor name. The hooks engine inherits this unmodified — it performs **no
de-duplication** of its own. Consequence, stated plainly: a hook's action
**may run twice** for the same logical event. Whether that is safe is the
injected implementation's responsibility, not this package's:

- A naturally idempotent action (e.g. "ensure a note with this content
  exists") is safe as-is.
- A non-idempotent action (e.g. "increment a counter", "send exactly one
  notification") is **not** safe without the implementation adding its
  own guard, e.g. keyed on `hookID` plus the triggering event's `Seq` —
  which `Dispatcher` does not currently thread through to
  `PluginDispatcher`/`NoteWriter`. This is a forward note for whichever
  ticket wires the first concrete implementation, not a defect in this
  ticket.

## Audit contract

Every fire attempt — success, dispatch error, timeout, panic, or
action-type refusal — publishes exactly one `HookFire` event to the
configured audit namespace:

```json
{"hook_id": "...", "trigger": "...", "action_type": "plugin-call",
 "params_hash": "...", "result_code": "success", "err_msg": "",
 "ts": "2026-...Z"}
```

`result_code` is one of `success`, `error`, `panic`, `timeout`, `refused`.
There is exactly one call site that turns a fire outcome into a
publish (`Dispatcher.emitAudit`) — "every fire is audited" is enforced by
construction, not by convention: no code path in `dispatchHook` returns
without going through it.

**Secret handling.** `HookFire` carries `params_hash` (a SHA-256 digest of
the hook's `action_params`), **never the raw params themselves** — that
is a structural guarantee, not a filter that could be bypassed. The one
remaining leak surface is `err_msg`, sourced from whatever error text a
`PluginDispatcher`/`NoteWriter` implementation returns, which this
package does not control the wording of. `audit.go`'s `redactSecrets`
mitigates the one leak vector this package DOES control: every
`action_params` value that matches `internal/runtime.LooksLikeSecret`
(the same heuristic `cascade config set`/`edit` already screens with,
`internal/runtime/config_write_secrets.go`) has every literal occurrence
of it stripped from `err_msg` before publication. This does **not** catch
a secret-shaped value introduced by the downstream implementation from
some source other than this hook's own params, or a secret that does not
match the heuristic's shape — stated plainly, not claimed as complete.

## Error kind: `HOOK_ACTION_NOT_PERMITTED`

Every action-type refusal (`shell`, or any type outside `{plugin-call,
agent-note}`) is raised as a `*cascade.Error` with `Kind ==
cascade.KindPolicyDenied`, carrying the stable string
`HOOK_ACTION_NOT_PERMITTED` in its message. This is a **documented
deviation** from the ticket's literal text, which asks for "error kind
HOOK_ACTION_NOT_PERMITTED from the A-T7 taxonomy": A-T7
(`pkg/cascade`) froze a closed, 14-member `Kind` enumeration
(`pkg/cascade/kinds.go`, T0 ruling R-14.3) with no member of that name,
and amending R-14.3 is a T0 decision, not something a build-time ticket
can do. `cascade.KindPolicyDenied` ("policy evaluation refused the
operation") is the closest existing member and an exact semantic match
for the W1 shell-deferral security ruling; `HOOK_ACTION_NOT_PERMITTED` is
carried as additional stable, greppable data in the error message and in
every `HookFire`'s `result_code`/`err_msg` fields instead.

## Composition-root wiring (forward notes, out of this ticket's scope)

- Decode `config.toml`'s `[[hooks]]` entries into `HookConfig` and call
  `Registry.Register` for each — no such loader exists yet.
- Construct the real `PluginDispatcher` over C/S-05.T7's plugin registry
  (`internal/plugins`), respecting `internal/storage/capability.go`'s
  cross-domain grant model exactly as any other cross-domain caller must —
  this package never bypasses it, since it never touches storage directly;
  whichever ticket wires the concrete `PluginDispatcher` is responsible for
  routing through the real registry rather than around it.
- Construct the real `NoteWriter` once G/S-13's journal/memory domain
  ships.
- Choose and inject the daemon's real `ActionTimeout`, trigger/audit
  namespaces, and cursor name — this package supplies no defaults.

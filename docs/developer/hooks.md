# Hooks engine (`internal/hooks`)

The hooks engine runs configured actions when a matching event lands on
the daemon's event bus (`internal/events`), and records one audit entry
for every attempt. This page describes the engine library. The daemon
composition (constructing the dispatcher, wiring the real plugin
dispatcher, router and rehydration seam) is separate work; until it lands
nothing in the shipped binary calls the engine.

## Config: `[[hooks]]`

```toml
[[hooks]]
id            = "notify-on-gate"        # optional; derived when absent
namespace     = "jobs.gate"             # required: the bus namespace to listen on
trigger       = "job.gate.failed"       # required: exact event kind
action_type   = "plugin-call"           # required: must be runnable on this dispatcher
action_params = { plugin = "mail", tool = "send" }   # optional, strings only
```

`ParseHooksSection(raw, dispatcher.Runnable)` reads the decoded section.
It is all or nothing: one bad entry refuses the whole section with a
`KindInvalidInput` error naming the entry index and key. Refused shapes:

- an unknown key, or a key of the wrong type (including a non-string param
  value);
- an empty `namespace` or `trigger`;
- a `namespace` not matching `^[a-z][a-z0-9._-]{0,63}$`, or equal to the
  audit namespace `hooks`;
- `trigger = "hooks.fire"` (the audit event kind);
- an `action_type` the dispatcher cannot run (`Dispatcher.Runnable`); shell
  is runnable only when the dispatcher was built with a `ShellRunner`;
- a duplicate `id`, explicit or derived.

Error text names indices, keys and types, never a configured value. An
absent id is derived by `DeriveHookID(namespace, trigger, action_type,
params)`, so identity is stable across restarts.

## Runner table

`DispatcherConfig.Runners` maps each runnable action type to an
`ActionRunner`. P1 ships `PluginCallRunner(PluginDispatcher)` and
`AgentNoteRunner(NoteWriter)`; later phases add entries. Each entry needs
a capability in `ActionCapabilities`. Shell never enters the table: it
runs only through `ShellRunner`, which comes paired with
`ShellCapability`. A runner receives a `Fire` (hook, namespace, event seq,
causal chain; the hook's raw params are removed) and the params the
rehydration seam produced.

## Per-fire pipeline

For each hook whose namespace and trigger match an event:

1. **Runnable check.** A type with no runner (an entry smuggled past the
   parser, or shell without a `ShellRunner`) is refused.
2. **Budget** (below). A refusal records `result_code = "budget"`.
3. **Egress pass.** The params go through the egress firewall, which swaps
   stored credentials for tags. Failure refuses the fire with an empty
   `params_hash`.
4. **Routing.** Every action type is routed through the policy router with
   `Origin = routing.OriginHook`, the type's capability, verb
   `hooks.fire`, a command built from the tagged params
   (`plugin-call <plugin>.<tool>`, `agent-note <note>`, or the shell
   command) and the tagged params hash. Allow continues; ask records
   `ask` and runs nothing (the evaluator files the approval); deny or an
   error records `refused`.
5. **Rehydration seam.** The required `Rehydrate` function turns the
   tagged params into the params the runner gets. It runs per fire, so a
   rotated credential is picked up at the next dispatch. Its error records
   `rehydrate` with the fixed text `hooks: rehydration seam failed` (the
   seam's own text may quote plaintext, so only its Kind is kept) and the
   runner is never called.
6. **Runner**, bounded by `ActionTimeout` even if it ignores its context.
   Once a fire has timed out, its abandoned pipeline starts no later
   stage after it observes the timeout: no seam after a late router. The
   runner may start at most at the timeout boundary, so a `timeout`
   record means the action's outcome is unknown. No plaintext is exposed
   and no second record is written. A
   panic anywhere in the pipeline records `panic` with the panic's type,
   never its value.
7. **Scrub.** Every non-empty rehydrated value (and the part of it that
   differs from its tagged form) is replaced by its tag in the error text,
   then secret-shaped configured values are redacted. This happens before
   the record is stored, published or ringed, and the scrubbed error is
   what the dispatcher returns.
8. **Zero.** The seam's `zero` func runs exactly once on every path,
   including runner error, panic and timeout.
9. **One `HookFire`** on the `hooks` namespace and in the ring.

```json
{"hook_id": "...", "namespace": "jobs.gate", "trigger": "...",
 "action_type": "plugin-call", "event_seq": 42, "depth": 0,
 "params_hash": "<sha256 of the tagged params>", "result_code": "success",
 "err_msg": "", "ts": "2026-...Z"}
```

`result_code` is one of `success`, `error`, `panic`, `timeout`, `refused`,
`ask`, `budget`, `rehydrate`, `duplicate`.

## Budget

Checked before anything runs; every limit refuses and never runs.

- **Lineage.** The bus keeps an in-memory cause table (the newest 4096
  caused events). A runner's context carries its chain; an event it
  publishes with that context is recorded against the chain. A fire on a
  caused event continues the chain at depth + 1; any other fire starts a
  chain at depth 0. Depth above 4 is refused, and one root's chain admits
  32 fires in total.
- **Rate backstop.** A runner that publishes with a fresh context starts a
  new root every time, so lineage cannot see the loop. Each hook id is
  limited to 32 admitted fires in any 60 seconds of the injected clock.
- **Redelivery.** The same hook on the same event inside one process runs
  once; the repeat records `duplicate`.

All of it is memory only. A restart resets the cause table and every
counter, so a crash loop gets up to 32 fires per hook per restart.

## Subscriptions

The dispatcher holds one subscription per namespace its hooks name, with
cursor `CursorPrefix + namespace`.

- `Bus.SubscribeFromHead` commits a never-used cursor at the namespace
  head before delivery, so a newly configured namespace never replays
  history. A cursor that has committed resumes as `Subscribe` does, so a
  restarted daemon still gets its backlog.
- `Bus.ResetCursorToHead` commits the head for a cursor; it refuses with
  `KindConflict` while that cursor has a live subscription.
- `Swap(reg)` replaces the registry atomically. Added namespaces are reset
  to head and subscribed; removed ones are unsubscribed and reset to head.
  A namespace removed and re-added never replays what landed while it was
  gone. While stopped, `Swap` applies the same resets for the next `Run`.
- The configured namespace set is persisted in `State` (namespace
  `hooks.dispatcher`, key `namespaces`). It never names a namespace the
  dispatcher does not follow. A running `Swap` resets and subscribes every
  added namespace without persisting it, then writes the new set once:
  that write is the commit point, made while removed namespaces are still
  subscribed.
- A failure up to and including the commit write stops the added
  subscriptions, leaves the persisted set as it was, keeps the previous
  registry in force and returns the error (a reload then publishes
  `hooks.reload.rejected`).
- After the commit, `Swap` installs the new registry, unsubscribes removed
  namespaces and resets their cursors best effort; it returns nil even if
  that reset fails. Those namespaces are outside the persisted set, so the
  next add or `Run` resets them.
- At `Run` start every namespace in exactly one of the persisted and
  configured sets is reset to head. A crash or a failed write during a
  `Swap` therefore cannot replay a backlog: at most, events published
  between the crash and the next `Run` on a namespace that was committed
  resume as any restarted subscription does.

## Reload

`WatchReload(ctx, bus, ns, kind, cursor)` subscribes from head
synchronously; the composition calls it before its initial build so a
reload announced between that build and `Run` is still delivered.
`(*ReloadWatch).Run(ctx, src, parse, d)` handles each `kind` event:
`parse(src())`, a fresh `Registry`, `Register` each entry, `d.Swap`. Any
failure keeps the previous set and publishes `hooks.reload.rejected` on
`ns` with `{"error": "<message>"}`. The message comes from the parser,
which never quotes values; a failing source publishes only its error kind.

## RPC surface

`Dispatcher.Hooks()` (configured hooks, params verbatim: an RPC exposing
them decides what to redact), `Fires()` (the newest 100, oldest first, no
params) and `Stats()` (`Registered`, `LastFire`, `BudgetRefusals`).

## Error kinds

Action-type refusals are `cascade.KindPolicyDenied` carrying the stable
string `HOOK_ACTION_NOT_PERMITTED`. Budget refusals are
`KindQuotaExhausted`, timeouts `KindTimeout`, config refusals
`KindInvalidInput`.

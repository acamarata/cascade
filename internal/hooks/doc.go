// Package hooks implements the internal hooks engine: it dispatches
// configured actions in response to typed events on the daemon's event
// bus (internal/events, C/S-04.T3), auditing every fire outcome.
//
// Purpose: fire configured hook actions when their trigger matches an
//
//	incoming bus event, restricted at W1 to exactly two action types
//	(plugin-call, agent-note); shell and any unrecognised type are
//	refused, permanently at this wave — shell lands only via I/S-18.T5's
//	policy-routed risk ladder (04-PEWS-PLAN-W1-W3.md §Epic C S-05.T1).
//
// Inputs: HookConfig values (from config.toml's [hooks] section,
//
//	08-INIT-CONFIG-SPEC.md §3 — parsing/loading is composition-root work,
//	out of this ticket's scope) registered via Registry.Register, and
//	typed events pulled from an internal/events.Bus subscription.
//
// Outputs: for plugin-call and agent-note hooks whose trigger matches an
//
//	event, an action dispatched through the injected PluginDispatcher or
//	NoteWriter seam; for every fire attempt (success, dispatch error,
//	timeout, panic, or defense-in-depth refusal) exactly one HookFire
//	event published to the event bus (audit.go) carrying a hash of the
//	action's params, never the params themselves.
//
// Constraints:
//
//   - Action-type restriction (R-14.9, this ticket's full_desc): the
//     registry refuses shell/unknown at Register time, and the dispatcher
//     independently refuses shell/unknown again immediately before any
//     inner call — defense-in-depth against a hook that somehow bypassed
//     registration validation (e.g. a future config-reload path that
//     skips Register).
//
//   - Bounded execution: internal/events' delivery guarantee is
//     AT-LEAST-ONCE — Publish never blocks, and a crash between event
//     landing and cursor commit redelivers. This package inherits both
//     halves of that contract: (a) a hook's action MUST be safe to run
//     twice for the same logical event (this package does not de-
//     duplicate; PluginDispatcher/NoteWriter implementations are
//     responsible for their own idempotency if their side effect is not
//     naturally idempotent — see dispatcher.go); (b) dispatchHook bounds
//     every action by an injected timeout via context.WithTimeout AND a
//     buffered result channel, so a hook whose action ignores context
//     cancellation entirely and blocks forever cannot hang the dispatch
//     loop — the loop moves on and audits result_code=timeout without
//     waiting for the abandoned goroutine (see dispatcher.go's package
//     comment for the full mechanism and its accepted goroutine-leak
//     trade-off).
//
//   - Re-entrancy: the dispatcher hard-excludes its own audit EventKind
//     (EventKindHookFire) from ever being matched as a trigger — both the
//     registry (Register refuses a HookConfig whose Trigger names it) and
//     the dispatcher (handleEvent skips it defensively even if a hook
//     were registered some other way) — which rules out the most direct
//     self-loop (a hook that fires on its own audit trail). It does NOT
//     and cannot rule out a longer cycle across independently-triggered
//     hooks (hook A's plugin-call causes some plugin to publish an event
//     that matches hook B's trigger, whose action causes an event
//     matching A's trigger again): PluginDispatcher and NoteWriter are
//     opaque injected interfaces with no real implementation yet in this
//     ticket, so this package has no visibility into what a dispatched
//     action ultimately publishes. Bounding that class of cycle (call-
//     depth tracking, a per-event hook-fire budget) is unimplemented and
//     explicitly out of scope here — stated plainly per Art.1 rather than
//     silently assumed away.
//
//   - Art.1 (anti-stub): agent-note actions have NO real consumer yet —
//     G/S-13 (the journal/memory domain NoteWriter is meant to wire to)
//     has not shipped. NoteWriter is a real, fully-specified interface
//     with a real dispatch path in this package; what does not exist yet
//     is a production implementation of it. The only implementations in
//     this tree are the composition root's future wiring (not this
//     ticket) and test fakes confined to _test.go, exactly as
//     PluginDispatcher is real-but-unwired pending C/S-05.T7.
//
//   - context.Context is the first argument on every crossing function;
//     Timestamp is stamped exclusively from an injected runtime.Clock
//     (never a bare time.Now — 02-TARGET-STRUCTURE.md §v1.1, forbidigo).
//
//   - Every external seam (PluginDispatcher, NoteWriter, the event bus)
//     is an injected interface, never a concrete import from a sibling
//     internal/ package (Art.10.2 import-boundary) — this package imports
//     internal/events (the bus itself, C/S-04.T3, already landed and a
//     direct dependency per the contract) and internal/runtime (Clock,
//     LooksLikeSecret) but never internal/plugins or a memory/journal
//     package directly.
//
// SPORT: internal.hooks.Registry/ADDED, internal.hooks.Dispatcher/ADDED,
//
//	internal.hooks.HookFire/ADDED (P1-E03-W1-S05-T1; placeholder per this
//	ticket's sport_updates — master lists undefined until N/S-28.T1's
//	taxonomy projector exists).
package hooks

// Package hooks implements the internal hooks engine: it dispatches
// configured actions in response to typed events on the daemon's event
// bus (internal/events), auditing every fire outcome.
//
// Purpose: fire configured hook actions when an event of a hook's trigger
//
//	kind lands on the namespace the hook names, through a runner table
//	keyed by action type (plugin-call and agent-note in P1; shell only
//	through ShellRunner, never the table).
//
// Inputs: the [[hooks]] config section (ParseHooksSection, config.go),
//
//	registered into a Registry, and typed events from one bus subscription
//	per namespace the registry names (subscriptions.go).
//
// Outputs: for every fire attempt exactly one HookFire, published on the
//
//	hooks audit namespace and kept in the recent-fires ring, carrying the
//	hash of the post-egress (tagged) params and never the params.
//
// Constraints:
//
//   - Pipeline order per fire (dispatcher.go): runnable check, fire budget
//     (budget.go), egress pass, policy routing with routing.OriginHook
//     (shell_route.go), the dispatch-time rehydration seam, the runner,
//     scrub, zero, one HookFire. No action type runs without an allow
//     verdict; the seam never sees raw configured params and a runner never
//     sees anything but the seam's output.
//
//   - Loops: a hook may not trigger on the audit kind or listen on the
//     audit namespace. Longer cycles are bounded by lineage (chain depth
//     4, 32 fires per root event) over the bus's in-memory cause table,
//     and loops that drop the runner's context by a 32-per-60s per-hook
//     backstop on the injected clock. Both are memory only: a restart
//     resets them.
//
//   - Delivery is at least once, as internal/events is. A redelivered
//     (hook, event) pair inside one process is recorded as duplicate and
//     not run; across a restart it may run again, so runners key their own
//     idempotency on the Fire identity.
//
//   - Every action is bounded by ActionTimeout even when it ignores its
//     context; the abandoned goroutine is a bounded, accepted leak.
//
//   - Every external seam (PluginDispatcher, NoteWriter, ShellRunner,
//     ActionRouter, Interceptor, the rehydration seam) is injected; this
//     package never imports internal/plugins, internal/secrets or a memory
//     package in non-test code.
package hooks

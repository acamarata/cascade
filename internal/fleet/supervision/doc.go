// Package supervision implements the attention queue (P1-E18-W4-S39-T1,
// 02-TARGET-STRUCTURE §internal/fleet/ SUP): the mechanism through which
// the fleet supervisor surfaces jobs requiring human intervention
// (stalled, blocked on a policy ask, or elevation-refused) so they are
// never silently invisible.
//
// Package layout:
//   - attention.go: AttentionItem, Kind, sentinel errors, Filter.
//   - attention_store.go: the provider.Store-backed Store (push/list/get/
//     ack), idempotency, and bounded-queue eviction.
//   - routing.go: R-21.157 visibility resolution (list/get) and addressed-
//     push authorization (push).
//   - subscribe.go: the fleet.sessions.changed event-bus subscriber that
//     pushes an item on a session's blocked/stalled transition.
//   - doctorcheck.go: the `cascade doctor` check.
//
// SPORT: fleet.supervision.attention_queue (ADD, per T-1 sport_updates).
package supervision

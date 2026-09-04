// Package audit records an append-only trail of security-relevant actions.
//
// Purpose: the immutable, queryable, explainable record of every policy
//
//	decision, routing decision, approval-queue transition, config reload,
//	and elevation attempt. It is the accountability backbone: what was
//	decided, by whom, on what basis, and what happened next.
//
// Inputs: a pkg/provider.Store scoped to the ratified audit domain, an
//
//	injected runtime.Clock, and an optional event-bus Publisher.
//
// Outputs: sealed Records, a paginated Page of them, an Explanation, or a
//
//	pkg/cascade taxonomy error.
//
// Constraints:
//   - Append is the only write. There is no update path and no delete
//     path, and the write itself is a conditional create, so an existing
//     record cannot be replaced even by a racing writer.
//   - Records are hash-chained and the tail is pinned by a head pointer,
//     so a record altered, replaced, or removed behind this API is
//     DETECTED on read and refused, not silently returned.
//   - Reads FAIL CLOSED. An unrecognised event kind, an unknown filter
//     field, a malformed cursor, or a present-but-empty constraint list
//     refuses. None of them widens into "match everything".
//   - A record about a secret never contains the secret: there is no
//     field for a value, and ParamsHash is the sanctioned way to record
//     what the parameters were without recording them.
//   - Ordering is by the log's own sequence number, so two records
//     appended within one clock tick still have one defined order.
//   - The clock is injected; this package never reads the wall clock.
//   - Nothing here imports internal/policy, internal/secrets, or
//     internal/daemon. Those subsystems inject the Writer interface
//     instead, which is what keeps the dependency direction one-way.
//
// SPORT: audit/audit-log (ADD, P1-E09-W2-S18-T2).
package audit

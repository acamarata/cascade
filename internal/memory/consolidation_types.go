package memory

// Purpose: the consolidation job's vocabulary — its refusals, its method
//   name, the event it emits and the sink that receives it, and the
//   config and report shapes a caller reads. Split from consolidation.go
//   under the 300-line file cap; nothing here does I/O or reads a clock.
// Inputs: none — these are types and sentinels.
// Outputs: the values every caller of ConsolidateMemories names.
// Constraints: each sentinel wraps exactly one frozen pkg/cascade Kind; no
//   Kind is invented; no method name is declared that this build cannot
//   actually produce.
// SPORT: internal.memory.consolidation.ConsolidateMemories (ADD,
//   P1-E07-W2-S13-T4).

import (
	"context"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Consolidation sentinel errors. Each names one refusal precisely and
// wraps exactly one frozen pkg/cascade Kind; no Kind is invented.
var (
	// ErrEmbeddingConsolidationUnavailable is returned when a caller asks
	// for the embedding clustering path. That path is gated on the S-13.T2
	// index, which this build does not carry, so the request is REFUSED
	// rather than quietly served by the exact-hash path: a caller who
	// switched the flag on believes near-duplicates are being merged, and
	// silently doing something narrower would be a lie about what ran.
	ErrEmbeddingConsolidationUnavailable = cascade.New(cascade.KindUnsupported,
		"embedding consolidation is not available in this build")
	// ErrMalformedConsolidation is returned when a consolidation record
	// exists but cannot be parsed whole. It fails closed for the reason
	// every other read in this package does: a record that cannot be read
	// is the only account of what happened to a retired memory, and
	// treating it as absent would let the next run overwrite it.
	ErrMalformedConsolidation = cascade.New(cascade.KindIntegrity,
		"malformed memory consolidation record")
	// ErrUnsupportedConsolidationFormat is the forward-compatibility
	// refusal for a consolidation record written by a newer build.
	ErrUnsupportedConsolidationFormat = cascade.New(cascade.KindUnsupported,
		"unsupported memory consolidation record format version")
)

// The consolidation method a report and an event carry. It is a value on
// the wire, so it is a constant rather than a literal typed at each site.
const (
	// ConsolidationMethodExactHash is the only method this build performs:
	// exact normalized-content-hash grouping (R-14.21). The embedding
	// method has no constant here on purpose: this build never returns
	// one, and a name nothing produces would read as a supported mode.
	ConsolidationMethodExactHash = "exact-hash"
)

// MemoryConsolidatedEvent is the bus name of the event one merged group
// emits.
const MemoryConsolidatedEvent = "memory.consolidated"

// ConsolidatedEvent reports that one group of exact duplicates was
// consolidated into a single surviving record.
type ConsolidatedEvent struct {
	// MemberIDs are the canonical addresses that were RETIRED, in lexical
	// order. The survivor is not among them.
	MemberIDs []string `json:"member_ids"`
	// ConsolidatedID is the canonical address of the surviving record.
	ConsolidatedID string `json:"consolidated_id"`
	// Method is how the group was formed (ConsolidationMethodExactHash).
	Method string `json:"method"`
	// ConsolidatedAt is the instant of the merge, from the injected clock.
	ConsolidatedAt time.Time `json:"consolidated_at"`
}

// EventName returns the bus name of this event.
func (ConsolidatedEvent) EventName() string { return MemoryConsolidatedEvent }

// ConsolidationEventSink receives one event per consolidated group.
//
// It is declared here, at the point of use, rather than imported from the
// event bus, for the reason CandidateEventSink gives: this package depends
// on the shape of a sink, not on any particular bus, and the composition
// root wires the two together. A sink failure is NOT fatal — the merge is
// already durable by the time the event is offered, and failing the job
// afterwards would report work as not done that in fact was.
type ConsolidationEventSink interface {
	// MemoryConsolidated reports one merged group.
	MemoryConsolidated(ctx context.Context, ev ConsolidatedEvent) error
}

// discardConsolidationEvents is the sink a consolidator built with no sink
// uses. It is a real, complete implementation: a caller with no event bus
// is a supported configuration, and discarding an event never changes what
// is stored or retired.
type discardConsolidationEvents struct{}

// MemoryConsolidated discards the event.
func (discardConsolidationEvents) MemoryConsolidated(context.Context, ConsolidatedEvent) error {
	return nil
}

// ConsolidationConfig is the per-run configuration of the job, sourced
// from the [memory] config section by the composition root.
type ConsolidationConfig struct {
	// EmbeddingEnabled mirrors [memory].consolidation_embedding. It is
	// default-off, and switching it on in this build is refused with
	// ErrEmbeddingConsolidationUnavailable rather than silently downgraded
	// to exact-hash grouping.
	EmbeddingEnabled bool
	// DryRun computes the whole grouping pass and reports what it would
	// merge, writing nothing and emitting nothing.
	DryRun bool
}

// ConsolidationGroup is one set of exact duplicates in a report.
type ConsolidationGroup struct {
	// CanonicalID is the address of the record that survives.
	CanonicalID string `json:"canonical_id"`
	// MemberIDs are the addresses retired into it, in lexical order.
	MemberIDs []string `json:"member_ids"`
}

// ConsolidationReport is what one run of the job did.
//
// Merged counts GROUPS, not records: a report of Merged:1 over a group of
// three means two records were retired into one, which Retired states
// exactly. Reporting only a group count would understate what was removed.
type ConsolidationReport struct {
	// Merged is the number of duplicate groups consolidated.
	Merged int `json:"merged"`
	// Retired is the number of individual records tombstoned.
	Retired int `json:"retired"`
	// NoChange is true when the run found nothing to do, which is the
	// idempotent second-run outcome (§5.9).
	NoChange bool `json:"no_change"`
	// Skipped is true when another consolidation was already running in
	// this process and this one stood down without touching anything.
	Skipped bool `json:"skipped"`
	// Method is the grouping method used (ConsolidationMethodExactHash).
	Method string `json:"method"`
	// DryRun echoes the request, so a caller reading only the result can
	// tell a rehearsal from a run that actually retired records.
	DryRun bool `json:"dry_run"`
	// Groups describes each consolidated (or, in a dry run, each
	// consolidatable) group, in canonical-address order.
	Groups []ConsolidationGroup `json:"groups,omitempty"`
	// Unreadable lists the addresses of records that could not be parsed
	// and were therefore left entirely alone, in lexical order. A damaged
	// file is never merged and never retired; it is reported so it can be
	// repaired.
	Unreadable []string `json:"unreadable,omitempty"`
}

package memory

// Purpose: the staleness scan. It flags records nothing has updated in a
//   while so a person can look at them later. It is a heuristic queue and
//   nothing more: it never edits, retires or deletes a record.
// Inputs: the T1 file store, an injected Clock, a StalenessConfig window,
//   and an optional event sink.
// Outputs: staleness-set files under {base}/staleness/, one
//   StaleQueuedEvent per scan that changed the set, and a
//   StalenessReport; or a typed pkg/cascade error.
// Constraints: no bare time.Now; the queue is RECOMPUTED each scan, so a
//   record that was refreshed leaves it — being old is never allowed to
//   become a permanent verdict; no map iteration decides what is queued.
// SPORT: internal.memory.staleness.ScanStaleness (ADD, P1-E07-W2-S13-T4).

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// DefaultStalenessWindow is the age past which a record is flagged, when
// [memory].staleness_window_days says nothing. Thirty days is the value
// the contract names; it is not inferred from anything.
const DefaultStalenessWindow = 30 * 24 * time.Hour

// MemoryStaleQueuedEvent is the bus name of the staleness event.
const MemoryStaleQueuedEvent = "memory.stale_queued"

// StaleQueuedEvent reports that the staleness queue gained entries.
type StaleQueuedEvent struct {
	// StaleIDs are the canonical addresses NEWLY added to the queue by
	// this scan, in lexical order. Addresses already queued are not
	// repeated, so a subscriber is told about a record once.
	StaleIDs []string `json:"stale_ids"`
	// QueuedAt is the instant of the scan, from the injected clock.
	QueuedAt time.Time `json:"queued_at"`
}

// EventName returns the bus name of this event.
func (StaleQueuedEvent) EventName() string { return MemoryStaleQueuedEvent }

// StalenessEventSink receives one event per scan that queued something
// new. It is declared here, at the point of use, for the same reason
// ConsolidationEventSink is, and a sink failure is likewise non-fatal.
type StalenessEventSink interface {
	// MemoryStaleQueued reports newly queued stale records.
	MemoryStaleQueued(ctx context.Context, ev StaleQueuedEvent) error
}

// discardStalenessEvents is the sink a scanner built with no sink uses.
type discardStalenessEvents struct{}

// MemoryStaleQueued discards the event.
func (discardStalenessEvents) MemoryStaleQueued(context.Context, StaleQueuedEvent) error { return nil }

// StalenessConfig is the per-run configuration of the scan.
type StalenessConfig struct {
	// Window is the age past which a record is flagged. A zero or negative
	// value uses DefaultStalenessWindow rather than flagging everything:
	// an unset config must not mean "every record is stale".
	Window time.Duration
}

// window returns the effective staleness window.
func (c StalenessConfig) window() time.Duration {
	if c.Window <= 0 {
		return DefaultStalenessWindow
	}
	return c.Window
}

// StalenessReport is what one scan found.
type StalenessReport struct {
	// Queued is how many addresses this scan ADDED to the queue.
	Queued int `json:"queued"`
	// Dropped is how many addresses this scan REMOVED from the queue
	// because they are no longer stale — the record was updated, or it is
	// gone. A queue that only ever grew would turn a heuristic into a
	// permanent verdict.
	Dropped int `json:"dropped"`
	// Total is the size of the queue after the scan.
	Total int `json:"total"`
	// Idempotent is true when the scan changed nothing, which is the
	// second-run outcome (§5.9).
	Idempotent bool `json:"idempotent"`
	// Skipped is true when another scan was already running in this
	// process and this one stood down without touching anything.
	Skipped bool `json:"skipped"`
	// WindowDays is the window this scan judged against, so a report can
	// be read without also holding the config that produced it.
	WindowDays float64 `json:"window_days"`
	// StaleIDs is the whole queue after the scan, in lexical order. It is
	// the review surface: a heuristic that flags a user's records has to
	// be readable, not merely actionable by a later pipeline.
	StaleIDs []string `json:"stale_ids,omitempty"`
	// Unreadable lists addresses whose records could not be parsed. They
	// are neither queued nor dropped; they are reported for repair.
	Unreadable []string `json:"unreadable,omitempty"`
}

// StalenessScanner maintains the staleness queue over one store tree.
//
// # This is a heuristic, and it is treated as one
//
// Age is not wrongness. A record nothing has touched for a year may be the
// truest thing in the store. So this scanner has exactly one power: it
// writes addresses into a queue file. It never edits a record, never
// tombstones one, never changes a confidence, and emits nothing that any
// other subsystem in this build acts on automatically. The queue is an
// input to review (the S-14.T3 surface) and to the S-14.T4 forget
// pipeline's CANDIDATE list — a candidate is a thing a person decides
// about, not a thing already decided.
//
// # And it is reversible
//
// Each scan recomputes the whole set from the tree rather than appending
// to what is already there. An address whose record has since been updated
// is DROPPED from the queue on the next scan, with no manual step. That is
// what keeps "this looked old once" from hardening into a standing
// judgement about a user's memory.
type StalenessScanner struct {
	base  string
	store MemoryStore
	clock Clock
	sink  StalenessEventSink
	fs    fileSystem
	// running is the in-process re-entrancy guard, for the reason
	// Consolidator.running gives.
	running sync.Mutex
}

// NewStalenessScanner returns a scanner over the store tree rooted at
// base, taking its timestamps from clk and reporting to sink. A nil sink
// discards events, which is the documented no-bus configuration.
func NewStalenessScanner(base string, store MemoryStore, clk Clock, sink StalenessEventSink) *StalenessScanner {
	return newStalenessScannerWithFS(base, store, clk, sink, osFS{})
}

// newStalenessScannerWithFS is NewStalenessScanner with the file-system
// seam supplied. Unexported: tests inject a failing file system through
// it, and no shipped path may substitute anything for osFS.
func newStalenessScannerWithFS(
	base string, store MemoryStore, clk Clock, sink StalenessEventSink, sys fileSystem,
) *StalenessScanner {
	if sink == nil {
		sink = discardStalenessEvents{}
	}
	return &StalenessScanner{base: base, store: store, clock: clk, sink: sink, fs: sys}
}

// ScanStaleness runs one staleness pass.
//
// # The rule
//
// A record is stale when now minus its Provenance.UpdatedAt exceeds
// cfg.Window (default DefaultStalenessWindow, config key
// memory.staleness_window_days). UpdatedAt equals CreatedAt for a record
// nothing has rewritten, so a record that was written once and never
// touched is judged on its creation, which is the same instant. There is
// no recall-tracking field in the T1 model, so staleness is defined purely
// on that age — a record that is read constantly and never edited will be
// flagged, which is exactly why the queue is advisory.
//
// # What it does with the answer
//
// It writes one staleness-set file per kind under {base}/staleness/,
// holding the sorted addresses of that kind's stale records. It retires
// nothing and edits nothing. The queue is fully recomputed, so an address
// that is no longer stale is dropped.
//
// # Idempotency (§5.9)
//
// A second scan with the same clock over the same tree computes the same
// set, writes no file (the encoded bytes are compared before writing),
// emits no event, and returns StalenessReport{Idempotent:true, Queued:0}.
//
// # Errors
//
// A record that cannot be parsed is reported in Unreadable and otherwise
// left entirely alone. A file-system failure returns a typed error; the
// per-kind files written before it are valid and complete on their own,
// because each kind's set is written atomically as a whole.
func (s *StalenessScanner) ScanStaleness(ctx context.Context, cfg StalenessConfig) (StalenessReport, error) {
	window := cfg.window()
	report := StalenessReport{Idempotent: true, WindowDays: window.Hours() / 24}
	if !s.running.TryLock() {
		report.Skipped = true
		report.Idempotent = false
		return report, nil
	}
	defer s.running.Unlock()
	if err := ctx.Err(); err != nil {
		return report, cascade.Wrap(cascade.KindCanceled, err, "memory staleness scan canceled")
	}
	now := s.clock.Now().UTC()
	var added []string
	for _, kind := range AllKinds() {
		kindAdded, err := s.scanKind(ctx, kind, now, window, &report)
		if err != nil {
			return report, err
		}
		added = append(added, kindAdded...)
	}
	sort.Strings(added)
	sort.Strings(report.StaleIDs)
	sort.Strings(report.Unreadable)
	report.Total = len(report.StaleIDs)
	report.Queued = len(added)
	report.Idempotent = report.Queued == 0 && report.Dropped == 0
	return report, s.emit(ctx, added, now)
}

// scanKind recomputes one kind's staleness set and returns the addresses
// this scan newly added, updating the running report.
func (s *StalenessScanner) scanKind(
	ctx context.Context, kind MemoryKind, now time.Time, window time.Duration, report *StalenessReport,
) ([]string, error) {
	names, err := s.store.List(ctx, kind)
	if err != nil {
		return nil, err
	}
	stale := make([]string, 0, len(names))
	for _, name := range names {
		entry, readErr := s.store.Read(ctx, kind, name)
		if readErr != nil {
			if isRecordUnreadable(readErr) {
				report.Unreadable = append(report.Unreadable, recordID(kind, name))
				continue
			}
			return nil, readErr
		}
		if isStale(entry, now, window) {
			stale = append(stale, entryID(entry))
		}
	}
	sort.Strings(stale)
	previous, err := s.loadQueue(kind)
	if err != nil {
		return nil, err
	}
	if err := s.saveQueue(kind, stale, now); err != nil {
		return nil, err
	}
	report.StaleIDs = append(report.StaleIDs, stale...)
	report.Dropped += len(difference(previous, stale))
	return difference(stale, previous), nil
}

// isStale reports whether e's last-update age exceeds window at now.
//
// The comparison is strictly greater than: a record exactly one window old
// is not yet stale, so the boundary is stable rather than flapping with
// the resolution of the clock.
func isStale(e MemoryEntry, now time.Time, window time.Duration) bool {
	updated := e.Provenance.UpdatedAt
	if updated.IsZero() {
		updated = e.Provenance.CreatedAt
	}
	if updated.IsZero() {
		return false
	}
	return now.Sub(updated.UTC()) > window
}

// difference returns the members of a that are not in b, preserving a's
// order. Both inputs are already sorted, so the result is sorted too.
func difference(a, b []string) []string {
	inB := make(map[string]bool, len(b))
	for _, s := range b {
		inB[s] = true
	}
	out := make([]string, 0, len(a))
	for _, s := range a {
		if !inB[s] {
			out = append(out, s)
		}
	}
	return out
}

// emit offers the newly queued addresses to the sink. A scan that queued
// nothing emits nothing, so a subscriber is not woken by a no-op run. The
// sink's failure is non-fatal for the reason Consolidator.emit gives: the
// queue is already durable.
func (s *StalenessScanner) emit(ctx context.Context, added []string, now time.Time) error {
	if len(added) == 0 {
		return nil
	}
	_ = s.sink.MemoryStaleQueued(ctx, StaleQueuedEvent{StaleIDs: added, QueuedAt: now})
	return nil
}

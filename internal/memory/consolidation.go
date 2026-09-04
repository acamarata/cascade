package memory

// Purpose: the background consolidation job. It finds records whose
//   normalized bodies are byte-identical within one kind (R-14.21), keeps
//   the oldest of each group, and retires the rest — but only after
//   writing a consolidation record that holds every retired member's full
//   metadata, so a user who remembers saying something can always be told
//   where it went.
// Inputs: the T1 file store, an injected Clock, an optional event sink,
//   and a ConsolidationConfig carrying the default-off embedding flag and
//   the dry-run rehearsal.
// Outputs: consolidation records under {base}/consolidations/, tombstones
//   on the retired members, one ConsolidatedEvent per group, and a
//   ConsolidationReport; or a typed pkg/cascade error.
// Constraints: no bare time.Now; no map iteration decides what is merged
//   or dropped (every group key is sorted before it is acted on); the
//   record is written BEFORE any member is tombstoned, so an interruption
//   leaves an explanation rather than an unexplained absence; content is
//   never rewritten, only retired.
// SPORT: internal.memory.consolidation.ConsolidateMemories (ADD,
//   P1-E07-W2-S13-T4).

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Consolidator runs the consolidation job over one memory store tree.
//
// # Why this can only ever retire, never rewrite
//
// Every member of a group has, by the grouping rule, a body that
// normalizes to exactly the same bytes as the survivor's. So there is no
// content to merge and nothing to write into the survivor: the job leaves
// the surviving file untouched on disk and retires the others. That is a
// deliberate narrowing of what a background job is allowed to do to a
// user's own words — it can take a duplicate away, and it can never
// author a new sentence into a record the user wrote.
//
// # What a retired record leaves behind
//
// Before any member is tombstoned, a consolidation record is written under
// {base}/consolidations/{kind}/{survivor}.consolidation.json holding every
// retired member's full metadata: its address, description, scope, commit,
// supersedes reference, confidence, origin, session, timestamps and
// content hash, together with the body they all shared. A user who
// remembers writing something can be told, from that file alone, that it
// was consolidated, when, and into which surviving record. Nothing this
// job removes is unaccounted for.
type Consolidator struct {
	base  string
	store MemoryStore
	clock Clock
	sink  ConsolidationEventSink
	fs    fileSystem
	// running is the in-process re-entrancy guard. The scheduled job and
	// the memory.consolidate RPC verb reach the same tree, and two runs
	// racing over one group would have the second one re-reading a set the
	// first is midway through retiring. TryLock is what lets the second
	// stand down reporting Skipped rather than blocking a user's command
	// behind a background job.
	running sync.Mutex
}

// NewConsolidator returns a Consolidator over the store tree rooted at
// base, taking its timestamps from clk and reporting merges to sink. A nil
// sink discards events, which is the documented no-bus configuration.
func NewConsolidator(base string, store MemoryStore, clk Clock, sink ConsolidationEventSink) *Consolidator {
	return newConsolidatorWithFS(base, store, clk, sink, osFS{})
}

// newConsolidatorWithFS is NewConsolidator with the file-system seam
// supplied. Unexported: tests inject a failing file system through it, and
// no shipped path may substitute anything for osFS.
func newConsolidatorWithFS(
	base string, store MemoryStore, clk Clock, sink ConsolidationEventSink, sys fileSystem,
) *Consolidator {
	if sink == nil {
		sink = discardConsolidationEvents{}
	}
	return &Consolidator{base: base, store: store, clock: clk, sink: sink, fs: sys}
}

// ConsolidateMemories runs one consolidation pass.
//
// # Algorithm (R-14.21, exact-hash only)
//
//  1. Walk every kind in AllKinds order, listing live records lexically.
//     A record that cannot be parsed is recorded in Unreadable and then
//     ignored entirely; it is never grouped and never retired.
//  2. Group by (kind, BLAKE3 of the normalized body). Normalization folds
//     CRLF and CR to LF, strips trailing space from every line, and trims
//     the whole body. Two records group only when those bytes are
//     identical. Nothing about similarity, distance or hash prefixes takes
//     part: the FNV-prefix clustering an earlier draft proposed is
//     stricken, because hash prefixes do not correlate with similarity.
//  3. For every group of two or more, the SURVIVOR is the member with the
//     earliest CreatedAt, ties broken lexically by address. Keeping the
//     oldest is what makes the outcome match what a user expects: the
//     record they first wrote is the one that remains.
//  4. Write the consolidation record, then tombstone the other members,
//     then emit the event. That order is the whole crash contract — see
//     the type doc.
//
// # Idempotency (§5.9)
//
// A second run over an already-consolidated tree forms no group of two,
// writes nothing, emits nothing, and returns
// ConsolidationReport{Merged:0, NoChange:true}. The result does not depend
// on run order, map iteration or the clock.
//
// # Errors
//
// EmbeddingEnabled is refused with ErrEmbeddingConsolidationUnavailable.
// A file-system failure part way through returns the failure together with
// the report of the groups that DID complete, so a caller is never told
// that nothing happened when something did.
func (c *Consolidator) ConsolidateMemories(ctx context.Context, cfg ConsolidationConfig) (ConsolidationReport, error) {
	if cfg.EmbeddingEnabled {
		return ConsolidationReport{}, cascade.Wrapf(cascade.KindUnsupported,
			ErrEmbeddingConsolidationUnavailable,
			"[memory].consolidation_embedding requires the memory index, which this build does not carry")
	}
	report := ConsolidationReport{
		Method: ConsolidationMethodExactHash, DryRun: cfg.DryRun, NoChange: true,
	}
	if !c.running.TryLock() {
		report.Skipped = true
		return report, nil
	}
	defer c.running.Unlock()
	if err := ctx.Err(); err != nil {
		return report, cascade.Wrap(cascade.KindCanceled, err, "memory consolidation canceled")
	}
	groups, unreadable, err := c.plan(ctx)
	if err != nil {
		return report, err
	}
	report.Unreadable = unreadable
	return c.apply(ctx, report, groups, cfg)
}

// duplicateGroup is one planned group: the survivor and the members that
// would be retired into it, all fully read.
type duplicateGroup struct {
	survivor MemoryEntry
	retired  []MemoryEntry
}

// plan reads the whole live tree and returns every group of two or more
// exact duplicates, in canonical-address order of their survivors, plus
// the addresses of the records that could not be read.
//
// The returned order is derived from a sorted key list, never from a map
// walk: which group is acted on first must not vary between runs.
func (c *Consolidator) plan(ctx context.Context) ([]duplicateGroup, []string, error) {
	buckets := map[string][]MemoryEntry{}
	var unreadable []string
	for _, kind := range AllKinds() {
		names, err := c.store.List(ctx, kind)
		if err != nil {
			return nil, nil, err
		}
		for _, name := range names {
			entry, err := c.store.Read(ctx, kind, name)
			if err != nil {
				if isRecordUnreadable(err) {
					unreadable = append(unreadable, recordID(kind, name))
					continue
				}
				return nil, nil, err
			}
			key := groupKey(kind, entry.Body)
			buckets[key] = append(buckets[key], entry)
		}
	}
	sort.Strings(unreadable)
	return sortedGroups(buckets), unreadable, nil
}

// sortedGroups turns the grouping buckets into a deterministic slice of
// the groups that actually have duplicates.
func sortedGroups(buckets map[string][]MemoryEntry) []duplicateGroup {
	keys := make([]string, 0, len(buckets))
	for k, members := range buckets {
		if len(members) >= 2 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]duplicateGroup, 0, len(keys))
	for _, k := range keys {
		out = append(out, newDuplicateGroup(buckets[k]))
	}
	sort.Slice(out, func(i, j int) bool {
		return entryID(out[i].survivor) < entryID(out[j].survivor)
	})
	return out
}

// newDuplicateGroup picks the survivor and orders the retirees.
//
// The survivor is the OLDEST member by CreatedAt, ties broken by address.
// Both halves matter: oldest is the record the user is most likely to
// remember writing, and the lexical tie-break is what makes the choice
// identical on every machine when two records share an instant.
func newDuplicateGroup(members []MemoryEntry) duplicateGroup {
	ordered := make([]MemoryEntry, len(members))
	copy(ordered, members)
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if !a.Provenance.CreatedAt.Equal(b.Provenance.CreatedAt) {
			return a.Provenance.CreatedAt.Before(b.Provenance.CreatedAt)
		}
		return entryID(a) < entryID(b)
	})
	return duplicateGroup{survivor: ordered[0], retired: ordered[1:]}
}

// groupKey is the (kind, normalized-body-hash) grouping key. The kind is a
// prefix rather than a mixed-in value so two records with identical bodies
// filed under different kinds can never collide: a user fact and a project
// note that happen to read the same are two different memories.
func groupKey(kind MemoryKind, body string) string {
	return string(kind) + "\x00" + HashBody(normalizeBody(body))
}

// normalizeBody folds the differences that are not differences in what a
// record says: line-ending style, trailing space on a line, and leading or
// trailing blank lines. Anything else — a changed word, a changed
// character, a changed amount of indentation — makes two bodies distinct
// and keeps them out of the same group.
func normalizeBody(body string) string {
	b := strings.ReplaceAll(body, "\r\n", "\n")
	b = strings.ReplaceAll(b, "\r", "\n")
	lines := strings.Split(b, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// entryID returns a record's canonical "<kind>/<name>" address.
func entryID(e MemoryEntry) string { return recordID(e.Kind, e.Name) }

// isRecordUnreadable reports whether err means the file is there but this
// build cannot read it whole — damaged, or written by a newer build. Both
// are reported and skipped; neither is ever treated as an absence, because
// a record this run cannot read is a record it must not retire.
func isRecordUnreadable(err error) bool {
	return errorsIsAny(err, ErrMalformedEntry, ErrUnsupportedFormat)
}

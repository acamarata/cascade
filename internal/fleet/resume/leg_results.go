// Purpose: the fleet side of contract:fanout-leg-results. One adapter
//   implements both conductor.JournalAppender (leg entries in the journal
//   entity FanOutEntity(fanoutID), per-leg attempts, the 3-start cap) and
//   conductor.LegResultStore (version-1 records, create-only, in namespace
//   conductor.fanout.legs under <fanoutID>#<legIndex>) over the
//   provider.Store the journal wraps, passed explicitly. A start's attempt
//   is a create-only slot in that store, so every adapter sharing it
//   allocates from one durable sequence.
// Inputs: a journal.Store and a provider.Store (both required for the
//   LegResultStore role).
// Outputs: leg attempts, stored LegResults, or typed errors.
// Constraints: journal payloads carry the result key and the digest,
//   never Response content; every store error is returned; a record is
//   written create-only and never rewritten; DeleteTask removes one
//   fan-out's records, and a finished leg's attempt slots, and nothing else.
// SPORT: internal.fleet.resume.ResumeManager/CHANGE (P1-CORE-15).

package resume

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const (
	// legResultsNamespace holds every stored LegResult record.
	legResultsNamespace = "conductor.fanout.legs"
	// legAttemptsNamespace holds one create-only slot per raw leg start,
	// keyed <fanoutID>#<legIndex>#<attempt>. A leg's slots are deleted only
	// together with its stored result (DeleteTask), never while it can start.
	legAttemptsNamespace = "conductor.fanout.attempts"
	// legResultVersion is the only record version this package reads.
	legResultVersion = 1
	// maxLegStarts is the per-leg start cap: a leg with this many starts
	// and no ok done is never dispatched again (unknown outcome).
	maxLegStarts = 3
	// slotConflictRetries bounds the retries of one attempt slot after a
	// conflict that left it absent.
	slotConflictRetries = 3
)

// ErrLegAttemptsExhausted refuses a fourth start of a leg with no ok
// outcome: its effect is unknown and it is never dispatched again.
var ErrLegAttemptsExhausted = cascade.New(cascade.KindConflict, "resume: fan-out leg reached its start cap with no ok outcome; outcome unknown, never dispatched again")

// ErrLegTerminal reports a fan-out with a failed_terminal leg (a door
// refusal): the fan-out is finalized terminal and never retried.
var ErrLegTerminal = cascade.New(cascade.KindPolicyDenied, "resume: a fan-out leg was refused terminally; the fan-out is terminal and never retried")

// ErrLegStoreUnset reports a LegResultStore call on an adapter built
// without a provider.Store: a refusal, never a silent skip.
var ErrLegStoreUnset = cascade.New(cascade.KindInvalidInput, "resume: fan-out leg results need a non-nil journal and provider store")

// journalAppenderAdapter implements conductor.JournalAppender and, when
// built by newLegAdapter with a store, conductor.LegResultStore.
type journalAppenderAdapter struct {
	journal journal.Store
	store   provider.Store
}

var (
	_ conductor.JournalAppender = (*journalAppenderAdapter)(nil)
	_ conductor.LegResultStore  = (*journalAppenderAdapter)(nil)
)

// newLegAdapter builds the adapter over the journal and the provider
// store it wraps. Both are required.
func newLegAdapter(js journal.Store, store provider.Store) (*journalAppenderAdapter, error) {
	if js == nil || store == nil {
		return nil, ErrLegStoreUnset
	}
	return &journalAppenderAdapter{journal: js, store: store}, nil
}

// legRecord is the version-1 stored form of a conductor.LegResult.
type legRecord struct {
	Version       int                      `json:"version"`
	FanOutID      string                   `json:"fanout_id"`
	TaskID        string                   `json:"task_id"`
	LegIndex      int                      `json:"leg_index"`
	Attempt       uint64                   `json:"attempt"`
	RequestDigest string                   `json:"request_digest"`
	Sensitivity   provider.SensitivityTier `json:"sensitivity"`
	Response      provider.ModelResponse   `json:"response"`
}

// PutLegResult writes r create-only (CompareAndSwap from absent).
func (a *journalAppenderAdapter) PutLegResult(ctx context.Context, r conductor.LegResult) error {
	if a.store == nil {
		return ErrLegStoreUnset
	}
	if !validFanOutID(r.FanOutID) {
		return cascade.Newf(cascade.KindInvalidInput, "resume: invalid fan-out id %q for a leg result", r.FanOutID)
	}
	data, err := json.Marshal(legRecord{Version: legResultVersion, FanOutID: r.FanOutID, TaskID: r.TaskID,
		LegIndex: r.LegIndex, Attempt: r.Attempt, RequestDigest: r.RequestDigest, Sensitivity: r.Sensitivity, Response: r.Response})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "resume: encoding fan-out leg result")
	}
	key := conductor.LegResultKey(r.FanOutID, r.LegIndex)
	err = a.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return tx.CompareAndSwap(ctx, legResultsNamespace, key, nil, data)
	})
	return rewrap(err, "resume: storing fan-out leg result "+key+" (create-only)")
}

// claimAttempt allocates a leg start's attempt: the first of slots
// 1..maxLegStarts it creates (CompareAndSwap from absent) is its own, so
// two adapters, or two processes, over one store never share an attempt
// and at most maxLegStarts raw starts ever succeed. It moves past a slot
// only when that slot is stored; with every slot stored it refuses with
// ErrLegAttemptsExhausted. A journal-only adapter refuses: an attempt it
// could not claim durably would not be unique.
func (a *journalAppenderAdapter) claimAttempt(ctx context.Context, fanoutID string, legIndex int, digest string) (uint64, error) {
	if a.store == nil {
		return 0, ErrLegStoreUnset
	}
	data, err := json.Marshal(legPayload{LegIndex: legIndex, RequestDigest: digest})
	if err != nil {
		return 0, cascade.Wrap(cascade.KindInternal, err, "resume: encoding leg attempt slot")
	}
	for attempt := uint64(1); attempt <= maxLegStarts; attempt++ {
		claimed, err := a.claimSlot(ctx, conductor.LegResultKey(fanoutID, legIndex)+"#"+itoa(attempt), data)
		if err != nil {
			return 0, err
		}
		if claimed {
			return attempt, nil
		}
	}
	return 0, ErrLegAttemptsExhausted
}

// claimSlot creates key create-only: true when this call created it,
// false when the slot is stored (another start holds it). A conflict
// that leaves the slot absent (a lock or busy conflict, not a taken
// slot) is retried up to slotConflictRetries times, then its error is
// returned: a slot is never skipped and the cap is never reported
// without every slot stored.
func (a *journalAppenderAdapter) claimSlot(ctx context.Context, key string, data []byte) (bool, error) {
	var err error
	for try := 0; try <= slotConflictRetries; try++ {
		err = a.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
			return tx.CompareAndSwap(ctx, legAttemptsNamespace, key, nil, data)
		})
		if err == nil {
			return true, nil
		}
		if !cascade.HasKind(err, cascade.KindConflict) {
			return false, rewrap(err, "resume: claiming fan-out leg attempt "+key)
		}
		_, getErr := a.store.Get(ctx, legAttemptsNamespace, key)
		if getErr == nil {
			return false, nil
		}
		if !cascade.HasKind(getErr, cascade.KindNotFound) {
			return false, rewrap(getErr, "resume: reading fan-out leg attempt slot "+key)
		}
	}
	return false, rewrap(err, "resume: claiming fan-out leg attempt "+key+" (conflict with the slot still absent)")
}

// GetLegResult reads one record. An absent key is (zero, false, nil); an
// undecodable record or an unknown version is KindIntegrity.
func (a *journalAppenderAdapter) GetLegResult(ctx context.Context, fanoutID string, legIndex int) (conductor.LegResult, bool, error) {
	if a.store == nil {
		return conductor.LegResult{}, false, ErrLegStoreUnset
	}
	key := conductor.LegResultKey(fanoutID, legIndex)
	data, err := a.store.Get(ctx, legResultsNamespace, key)
	if cascade.HasKind(err, cascade.KindNotFound) {
		return conductor.LegResult{}, false, nil
	}
	if err != nil {
		return conductor.LegResult{}, false, rewrap(err, "resume: reading fan-out leg result "+key)
	}
	var rec legRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return conductor.LegResult{}, false, cascade.Wrapf(cascade.KindIntegrity, err, "resume: fan-out leg result %s is undecodable", key)
	}
	if rec.Version != legResultVersion {
		return conductor.LegResult{}, false, cascade.Newf(cascade.KindIntegrity, "resume: fan-out leg result %s has version %d, want %d", key, rec.Version, legResultVersion)
	}
	return conductor.LegResult{FanOutID: rec.FanOutID, TaskID: rec.TaskID, LegIndex: rec.LegIndex, Attempt: rec.Attempt,
		RequestDigest: rec.RequestDigest, Sensitivity: rec.Sensitivity, Response: rec.Response}, true, nil
}

// DeleteTask removes every <fanoutID>#<n> record and nothing else: a key
// whose remainder after "<fanoutID>#" is not a canonical leg index (for
// example another fan-out "<fanoutID>#x#0") is left alone. Each leg with a
// stored result or an attempt slot goes through deleteLeg, so a finished
// leg's slots go before its record and an unfinished leg keeps its slots.
func (a *journalAppenderAdapter) DeleteTask(ctx context.Context, fanoutID string) error {
	if a.store == nil {
		return ErrLegStoreUnset
	}
	if !validFanOutID(fanoutID) {
		return cascade.Newf(cascade.KindInvalidInput, "resume: invalid fan-out id %q for DeleteTask", fanoutID)
	}
	legs := map[int]bool{}
	for _, ns := range []string{legResultsNamespace, legAttemptsNamespace} {
		if err := a.legIndexes(ctx, ns, fanoutID, legs); err != nil {
			return err
		}
	}
	for leg := range legs {
		if err := a.deleteLeg(ctx, conductor.LegResultKey(fanoutID, leg)); err != nil {
			return err
		}
	}
	return nil
}

// deleteLeg deletes one leg's attempt slots 1..maxLegStarts and then its
// result (R13 retention order). The slots go in one transaction that first
// reads the result: a result is stored only after the leg's call returned,
// so the leg is replayed and never starts again. An absent result is a leg
// that can still start, and nothing is deleted. A read or delete error
// rolls the slot transaction back, every slot in place. A failed result
// delete leaves a replayable result and no slot; the next sweep finishes.
func (a *journalAppenderAdapter) deleteLeg(ctx context.Context, key string) error {
	inFlight := false
	err := a.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		if _, err := tx.Get(ctx, legResultsNamespace, key); err != nil {
			inFlight = cascade.HasKind(err, cascade.KindNotFound)
			return err
		}
		for attempt := uint64(1); attempt <= maxLegStarts; attempt++ {
			if err := tx.Delete(ctx, legAttemptsNamespace, key+"#"+itoa(attempt)); err != nil {
				return err
			}
		}
		return nil
	})
	switch {
	case inFlight:
		return nil // keep the slots that enforce the start cap
	case err != nil:
		return rewrap(err, "resume: deleting the attempt slots of fan-out leg "+key)
	}
	return rewrap(a.store.Delete(ctx, legResultsNamespace, key), "resume: deleting fan-out leg result "+key)
}

// legIndexes adds to into the leg index of every key of namespace that
// belongs to fanoutID: "<fanoutID>#<n>" for results,
// "<fanoutID>#<n>#<attempt>" for attempt slots, n canonical.
func (a *journalAppenderAdapter) legIndexes(ctx context.Context, namespace, fanoutID string, into map[int]bool) error {
	prefix := fanoutID + "#"
	label := "fan-out leg results"
	if namespace == legAttemptsNamespace {
		label = "fan-out leg attempt slots"
	}
	it, err := a.store.Scan(ctx, namespace, prefix)
	if err != nil {
		return rewrap(err, "resume: listing "+label+" of "+fanoutID)
	}
	for it.Next(ctx) {
		rest := strings.TrimPrefix(it.Key(), prefix)
		if namespace == legAttemptsNamespace {
			rest, _, _ = strings.Cut(rest, "#")
		}
		if n, convErr := strconv.Atoi(rest); convErr == nil && n >= 0 && strconv.Itoa(n) == rest {
			into[n] = true
		}
	}
	iterErr := it.Err()
	closeErr := it.Close()
	if iterErr != nil {
		return rewrap(iterErr, "resume: listing "+label+" of "+fanoutID)
	}
	return rewrap(closeErr, "resume: closing the "+strings.TrimSuffix(label, "s")+" listing")
}

// validFanOutID mirrors the conductor's rule: non-empty, and no '#' or
// NUL, which would make record keys ambiguous across fan-outs.
func validFanOutID(id string) bool {
	return id != "" && !strings.ContainsAny(id, "#\x00")
}

// rewrap keeps err's kind (KindInternal when it has none) and cause and
// adds what failed; nil stays nil.
func rewrap(err error, what string) error {
	if err == nil {
		return nil
	}
	kind, ok := cascade.KindOf(err)
	if !ok {
		kind = cascade.KindInternal
	}
	return cascade.Wrap(kind, err, what)
}

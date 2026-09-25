// Purpose: the fleet side of contract:fanout-leg-results. One adapter
//   implements both conductor.JournalAppender (leg entries in the journal
//   entity FanOutEntity(fanoutID), per-leg attempts, the 3-start cap) and
//   conductor.LegResultStore (version-1 records, create-only, in namespace
//   conductor.fanout.legs under <fanoutID>#<legIndex>) over the
//   provider.Store the journal wraps, passed explicitly.
// Inputs: a journal.Store and a provider.Store (both required for the
//   LegResultStore role).
// Outputs: leg attempts, stored LegResults, or typed errors.
// Constraints: journal payloads carry the result key and the digest,
//   never Response content; every store error is returned; a record is
//   written create-only and never rewritten; DeleteTask removes one
//   fan-out's records and nothing else.
// SPORT: internal.fleet.resume.ResumeManager/CHANGE (P1-CORE-18).

package resume

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const (
	// legResultsNamespace holds every stored LegResult record.
	legResultsNamespace = "conductor.fanout.legs"
	// legResultVersion is the only record version this package reads.
	legResultVersion = 1
	// maxLegStarts is the per-leg start cap: a leg with this many starts
	// and no ok done is never dispatched again (unknown outcome).
	maxLegStarts = 3
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
	mu      sync.Mutex // serializes a start's count-then-append
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
// example another fan-out "<fanoutID>#x#0") is left alone.
func (a *journalAppenderAdapter) DeleteTask(ctx context.Context, fanoutID string) error {
	if a.store == nil {
		return ErrLegStoreUnset
	}
	if !validFanOutID(fanoutID) {
		return cascade.Newf(cascade.KindInvalidInput, "resume: invalid fan-out id %q for DeleteTask", fanoutID)
	}
	keys, err := a.taskKeys(ctx, fanoutID)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := a.store.Delete(ctx, legResultsNamespace, key); err != nil {
			return rewrap(err, "resume: deleting fan-out leg result "+key)
		}
	}
	return nil
}

// taskKeys lists fanoutID's own record keys.
func (a *journalAppenderAdapter) taskKeys(ctx context.Context, fanoutID string) ([]string, error) {
	prefix := fanoutID + "#"
	it, err := a.store.Scan(ctx, legResultsNamespace, prefix)
	if err != nil {
		return nil, rewrap(err, "resume: listing fan-out leg results of "+fanoutID)
	}
	var keys []string
	for it.Next(ctx) {
		rest := strings.TrimPrefix(it.Key(), prefix)
		if n, convErr := strconv.Atoi(rest); convErr == nil && n >= 0 && strconv.Itoa(n) == rest {
			keys = append(keys, it.Key())
		}
	}
	iterErr := it.Err()
	closeErr := it.Close()
	if iterErr != nil {
		return nil, rewrap(iterErr, "resume: listing fan-out leg results of "+fanoutID)
	}
	if closeErr != nil {
		return nil, rewrap(closeErr, "resume: closing the fan-out leg result listing")
	}
	return keys, nil
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

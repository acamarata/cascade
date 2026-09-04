// Purpose: the quarantine ledger's READ and RELEASE half - replay, List,
//
//	Get, Delete and PendingCount - split from quarantine.go under the
//	repo's 300-line file cap (Art.10.3). quarantine.go owns writing a
//	detection down; this file owns reading it back and retiring it.
//
// Inputs: the ledger file quarantine.go appends to.
// Outputs: QuarantineEntry values, sorted deterministically, and release
//
//	records that keep every exit from quarantine accounted.
//
// Constraints: a torn line costs one record, never the file (see replay);
//
//	no output is produced by iterating a map; nothing here can reach a
//	flagged value, because no value was ever stored.
//
// SPORT: QUARANTINE_STORE: ADD (internal/secrets.QuarantineStore reads).

package secrets

import (
	"bufio"
	"encoding/json"
	"os"
	"sort"

	"github.com/acamarata/cascade/pkg/cascade"
)

// replay reads the ledger and returns the live entries by id. A line it
// cannot decode is SKIPPED, not fatal: refusing to read the whole ledger
// because one line is torn would hide every other entry from the operator
// who needs to review them, and nothing here is ever overwritten, so a
// damaged line costs one record rather than the file.
func (q *QuarantineStore) replay() (map[string]QuarantineEntry, error) {
	live := map[string]QuarantineEntry{}
	f, err := os.Open(q.logPath()) //nolint:gosec // fixed name under the caller's data dir
	if err != nil {
		if os.IsNotExist(err) {
			return live, nil
		}
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "secrets: could not open the quarantine log")
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		var rec quarantineRecord
		if json.Unmarshal(scanner.Bytes(), &rec) != nil || rec.Entry.ID == "" {
			continue
		}
		if rec.Op == "release" {
			delete(live, rec.Entry.ID)
			continue
		}
		live[rec.Entry.ID] = rec.Entry
	}
	if err := scanner.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "secrets: could not read the quarantine log")
	}
	return live, nil
}

// List returns every live entry, sorted by detection time and then by id
// so the order never depends on map iteration.
func (q *QuarantineStore) List() ([]QuarantineEntry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	live, err := q.replay()
	if err != nil {
		return nil, err
	}
	out := make([]QuarantineEntry, 0, len(live))
	for _, entry := range live {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].DetectedAt.Equal(out[j].DetectedAt) {
			return out[i].DetectedAt.Before(out[j].DetectedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Get returns one live entry by id.
func (q *QuarantineStore) Get(id string) (QuarantineEntry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	live, err := q.replay()
	if err != nil {
		return QuarantineEntry{}, err
	}
	entry, ok := live[id]
	if !ok {
		return QuarantineEntry{}, ErrQuarantineNotFound(id)
	}
	return entry, nil
}

// Delete retires an entry, recording WHY. This is the way out of
// quarantine in both directions: ReleasePromoted after the value reaches
// the vault, ReleaseFalsePositive when the detector was wrong. The record
// stays in the ledger, so a release is accounted rather than erased.
func (q *QuarantineStore) Delete(id, reason string) error {
	if reason == "" {
		return cascade.New(cascade.KindInvalidInput,
			"secrets: releasing a quarantine entry requires a reason")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	live, err := q.replay()
	if err != nil {
		return err
	}
	entry, ok := live[id]
	if !ok {
		return ErrQuarantineNotFound(id)
	}
	return q.appendLocked(quarantineRecord{
		Op: "release", Reason: reason, At: q.clock.Now().UTC(), Entry: entry,
	})
}

// PendingCount reports how many entries are awaiting a decision. It is
// the read-only probe the doctor's quarantine-depth check consumes; it
// discloses a count, never a record.
func (q *QuarantineStore) PendingCount() (int, error) {
	entries, err := q.List()
	return len(entries), err
}

// ErrQuarantineNotFound reports that no live entry carries id. The id is
// safe to echo: it is a keyed digest of metadata, not a value.
func ErrQuarantineNotFound(id string) error {
	return cascade.Newf(cascade.KindNotFound, "secrets: no quarantine entry with id %q", id)
}

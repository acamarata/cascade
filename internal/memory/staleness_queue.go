package memory

// Purpose: the staleness-set file and its codec — one file per kind
//   holding the sorted addresses that scan flagged. Split from
//   staleness.go under the 300-line file cap.
// Inputs: a kind and the addresses a scan computed; raw file bytes on the
//   way back in.
// Outputs: canonical bytes under {base}/staleness/{kind}.json; the stored
//   address set, or a typed refusal.
// Constraints: fails closed on a malformed or unknown-version file; the
//   write is atomic and is SKIPPED when the bytes would be unchanged, so a
//   converged scan does no work; no map iteration reaches the file.
// SPORT: internal.memory.staleness.ScanStaleness (ADD, P1-E07-W2-S13-T4).

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"sort"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// stalenessFormatVersion is the queue format this build writes. An unknown
// version is refused rather than guessed at, for the reason every other
// codec in this package gives.
const stalenessFormatVersion = 1

// stalenessDir and stalenessSuffix are where and how a kind's set is
// filed, outside the record tree so nothing walking a kind's directory
// mistakes the queue for a memory.
const (
	stalenessDir    = "staleness"
	stalenessSuffix = ".staleness.json"
)

// Staleness queue sentinel errors.
var (
	// ErrMalformedStalenessSet is returned when a staleness-set file
	// exists but cannot be parsed whole.
	ErrMalformedStalenessSet = cascade.New(cascade.KindIntegrity,
		"malformed memory staleness set")
	// ErrUnsupportedStalenessFormat is the forward-compatibility refusal
	// for a staleness set written by a newer build.
	ErrUnsupportedStalenessFormat = cascade.New(cascade.KindUnsupported,
		"unsupported memory staleness set format version")
)

// stalenessSet is one kind's queue as stored.
type stalenessSet struct {
	// Format is the file format version.
	Format int `json:"format"`
	// Kind is the taxonomy member this set covers, repeated in the file so
	// a set read on its own is self-describing.
	Kind string `json:"kind"`
	// IDs are the stale records' canonical addresses, lexically ordered
	// and duplicate-free.
	IDs []string `json:"ids"`
	// ScannedAt is when the set was last computed, from the injected
	// clock.
	ScannedAt time.Time `json:"scanned_at"`
}

// stalenessPath returns the on-disk path of a kind's set.
func (s *StalenessScanner) stalenessPath(kind MemoryKind) string {
	return filepath.Join(s.base, stalenessDir, string(kind)+stalenessSuffix)
}

// loadQueue reads a kind's stored set. A missing file is an empty set,
// which is the honest state for a kind never scanned; an unreadable file
// is an error, never a silent empty, because treating it as empty would
// report every already-queued address as newly queued and wake every
// subscriber a second time.
func (s *StalenessScanner) loadQueue(kind MemoryKind) ([]string, error) {
	data, err := s.fs.ReadFile(s.stalenessPath(kind))
	if err != nil {
		if isNotExist(err) {
			return nil, nil
		}
		return nil, cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"reading staleness set for %s: %v", kind, err)
	}
	set, err := decodeStalenessSet(data)
	if err != nil {
		return nil, err
	}
	return set.IDs, nil
}

// saveQueue writes a kind's set atomically, and does nothing at all when
// the set is already exactly what is on disk.
//
// The no-op case is not an optimization. An unattended job that rewrites
// an unchanged file on every fire makes its own output indistinguishable
// from a job that is finding new work, and §5.9 requires the second run to
// be a genuine no-op rather than a run that merely reports one.
func (s *StalenessScanner) saveQueue(kind MemoryKind, ids []string, now time.Time) error {
	next := stalenessSet{Kind: string(kind), IDs: ids, ScannedAt: now}.canonical()
	data, err := encodeStalenessSet(next)
	if err != nil {
		return err
	}
	path := s.stalenessPath(kind)
	if existing, readErr := s.fs.ReadFile(path); readErr == nil {
		if stored, decErr := decodeStalenessSet(existing); decErr == nil &&
			equalIDs(stored.IDs, next.IDs) && bytes.Equal(existing, data) {
			return nil
		}
	}
	if err := s.fs.WriteAtomic(path, data); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"writing staleness set for %s: %v", kind, err)
	}
	return nil
}

// equalIDs reports whether two sorted address lists hold the same members.
func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// canonical puts the set in its canonical form: the declared version, UTC
// timestamp, and a sorted, duplicate-free address list, so the same set
// always encodes to the same bytes.
func (s stalenessSet) canonical() stalenessSet {
	out := s
	out.Format = stalenessFormatVersion
	out.ScannedAt = s.ScannedAt.UTC()
	ids := sortedUnique(s.IDs)
	if ids == nil {
		ids = []string{}
	}
	sort.Strings(ids)
	out.IDs = ids
	return out
}

// encodeStalenessSet renders a set as indented JSON, readable for the same
// reason the consolidation record is: a person may want to see what the
// heuristic flagged without a tool.
func encodeStalenessSet(s stalenessSet) ([]byte, error) {
	data, err := json.MarshalIndent(s.canonical(), "", "  ")
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindIntegrity, ErrMalformedStalenessSet,
			"encoding staleness set for %s: %v", s.Kind, err)
	}
	return append(data, '\n'), nil
}

// decodeStalenessSet parses a set, failing closed on anything it cannot
// read whole and refusing an unknown format version separately.
func decodeStalenessSet(data []byte) (stalenessSet, error) {
	var set stalenessSet
	if err := json.Unmarshal(data, &set); err != nil {
		return stalenessSet{}, cascade.Wrapf(cascade.KindIntegrity,
			ErrMalformedStalenessSet, "parsing staleness set: %v", err)
	}
	if set.Format != stalenessFormatVersion {
		return stalenessSet{}, cascade.Wrapf(cascade.KindUnsupported,
			ErrUnsupportedStalenessFormat,
			"staleness set declares format %d, this build writes %d",
			set.Format, stalenessFormatVersion)
	}
	if _, err := ParseKind(set.Kind); err != nil {
		return stalenessSet{}, cascade.Wrapf(cascade.KindIntegrity,
			ErrMalformedStalenessSet, "staleness set names no valid kind: %v", err)
	}
	return set.canonical(), nil
}

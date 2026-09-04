package memory

// Purpose: the read-only half of FileCandidateLedger: reading one
//   candidate and listing the names of a kind's candidates. Split from
//   ledger_store.go per the 300-line file cap.
// Inputs: a kind and, for a read, a candidate name.
// Outputs: a candidate view or a lexically ordered name list, and typed
//   pkg/cascade errors.
// Constraints: listing parses no record, so one damaged candidate can
//   never make a kind unlistable or hide the candidates beside it.
// SPORT: G/memory-candidate-ledger (ADD, placeholder per T-1
//   sport_updates).

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Get returns one candidate's current state.
func (l *FileCandidateLedger) Get(
	ctx context.Context, kind MemoryKind, name string,
) (CandidateEntry, error) {
	if err := ctx.Err(); err != nil {
		return CandidateEntry{}, cascade.Wrap(cascade.KindCanceled, err, "candidate read canceled")
	}
	rec, err := l.mustLoad(kind, name)
	if err != nil {
		return CandidateEntry{}, err
	}
	return rec.view(), nil
}

// List returns the names of every candidate of a kind, lexically ordered.
//
// It reads directory names and parses nothing. A candidate whose file this
// build cannot read still appears here, which is deliberate: a damaged
// record has to stay visible to be inspected and removed, and one bad file
// must never make the rest of the kind invisible.
func (l *FileCandidateLedger) List(ctx context.Context, kind MemoryKind) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindCanceled, err, "candidate list canceled")
	}
	if !kind.Valid() {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidKind,
			"unknown memory kind %q", string(kind))
	}
	names, err := l.fs.ReadDirNames(filepath.Join(l.base, candidatesDir, string(kind)))
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"listing memory candidates of kind %s: %v", kind, err)
	}
	return candidateNames(names), nil
}

// candidateNames turns a directory listing into the sorted set of
// candidate names, ignoring anything that is not a candidate file or whose
// stem is not a usable record name.
func candidateNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		stem, ok := strings.CutSuffix(n, candidateSuffix)
		if !ok || ValidateName(stem) != nil {
			continue
		}
		out = append(out, stem)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

// Purpose: the sessions domain's read-side listing: Filter, its matcher
// and Store.List, split out of domain.go for the 300-line cap
// (P1-HRN-08). Behaviour is unchanged from domain.go.

package sessions

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Filter selects a subset of List's results. A nil/zero field matches
// every value for that column; all non-nil fields must match (AND).
type Filter struct {
	Harness         *string
	Account         *string
	State           *string
	ParentSessionID *string
}

// List returns every record matching filter, in session_id order.
func (s *Store) List(ctx context.Context, filter Filter) ([]SessionRecord, error) {
	it, err := s.store.Scan(ctx, DefaultNamespace, sessionKeyPrefix)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "sessions: scanning sessions")
	}
	defer func() { _ = it.Close() }()

	var out []SessionRecord
	for it.Next(ctx) {
		var rec SessionRecord
		if uerr := json.Unmarshal(it.Value(), &rec); uerr != nil {
			return nil, cascade.Wrapf(cascade.KindIntegrity, uerr, "sessions: decoding key %q", it.Key())
		}
		if filter.matches(rec) {
			out = append(out, rec)
		}
	}
	if err := it.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "sessions: iterating sessions")
	}
	return out, nil
}

func (f Filter) matches(rec SessionRecord) bool {
	if f.Harness != nil && rec.Harness != *f.Harness {
		return false
	}
	if f.Account != nil && rec.Account != *f.Account {
		return false
	}
	if f.State != nil && rec.State != *f.State {
		return false
	}
	if f.ParentSessionID != nil {
		if rec.ParentSessionID == nil || *rec.ParentSessionID != *f.ParentSessionID {
			return false
		}
	}
	return true
}

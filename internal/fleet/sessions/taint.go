// Purpose: the sticky session taint (contract:session-taint, P1-HRN-08).
// MarkUntrusted flags a session once untrusted content reaches it; Taint
// reads that flag back by session id.
//
// Inputs: a session id and, for MarkUntrusted, a provenance reference.
// Outputs: TaintState values, or a pkg/cascade taxonomy error.
// Constraints: no method clears a taint. The first mark stamps SinceMs
// from the injected clock and keeps its ProvenanceRef; later marks change
// nothing. Readers must treat every Taint error as tainted.

package sessions

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TaintState is the read view of a session's sticky taint.
type TaintState struct {
	// Tainted is true once MarkUntrusted has run for the session.
	Tainted bool `json:"tainted"`
	// SinceMs is the Unix-millisecond time of the first mark (0 if clean).
	SinceMs int64 `json:"since_ms"`
	// ProvenanceRef names the content that caused the first mark.
	ProvenanceRef string `json:"provenance_ref"`
}

// MarkUntrusted flags session id as untrusted. The mark is sticky: the
// first call stamps the time and provenanceRef, later calls keep both.
// An unknown id returns ErrNotFound (KindNotFound).
func (s *Store) MarkUntrusted(ctx context.Context, id, provenanceRef string) error {
	if id == "" {
		return cascade.Wrap(cascade.KindInvalidInput, ErrInvalidRecord, "sessions: MarkUntrusted requires a session id")
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	rec, err := s.getLocked(ctx, id)
	if err != nil {
		return err
	}
	if rec.Untrusted {
		return nil
	}
	now := s.clock.Now().UnixMilli()
	rec.Untrusted, rec.UntrustedSince, rec.UntrustedRef = true, &now, provenanceRef
	return s.putLocked(ctx, rec)
}

// Taint returns the taint state of session id. An unknown id returns
// ErrNotFound (KindNotFound). On any error the returned state reports
// Tainted true, so a caller that ignores the error still fails closed.
func (s *Store) Taint(ctx context.Context, id string) (TaintState, error) {
	if id == "" {
		return TaintState{Tainted: true}, cascade.Wrap(cascade.KindInvalidInput, ErrInvalidRecord, "sessions: Taint requires a session id")
	}
	rec, err := s.getLocked(ctx, id)
	if err != nil {
		return TaintState{Tainted: true}, err
	}
	st := TaintState{Tainted: rec.Untrusted, ProvenanceRef: rec.UntrustedRef}
	if rec.UntrustedSince != nil {
		st.SinceMs = *rec.UntrustedSince
	}
	return st, nil
}

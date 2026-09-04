package memory

// Purpose: the on-disk candidate record and its codec. This file IS the
//   candidate format contract: a change here that cannot read what an
//   older build wrote loses accumulated evidence, which is the same class
//   of loss the record store's frontmatter codec exists to prevent.
// Inputs: a candidate record to encode; raw file bytes to decode.
// Outputs: canonical bytes; a fully-populated record, or a typed refusal.
// Constraints: deterministic (fixed struct field order, sorted session
//   list, UTC timestamps, no map reaching the output); fails closed on
//   every malformed, truncated or unknown-version input and never returns
//   a half-populated record beside an error.
// SPORT: G/memory-candidate-ledger (ADD, placeholder per T-1
//   sport_updates).

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// candidateFormatVersion is the record format this build writes. A file
// declaring any other version is refused with
// ErrUnsupportedCandidateFormat rather than read on a best-effort basis,
// for the reason the record store gives: guessing at a layout written by a
// build that knew more than this one is how evidence gets silently
// dropped, and a wrong guess here promotes a false belief.
const candidateFormatVersion = 1

// candidateSuffix is the file shape a candidate takes. Candidate records
// are machine bookkeeping rather than prose a person edits, so they are
// JSON and not the markdown-with-frontmatter shape a memory record uses.
// The discipline is the same: one file per record, a declared format
// version, an atomic write, and a fail-closed read.
const candidateSuffix = ".candidate.json"

// candidatesDir is the subdirectory of the store base that holds
// candidates, keeping them out of the record tree so a candidate is never
// mistaken for a durable record by anything walking it.
const candidatesDir = "candidates"

// candidateDraft is the durable record a candidate becomes when promoted,
// stored beside its counts. Identity (kind and name) is not repeated here:
// it lives on the record and is the file's own location.
type candidateDraft struct {
	Description string     `json:"description"`
	Body        string     `json:"body"`
	ScopeRef    string     `json:"scope_ref"`
	CommitSHA   string     `json:"commit_sha"`
	Supersedes  string     `json:"supersedes"`
	Confidence  float64    `json:"confidence"`
	Origin      string     `json:"origin"`
	SessionID   string     `json:"session_id"`
	ExpiresAt   *time.Time `json:"expires_at"`
}

// candidateRecord is one candidate as stored.
//
// It holds strictly more than CandidateEntry exposes: the draft a
// promotion writes, and the revert history. The history is kept rather
// than cleared because a promotion a user later asks about has to remain
// accountable; a reverted candidate that simply lost its counts would
// leave no answer to "why did it once believe that".
type candidateRecord struct {
	Format       int            `json:"format"`
	Name         string         `json:"name"`
	Kind         string         `json:"kind"`
	Status       string         `json:"status"`
	RefCount     int            `json:"ref_count"`
	SessionIDs   []string       `json:"session_ids"`
	FirstSeen    time.Time      `json:"first_seen"`
	UpdatedAt    time.Time      `json:"updated_at"`
	PromotedAt   *time.Time     `json:"promoted_at"`
	RevertedAt   *time.Time     `json:"reverted_at"`
	RevertReason string         `json:"revert_reason"`
	SnoozeUntil  *time.Time     `json:"snooze_until"`
	Draft        candidateDraft `json:"draft"`
}

// canonical returns a copy of r with everything that reaches disk in its
// canonical form: timestamps in UTC so a record written in one time zone
// compares equal when read in another, and the session list sorted and
// duplicate-free so identical evidence always produces identical bytes.
func (r candidateRecord) canonical() candidateRecord {
	out := r
	out.Format = candidateFormatVersion
	out.FirstSeen = r.FirstSeen.UTC()
	out.UpdatedAt = r.UpdatedAt.UTC()
	out.PromotedAt = utcPtr(r.PromotedAt)
	out.RevertedAt = utcPtr(r.RevertedAt)
	out.SnoozeUntil = utcPtr(r.SnoozeUntil)
	out.Draft.ExpiresAt = utcPtr(r.Draft.ExpiresAt)
	out.SessionIDs = sortedUnique(r.SessionIDs)
	return out
}

// utcPtr returns a new pointer to t in UTC, or nil. The copy matters: the
// caller keeps its own pointer, so a later edit through it cannot reach
// what was stored.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// sortedUnique returns the distinct members of in, lexically ordered. The
// result is always a fresh slice, so nothing shares backing memory with
// the caller's input.
func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// view returns the caller-visible projection of the record. The slice and
// the pointers it returns are copies, so a caller cannot reach back into
// ledger state through them.
func (r candidateRecord) view() CandidateEntry {
	ids := make([]string, len(r.SessionIDs))
	copy(ids, r.SessionIDs)
	if len(ids) == 0 {
		ids = nil
	}
	return CandidateEntry{
		Name:        r.Name,
		Kind:        MemoryKind(r.Kind),
		SessionIDs:  ids,
		RefCount:    r.RefCount,
		PromotedAt:  utcPtr(r.PromotedAt),
		SnoozeUntil: utcPtr(r.SnoozeUntil),
		Status:      CandidateStatus(r.Status),
	}
}

// entry returns the durable record this candidate is promoted into.
func (r candidateRecord) entry() MemoryEntry {
	return MemoryEntry{
		Name:        r.Name,
		Kind:        MemoryKind(r.Kind),
		Description: r.Draft.Description,
		Body:        r.Draft.Body,
		ScopeRef:    r.Draft.ScopeRef,
		CommitSHA:   r.Draft.CommitSHA,
		Supersedes:  r.Draft.Supersedes,
		ExpiresAt:   utcPtr(r.Draft.ExpiresAt),
		Confidence:  r.Draft.Confidence,
		Provenance: Provenance{
			Origin:    Origin(r.Draft.Origin),
			SessionID: r.Draft.SessionID,
		},
	}
}

// draftOf reduces a caller's draft record to the fields the candidate
// stores. Identity is dropped here on purpose: it is the file's location
// and repeating it in two places invites the two to disagree.
func draftOf(e MemoryEntry) candidateDraft {
	return candidateDraft{
		Description: e.Description,
		Body:        e.Body,
		ScopeRef:    e.ScopeRef,
		CommitSHA:   e.CommitSHA,
		Supersedes:  e.Supersedes,
		Confidence:  e.Confidence,
		Origin:      string(e.Provenance.Origin),
		SessionID:   e.Provenance.SessionID,
		ExpiresAt:   utcPtr(e.ExpiresAt),
	}
}

// encodeCandidate renders r as the canonical on-disk bytes. It takes an
// already-canonical record; encoding does not silently repair one, because
// a codec that fixes its input hides the caller's bug until the day the
// file is read somewhere else.
func encodeCandidate(r candidateRecord) ([]byte, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindInternal, ErrMalformedCandidate,
			"encoding candidate %s/%s: %v", r.Kind, r.Name, err)
	}
	return append(data, '\n'), nil
}

// decodeCandidate parses stored bytes back into a record.
//
// The format version is read first, from a shape that ignores every other
// field, so a record written by a newer build is refused as unsupported
// rather than as damaged even when its other fields are unreadable here.
// Everything after that is checked before anything is returned: a record
// whose kind or status this build does not recognize is refused whole,
// never mapped to a default, because defaulting a status is how a promoted
// candidate would be re-promoted from zero.
func decodeCandidate(data []byte) (candidateRecord, error) {
	var probe struct {
		Format *int `json:"format"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return candidateRecord{}, cascade.Wrapf(cascade.KindIntegrity, ErrMalformedCandidate,
			"candidate record is not valid JSON: %v", err)
	}
	if probe.Format == nil {
		return candidateRecord{}, cascade.Wrapf(cascade.KindIntegrity, ErrMalformedCandidate,
			"candidate record declares no format version")
	}
	if *probe.Format != candidateFormatVersion {
		return candidateRecord{}, cascade.Wrapf(cascade.KindUnsupported, ErrUnsupportedCandidateFormat,
			"candidate record format version %d, this build reads %d",
			*probe.Format, candidateFormatVersion)
	}
	var r candidateRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return candidateRecord{}, cascade.Wrapf(cascade.KindIntegrity, ErrMalformedCandidate,
			"candidate record fields are unreadable: %v", err)
	}
	if err := validateCandidateRecord(r); err != nil {
		return candidateRecord{}, err
	}
	return r.canonical(), nil
}

// validateCandidateRecord refuses a stored record this build must not act
// on. Every check here is a promotion-safety check: acting on a record
// whose status, kind or counts are not intelligible risks writing a
// durable belief from evidence nobody can read.
func validateCandidateRecord(r candidateRecord) error {
	if err := checkKey(MemoryKind(r.Kind), r.Name); err != nil {
		return cascade.Wrapf(cascade.KindIntegrity, ErrMalformedCandidate,
			"candidate record identity %q/%q is not usable: %v", r.Kind, r.Name, err)
	}
	if _, err := ParseCandidateStatus(r.Status); err != nil {
		return cascade.Wrapf(cascade.KindIntegrity, ErrMalformedCandidate,
			"candidate record has unknown status %q", r.Status)
	}
	if r.RefCount < 0 {
		return cascade.Wrapf(cascade.KindIntegrity, ErrMalformedCandidate,
			"candidate record has a negative reference count (%d)", r.RefCount)
	}
	if r.RefCount < len(sortedUnique(r.SessionIDs)) {
		return cascade.Wrapf(cascade.KindIntegrity, ErrMalformedCandidate,
			"candidate record counts %d references across %d distinct sessions",
			r.RefCount, len(sortedUnique(r.SessionIDs)))
	}
	return nil
}

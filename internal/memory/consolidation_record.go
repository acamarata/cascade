package memory

// Purpose: the consolidation record — the on-disk account of what the
//   consolidation job retired and where it went. This file IS the answer
//   to "what happened to the thing I remember saying", so it holds every
//   retired member whole, not merely a count.
// Inputs: a planned group; raw file bytes on the way back in.
// Outputs: canonical, deterministic bytes under
//   {base}/consolidations/{kind}/{survivor}.consolidation.json; a fully
//   populated record, or a typed refusal.
// Constraints: fails closed on a malformed or unknown-version file rather
//   than overwriting it; a second consolidation into the same survivor
//   UNIONS with what is already recorded instead of replacing it; the
//   encoding is byte-stable for the same inputs, so an unchanged record is
//   not rewritten.
// SPORT: internal.memory.consolidation.ConsolidationRecord (ADD,
//   P1-E07-W2-S13-T4).

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"sort"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// consolidationFormatVersion is the record format this build writes. A
// file declaring any other version is refused rather than read on a
// best-effort basis: this record is the only surviving account of records
// that were removed, and guessing at a layout a newer build wrote is how
// that account gets silently truncated.
const consolidationFormatVersion = 1

// consolidationsDir and consolidationSuffix are where and how a record is
// filed. They sit outside the record tree so nothing walking a kind's
// directory mistakes a consolidation record for a memory.
const (
	consolidationsDir   = "consolidations"
	consolidationSuffix = ".consolidation.json"
)

// RetiredMember is one record the consolidation job removed, captured in
// full.
//
// Every field a MemoryEntry carries is here except the body, which is held
// once on the record itself: the grouping rule is that all members'
// normalized bodies are byte-identical, so one copy is the whole content
// of every member. Together, a RetiredMember and the record's Body
// reconstruct exactly what was removed.
type RetiredMember struct {
	// ID is the retired record's canonical "<kind>/<name>" address.
	ID string `json:"id"`
	// Description is the retired record's one-line summary. It is kept
	// per-member because two records with the same body may carry
	// different descriptions, and a merge must not lose the one that did
	// not survive.
	Description string `json:"description"`
	// ScopeRef, CommitSHA and Supersedes are the retired record's
	// R-16.7 reference fields.
	ScopeRef   string `json:"scope_ref"`
	CommitSHA  string `json:"commit_sha,omitempty"`
	Supersedes string `json:"supersedes,omitempty"`
	// Confidence is how much the retired record was trusted.
	Confidence float64 `json:"confidence"`
	// Origin and SessionID are its provenance references.
	Origin    string `json:"origin"`
	SessionID string `json:"session_id,omitempty"`
	// CreatedAt and UpdatedAt are its stored timestamps, in UTC.
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// ContentHash is the body digest the store last wrote for it.
	ContentHash string `json:"content_hash"`
	// ExpiresAt is its TTL, or nil.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// ConsolidationRecord is the durable account of every consolidation that
// has ever retired a record into one survivor.
//
// It accumulates. A later run that retires another duplicate into the same
// survivor unions its members into this record rather than replacing it,
// so the file always names every record that was ever consolidated away
// into that address, not just the most recent batch.
type ConsolidationRecord struct {
	// Format is the record format version.
	Format int `json:"format"`
	// ConsolidatedID is the surviving record's canonical address.
	ConsolidatedID string `json:"consolidated_id"`
	// Method is how the groups were formed.
	Method string `json:"method"`
	// Body is the content every member of the group shared, kept verbatim
	// so a retired record is reconstructible from this file plus its
	// RetiredMember entry even if the survivor is later edited or forgotten.
	Body string `json:"body"`
	// Members are the retired records, in canonical-address order.
	Members []RetiredMember `json:"members"`
	// FirstConsolidatedAt and LastConsolidatedAt bound when this record's
	// retirements happened, from the injected clock.
	FirstConsolidatedAt time.Time `json:"first_consolidated_at"`
	LastConsolidatedAt  time.Time `json:"last_consolidated_at"`
}

// consolidationPath returns the on-disk path of a survivor's record. Kind
// and name come from a record already read out of the store, so both are
// valid path segments and the result is always inside base.
func (c *Consolidator) consolidationPath(kind MemoryKind, name string) string {
	return filepath.Join(c.base, consolidationsDir, string(kind), name+consolidationSuffix)
}

// recordGroup writes (or extends) the consolidation record for one group.
//
// It reads any existing record first and unions the new members into it.
// If the resulting bytes are identical to what is already on disk the file
// is NOT rewritten: a re-run over a tree that is already in the intended
// state must do no work, and rewriting a byte-identical file would both
// churn the disk and move a timestamp that nothing changed.
func (c *Consolidator) recordGroup(g duplicateGroup, now time.Time) error {
	path := c.consolidationPath(g.survivor.Kind, g.survivor.Name)
	existing, found, err := c.loadRecord(path)
	if err != nil {
		return err
	}
	next := mergeRecord(existing, found, g, now)
	data, err := encodeConsolidation(next)
	if err != nil {
		return err
	}
	if found {
		old, encErr := encodeConsolidation(existing)
		if encErr == nil && bytes.Equal(old, data) {
			return nil
		}
	}
	if err := c.fs.WriteAtomic(path, data); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"writing consolidation record for %s: %v", entryID(g.survivor), err)
	}
	return nil
}

// loadRecord reads a survivor's existing record. found is false only when
// no file is there; an unreadable file is an error, never a silent
// absence, because overwriting the account of an earlier consolidation
// would destroy the only trace of records already removed.
func (c *Consolidator) loadRecord(path string) (ConsolidationRecord, bool, error) {
	data, err := c.fs.ReadFile(path)
	if err != nil {
		if isNotExist(err) {
			return ConsolidationRecord{}, false, nil
		}
		return ConsolidationRecord{}, false, cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"reading consolidation record %s: %v", filepath.Base(path), err)
	}
	rec, err := decodeConsolidation(data)
	if err != nil {
		return ConsolidationRecord{}, false, err
	}
	return rec, true, nil
}

// mergeRecord unions g's retirees into rec, keeping the earliest first
// instant and advancing the last.
func mergeRecord(rec ConsolidationRecord, found bool, g duplicateGroup, now time.Time) ConsolidationRecord {
	out := rec
	if !found {
		out = ConsolidationRecord{FirstConsolidatedAt: now}
	}
	out.ConsolidatedID = entryID(g.survivor)
	out.Method = ConsolidationMethodExactHash
	out.Body = g.survivor.Body
	out.LastConsolidatedAt = now
	byID := map[string]RetiredMember{}
	for _, m := range out.Members {
		byID[m.ID] = m
	}
	for _, m := range g.retired {
		byID[entryID(m)] = retiredMemberOf(m)
	}
	out.Members = sortedMembers(byID)
	return out.canonical()
}

// sortedMembers flattens the member set into canonical-address order, so
// no map walk ever reaches the encoded bytes.
func sortedMembers(byID map[string]RetiredMember) []RetiredMember {
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]RetiredMember, 0, len(ids))
	for _, id := range ids {
		out = append(out, byID[id])
	}
	return out
}

// retiredMemberOf captures one record about to be removed.
func retiredMemberOf(e MemoryEntry) RetiredMember {
	return RetiredMember{
		ID: entryID(e), Description: e.Description, ScopeRef: e.ScopeRef,
		CommitSHA: e.CommitSHA, Supersedes: e.Supersedes, Confidence: e.Confidence,
		Origin: string(e.Provenance.Origin), SessionID: e.Provenance.SessionID,
		CreatedAt: e.Provenance.CreatedAt.UTC(), UpdatedAt: e.Provenance.UpdatedAt.UTC(),
		ContentHash: e.Provenance.ContentHash, ExpiresAt: utcPtr(e.ExpiresAt),
	}
}

// canonical puts everything that reaches disk in its canonical form, so
// the same inputs always produce the same bytes.
func (r ConsolidationRecord) canonical() ConsolidationRecord {
	out := r
	out.Format = consolidationFormatVersion
	out.FirstConsolidatedAt = r.FirstConsolidatedAt.UTC()
	out.LastConsolidatedAt = r.LastConsolidatedAt.UTC()
	members := make([]RetiredMember, len(r.Members))
	copy(members, r.Members)
	for i := range members {
		members[i].CreatedAt = members[i].CreatedAt.UTC()
		members[i].UpdatedAt = members[i].UpdatedAt.UTC()
		members[i].ExpiresAt = utcPtr(members[i].ExpiresAt)
	}
	out.Members = members
	return out
}

// encodeConsolidation renders a record as indented JSON. Indented because
// this file is the one a person reads when they want to know what happened
// to a memory, and a wall of minified JSON is a worse answer than a
// readable one.
func encodeConsolidation(r ConsolidationRecord) ([]byte, error) {
	data, err := json.MarshalIndent(r.canonical(), "", "  ")
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindIntegrity, ErrMalformedConsolidation,
			"encoding consolidation record %s: %v", r.ConsolidatedID, err)
	}
	return append(data, '\n'), nil
}

// decodeConsolidation parses a record, failing closed on anything it
// cannot read whole and refusing an unknown format version separately from
// a damaged file.
func decodeConsolidation(data []byte) (ConsolidationRecord, error) {
	var rec ConsolidationRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return ConsolidationRecord{}, cascade.Wrapf(cascade.KindIntegrity,
			ErrMalformedConsolidation, "parsing consolidation record: %v", err)
	}
	if rec.Format != consolidationFormatVersion {
		return ConsolidationRecord{}, cascade.Wrapf(cascade.KindUnsupported,
			ErrUnsupportedConsolidationFormat,
			"consolidation record declares format %d, this build writes %d",
			rec.Format, consolidationFormatVersion)
	}
	if rec.ConsolidatedID == "" {
		return ConsolidationRecord{}, cascade.Wrapf(cascade.KindIntegrity,
			ErrMalformedConsolidation, "consolidation record names no surviving record")
	}
	return rec.canonical(), nil
}

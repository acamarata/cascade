package memory

// Purpose: the SOUL's machine-managed half — the on-disk ledger carrying
//   the version counter, the content digest, the reconcile pointer and the
//   audit log, plus its codec. This file IS the ledger format contract: a
//   change here that cannot read what an older build wrote loses the
//   version history of the system's model of the user.
// Inputs: ledger bytes from disk; a ledger value to persist.
// Outputs: a fully-populated ledger, or a typed refusal.
// Constraints: fails closed on every malformed, truncated or
//   unknown-version input, and never returns a half-populated ledger
//   beside an error; deterministic encoding (fixed struct field order, no
//   map reaching the file).
// SPORT: G/memory-soul-store (ADD, placeholder per T-2 sport_updates).

import (
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// soulLedger is the SOUL's machine-managed half as stored.
type soulLedger struct {
	// Format is the layout version of this file.
	Format int `json:"format"`
	// Version is the monotonic version counter, 0 before the first write.
	Version int `json:"version"`
	// Schema is the document schema as of the last write.
	Schema string `json:"schema"`
	// ContentHash is the BLAKE3 digest of the body the store last wrote
	// or last adopted.
	ContentHash string `json:"content_hash"`
	// LastReconciledVersion is the version as of the last time the store
	// adopted the file's own content (route b). A Version greater than
	// this one means the store has written since the file was last known
	// to agree with it, which is exactly the condition that turns a
	// changed file into a conflict rather than a clean external edit.
	LastReconciledVersion int `json:"last_reconciled_version"`
	// UpdatedAt is when the ledger was last written, from the clock.
	UpdatedAt time.Time `json:"updated_at"`
	// Entries is the audit log in version order. It carries no soul
	// content; see AuditEntry.
	Entries []AuditEntry `json:"entries"`
}

// document returns the ledger's view of the stored document, given the
// body that was read from disk.
func (l soulLedger) document(body string) SoulDocument {
	return SoulDocument{Body: body, Schema: l.Schema}.canonical()
}

// loadLedger reads the ledger. A missing ledger is the empty ledger, which
// is the legitimate "nothing written yet" state; a damaged or
// unknown-version one is an error, never a silent reset, because treating
// an unreadable ledger as absent would restart the version counter at zero
// and drop every audit entry it held.
func (s *FileSoulStore) loadLedger() (soulLedger, error) {
	data, err := s.fs.ReadFile(s.ledgerPath())
	if err != nil {
		if isNotExist(err) {
			return soulLedger{Format: soulFormatVersion}, nil
		}
		return soulLedger{}, cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"reading soul ledger: %v", err)
	}
	return decodeSoulLedger(data)
}

// decodeSoulLedger parses a ledger, failing closed on everything it cannot
// read whole and distinguishing a damaged file from one written by a build
// this one does not understand.
func decodeSoulLedger(data []byte) (soulLedger, error) {
	var probe struct {
		Format *int `json:"format"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return soulLedger{}, cascade.Wrapf(cascade.KindIntegrity, ErrMalformedSoulLedger,
			"decoding soul ledger: %v", err)
	}
	if probe.Format == nil {
		return soulLedger{}, cascade.Wrapf(cascade.KindIntegrity, ErrMalformedSoulLedger,
			"soul ledger declares no format version")
	}
	if *probe.Format != soulFormatVersion {
		return soulLedger{}, cascade.Wrapf(cascade.KindUnsupported, ErrUnsupportedSoulFormat,
			"soul ledger format %d, this build writes %d", *probe.Format, soulFormatVersion)
	}
	var l soulLedger
	if err := json.Unmarshal(data, &l); err != nil {
		return soulLedger{}, cascade.Wrapf(cascade.KindIntegrity, ErrMalformedSoulLedger,
			"decoding soul ledger: %v", err)
	}
	return l, validateLedgerEntries(l)
}

// validateLedgerEntries refuses a log that does not read as one: an
// unknown route, or versions out of order. A log that cannot be trusted to
// say what happened is worse than one that refuses to be read.
func validateLedgerEntries(l soulLedger) error {
	for i, e := range l.Entries {
		// ParseSoulEditRoute, not Route.Valid(), because this is external
		// input: the ledger is a file a user or another build may have
		// written, and the parse is where an unknown route is refused
		// rather than mapped onto something nearby.
		if _, err := ParseSoulEditRoute(string(e.Route)); err != nil {
			return cascade.Wrapf(cascade.KindIntegrity, ErrMalformedSoulLedger,
				"soul audit entry %d names unknown route %q: %v", i, string(e.Route), err)
		}
		if e.Version != i+1 {
			return cascade.Wrapf(cascade.KindIntegrity, ErrMalformedSoulLedger,
				"soul audit entry %d declares version %d, expected %d", i, e.Version, i+1)
		}
	}
	if len(l.Entries) > l.Version {
		return cascade.Wrapf(cascade.KindIntegrity, ErrMalformedSoulLedger,
			"soul ledger holds %d entries at version %d", len(l.Entries), l.Version)
	}
	return nil
}

// persistLedger writes the ledger atomically.
func (s *FileSoulStore) persistLedger(l soulLedger) error {
	data, err := json.Marshal(l)
	if err != nil {
		return cascade.Wrapf(cascade.KindInternal, ErrMalformedSoulLedger,
			"encoding soul ledger: %v", err)
	}
	if err := s.fs.WriteAtomic(s.ledgerPath(), data); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"writing soul ledger: %v", err)
	}
	return nil
}

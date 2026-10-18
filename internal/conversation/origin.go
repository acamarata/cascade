package conversation

// Purpose: the untrusted-origin marker on a chat turn -- which host binding
//   stamped it (Origin) and whether the user vouched for it (Provenance) --
//   persisted in conversation_turn_origin in the same transaction as the
//   turn and returned by every read path (chat.get_thread, chat.search,
//   journal resume).
// Inputs: a chat.append_turn request's origin/untrusted wire fields, a
//   Turn handed to Store.AppendTurn, or a stored row.
// Outputs: the validated marker, the origin-row INSERT, and the turn
//   SELECT that joins the marker back on.
// Constraints: a LABEL, never an authority (R-14.303). Absence means a
//   local caller (Origin "", Provenance trusted); the marker only ever
//   narrows. A bridge origin is never stored as trusted, an unparsable
//   stored provenance reads as untrusted, and no read, resume, export or
//   journal path clears or rewrites a row. The Provenance type is
//   provider.Provenance (contract:untrusted-provenance); this file
//   declares no provenance, trust or sensitivity type of its own.
// SPORT: internal.conversation.origin/ADDED (contract:chat-turn-origin).

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// The closed Origin set. OriginLocal (the empty string) is the absent
// marker: a caller on the daemon's own socket. The bridge values are
// stamped by the host's bridge bindings only; a plugin or a message
// payload never supplies one.
const (
	OriginLocal          = ""
	OriginBridgeTelegram = "bridge-telegram"
	OriginBridgeWhatsApp = "bridge-whatsapp"
)

// tableTurnOrigin holds one row per marked turn. A turn with no row is a
// local, trusted turn.
const tableTurnOrigin = "conversation_turn_origin"

// turnSelect is the one SELECT every Turn reader shares: the turn columns
// plus the optional marker row. Callers append their own WHERE/ORDER BY
// using the t. alias.
const turnSelect = `SELECT t.id, t.thread_id, t.seq, t.role, t.created_at, o.origin, o.provenance FROM ` +
	tableTurn + ` t LEFT JOIN ` + tableTurnOrigin + ` o ON o.turn_id = t.id`

// originStep is MigrationSet's step for the marker table. A function
// rather than a literal inline in schema.go, matching privacyStep.
func originStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "conversation_turn_origin: untrusted-origin marker, one row per marked turn",
		Table: &migrate.TableDef{
			Name:        tableTurnOrigin,
			Columns:     []migrate.ColumnDef{id("turn_id"), text("origin"), text("provenance")},
			ForeignKeys: []migrate.ForeignKeyDef{fk("turn_id", tableTurn)},
		},
	}
}

// validOrigin reports whether origin is a member of the closed set.
func validOrigin(origin string) bool {
	switch origin {
	case OriginLocal, OriginBridgeTelegram, OriginBridgeWhatsApp:
		return true
	}
	return false
}

// resolveMarker validates a wire/stored origin plus untrusted flag and
// returns the Provenance to store. An origin outside the closed set, or a
// bridge origin with untrusted=false, is refused (KindInvalidInput, static
// message: the offending value is never echoed). A local origin with
// untrusted=true is accepted and stored untrusted.
func resolveMarker(origin string, untrusted bool) (provider.Provenance, error) {
	if !validOrigin(origin) {
		return "", cascade.New(cascade.KindInvalidInput, "conversation: chat.append_turn origin is not a known origin")
	}
	if origin != OriginLocal && !untrusted {
		return "", cascade.New(cascade.KindInvalidInput, "conversation: chat.append_turn bridge origin must be marked untrusted")
	}
	if untrusted {
		return provider.ProvenanceUntrustedSource, nil
	}
	return provider.ProvenanceTrusted, nil
}

// newAppendedTurn builds the Turn chat.append_turn stores from its decoded
// params, carrying the marker the decoder already validated.
func newAppendedTurn(p appendTurnParams, role Role, seq, now int64) Turn {
	prov, _ := resolveMarker(p.Origin, p.Untrusted) // decodeAppendTurnParams refused an invalid pair
	return Turn{ID: NewTurnID(p.ThreadID, seq, role), ThreadID: p.ThreadID, Seq: seq, Role: role,
		CreatedAt: now, Origin: p.Origin, Provenance: prov}
}

// checkTurnMarker is the store-level twin of resolveMarker for a Turn built
// by a caller that is not the wire (journal resume, tests). The empty
// Provenance is the unmarked local default; a bridge origin must carry
// untrusted-source; anything that is not a provenance is refused.
func checkTurnMarker(t Turn) error {
	if !validOrigin(t.Origin) {
		return ErrInvalidRecord
	}
	if t.Provenance != "" && !t.Provenance.Valid() {
		return ErrInvalidRecord
	}
	if t.Origin != OriginLocal && t.Provenance != provider.ProvenanceUntrustedSource {
		return ErrInvalidRecord
	}
	return nil
}

// needsOriginRow reports whether t carries a marker worth a row: a bridge
// origin or an untrusted provenance. A local trusted turn writes none.
func needsOriginRow(t Turn) bool {
	return t.Origin != OriginLocal || t.Provenance == provider.ProvenanceUntrustedSource
}

// insertTurnOrigin writes t's marker row on tx, the transaction that wrote
// the turn, so the two commit or roll back together. No-op for an unmarked
// local turn.
func insertTurnOrigin(ctx context.Context, tx *sql.Tx, t Turn) error {
	if !needsOriginRow(t) {
		return nil
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO `+tableTurnOrigin+` (turn_id, origin, provenance) VALUES (?, ?, ?)`,
		t.ID, t.Origin, string(t.Provenance)) // checkTurnMarker already refused a bridge origin that is not untrusted-source
	return translateAppendError(err, "conversation: append turn origin")
}

// applyStoredMarker sets t's Origin and Provenance from the LEFT JOINed
// columns. A NULL pair is an unmarked local turn (trusted). A stored
// provenance that does not parse reads as untrusted-source, never as
// trusted (provider.ParseProvenance); a row naming any non-local origin reads
// as untrusted as well (a bridge row stored "trusted" can only be tampering),
// with the origin label kept as stored.
func applyStoredMarker(t *Turn, origin, prov sql.NullString) {
	if !origin.Valid && !prov.Valid {
		t.Origin, t.Provenance = OriginLocal, provider.ProvenanceTrusted
		return
	}
	t.Origin = origin.String
	p, err := provider.ParseProvenance(prov.String)
	if err != nil || t.Origin != OriginLocal {
		p = provider.ProvenanceUntrustedSource // a bridge (or unknown) origin never reads trusted, whatever the row says
	}
	t.Provenance = p
}

// isUntrusted is the wire "untrusted" flag: true when the turn's
// provenance is untrusted-source. The empty Provenance is the unmarked
// local default and is not untrusted.
func (t Turn) isUntrusted() bool {
	return t.Provenance != "" && t.Provenance != provider.ProvenanceTrusted
}

// restoreMarker rebuilds a journaled turn's marker for replay: the payload's
// provenance when it recorded one, else untrusted-source whenever the turn
// carries a bridge origin (an entry that lost its provenance is never
// replayed as trusted). An unmarked local entry stays unmarked.
func restoreMarker(t Turn, prov provider.Provenance) Turn {
	switch {
	case prov != "":
		t.Provenance = prov
	case t.Origin != OriginLocal:
		t.Provenance = provider.ProvenanceUntrustedSource
	}
	return t
}

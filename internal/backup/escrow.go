// Purpose: the S-42.T6 escrow-guard half of the recovery-key ceremony --
//
//	split out of recovery.go to stay under the repo's 300-line-per-file
//	gate. Records "the recovery key has been escrowed" in the audit
//	domain and answers "has it" for CreateSnapshot's (snapshot.go)
//	engine-level guard.
//
// Inputs: an audit.Writer (record) or an AuditReader (check).
// Outputs: a written audit record, or an escrow boolean.
// Constraints: the audit Kind enum is CLOSED at fourteen members (schema.go);
//
//	this file reuses KindElevationGrant rather than inventing a fifteenth.
//	The Action/Outcome pair it emits and scans for is never re-derived
//	from a second copy of itself.
//
// SPORT: internal.backup.escrow/ADDED (P1-E19-W4-S42-T6).

// Package backup doc: see doc.go for the canonical package comment.
package backup

import (
	"context"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// EscrowChecker reports whether the backup age identity has been recorded
// as escrowed via a successful `cascade backup key export`. CreateSnapshot
// (snapshot.go) consults it, when configured, before capturing a single
// domain -- so a manual `backup create` refuses identically to the
// scheduled-fire path (which already refuses on empty ElevationProof:
// 06 §5.14's "no elevation bypass" rule means the unattended path never
// even reaches this check).
type EscrowChecker interface {
	Escrowed(ctx context.Context) (bool, error)
}

// AuditReader is the read-only audit seam AuditEscrowChecker depends on --
// audit.Log.Query's exact signature -- so this package never needs the
// audit write path to answer "has the ceremony run".
type AuditReader interface {
	Query(ctx context.Context, f audit.Filter) (audit.Page, error)
}

// EscrowRecordAction and EscrowRecordOutcome are the exact Action/Outcome
// pair a successful key-export ceremony records and AuditEscrowChecker
// scans for -- never re-derived from a second copy of themselves, since
// nothing else in this codebase emits this pair.
const (
	EscrowRecordAction  = "backup.key_export"
	EscrowRecordOutcome = "backup_key_escrowed=true"
)

// RecordKeyEscrowed appends the audit record AuditEscrowChecker looks for.
// Called once, by a successful `cascade backup key export`.
func RecordKeyEscrowed(ctx context.Context, w audit.Writer, actor string) error {
	_, err := w.Append(ctx, audit.Event{
		Kind: audit.KindElevationGrant, Actor: actor, Action: EscrowRecordAction, Outcome: EscrowRecordOutcome,
	})
	return err
}

// AuditEscrowChecker is the production EscrowChecker: it reports true once
// RecordKeyEscrowed has appended at least one matching record. The audit
// log is append-only, so once true this never reverts to false.
type AuditEscrowChecker struct {
	Reader AuditReader
}

// Escrowed implements EscrowChecker.
func (c AuditEscrowChecker) Escrowed(ctx context.Context) (bool, error) {
	if c.Reader == nil {
		return false, cascade.New(cascade.KindUnavailable, "backup: no audit reader configured for the escrow check")
	}
	cursor := ""
	for {
		page, err := c.Reader.Query(ctx, audit.Filter{
			Kinds: []audit.Kind{audit.KindElevationGrant}, Cursor: cursor, Limit: 500,
		})
		if err != nil {
			return false, err
		}
		for _, rec := range page.Records {
			if rec.Action == EscrowRecordAction && rec.Outcome == EscrowRecordOutcome {
				return true, nil
			}
		}
		if page.NextCursor == "" {
			return false, nil
		}
		cursor = page.NextCursor
	}
}

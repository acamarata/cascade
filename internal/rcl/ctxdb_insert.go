package rcl

import (
	"database/sql"
	"errors"
	"strings"
)

// Insert inserts a context record of the given singular type ("segment",
// "blob" or "section") with external identifier id and body text.
//
// The row is added to the plural table and, unless an after-insert trigger
// already maintains the matching FTS index, to the FTS table as well,
// both inside a single transaction. A duplicate external_id within the
// same type yields ErrCtxDBDuplicate and leaves the database unchanged.
// Blank or whitespace-only id or text, an unknown type, or a nil receiver
// or underlying DB yields ErrCtxDBInvalid. Other SQL errors propagate
// without leaving a partial transaction.
func (db *CtxDB) Insert(typ, id, text string) error {
	if db == nil || db.DB == nil {
		return ErrCtxDBInvalid
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(text) == "" {
		return ErrCtxDBInvalid
	}

	table, ftsTable, ok := tableNames(typ)
	if !ok {
		return ErrCtxDBInvalid
	}

	// Check for duplicate external_id within this type.
	var n int
	if err := db.DB.QueryRow(
		"SELECT COUNT(*) FROM "+table+" WHERE external_id = ?", id,
	).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrCtxDBDuplicate
	}

	// Does an after-insert trigger already maintain the FTS shadow?
	var trig int
	if err := db.DB.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND (name = ? OR name = ?)",
		"t_ai_"+table, "t_ai_"+ftsTable,
	).Scan(&trig); err != nil {
		return err
	}

	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		"INSERT INTO "+table+" (external_id, text) VALUES (?, ?)",
		id, text,
	)
	if err != nil {
		return err
	}

	if trig == 0 {
		rowID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			"INSERT INTO "+ftsTable+" (rowid, text) VALUES (?, ?)",
			rowID, text,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// tableNames maps a singular context type to its plural table and the
// matching FTS5 table. It returns ok=false for unknown types.
func tableNames(typ string) (table, ftsTable string, ok bool) {
	switch typ {
	case "segment":
		return "segments", "segments_fts", true
	case "blob":
		return "blobs", "blobs_fts", true
	case "section":
		return "sections", "sections_fts", true
	default:
		return "", "", false
	}
}

// ensure imports used even if the body above is later trimmed.
var (
	_            = errors.Is
	_ sql.Result = nil
)

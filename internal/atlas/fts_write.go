package atlas

import (
	"database/sql"
	"fmt"
)

// WriteBody atomically inserts or replaces the body of a note in the FTS index.
func WriteBody(db *sql.DB, id int64, body string, homes map[int64]string) error {
	if db == nil || id <= 0 {
		return fmt.Errorf("%w: db or id", ErrBodyInput)
	}

	for k, v := range homes {
		if k <= 0 {
			return fmt.Errorf("%w: home key", ErrBodyInput)
		}
		if v != "sqlite" && v != "git" && v != "archive" {
			return fmt.Errorf("%w: home value", ErrBodyInput)
		}
	}

	var exists int
	err := db.QueryRow("SELECT COUNT(1) FROM notes WHERE id = ?", id).Scan(&exists)
	if err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("%w: note", ErrBodyNotFound)
	}

	home, hasHome := homes[id]
	if !hasHome {
		return ErrFTSBodyPolicyRequired
	}

	if home == "git" || home == "archive" {
		return ErrFTSBodyProhibited
	}

	// home == "sqlite"
	_, err = db.Exec("REPLACE INTO notes_fts(rowid, body) VALUES (?, ?)", id, body)
	if err != nil {
		return err
	}

	return nil
}

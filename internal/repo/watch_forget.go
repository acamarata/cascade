package repo

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ForgetRepository clears a repository's paths and intents atomically while
// retaining its cursor, so the next maintenance batch advances monotonically.
func (j *WatchJournal) ForgetRepository(ctx context.Context, repoID string) error {
	if repoID == "" {
		return cascade.New(cascade.KindInvalidInput, "repo: forget requires a repository id")
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return jerr(err, "begin forget")
	}
	defer func() { _ = tx.Rollback() }()
	for _, table := range []string{tableWatchPath, tableWatchIntent} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE repository_id = ?`, repoID); err != nil {
			return jerr(err, "forget repository")
		}
	}
	return commitTx(tx)
}

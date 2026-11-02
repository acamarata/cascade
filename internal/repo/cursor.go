package repo

// Purpose: the repo_watch journal behind the maintenance pipeline: one
//   cursor row per repository, one row per indexed path (content hash, the
//   chunk ids written for it, whether its vectors are still pending) and one
//   intent row per path whose legs have not all applied yet.
// Inputs: a *sql.DB carrying WatchJournalMigrationSet's tables.
// Outputs: typed reads (Cursor, Paths) and the transactional writes the
//   Maintenance pipeline needs (intent begin/leg mark/complete).
// Constraints: the cursor only moves forward by Generation (KindConflict
//   otherwise); a path row and its intent row change in ONE transaction, so
//   a path hash never runs ahead of the effects it summarizes.
// SPORT: repo/maintenance-journal/ADD (contract:repo-maintenance, P1-GEN-04).

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	tableWatchCursor = "repo_watch_cursor"
	tableWatchPath   = "repo_watch_path"
	tableWatchIntent = "repo_watch_intent"

	watchSchemaVersion = 1
)

// WatchJournalMigrationSet is the three-table journal schema.
func WatchJournalMigrationSet() migrate.MigrationSet {
	text := func(n string, pk bool) migrate.ColumnDef {
		return migrate.ColumnDef{Name: n, Type: migrate.TypeText, PrimaryKey: pk, NotNull: true}
	}
	num := func(n string) migrate.ColumnDef {
		return migrate.ColumnDef{Name: n, Type: migrate.TypeInteger, NotNull: true}
	}
	step := func(desc, name string, cols ...migrate.ColumnDef) migrate.MigrationStep {
		return migrate.MigrationStep{Kind: migrate.StepCreateTable, Description: desc,
			Table: &migrate.TableDef{Name: name, Columns: cols}}
	}
	return migrate.MigrationSet{
		SetID:         "repo-watch",
		SchemaVersion: watchSchemaVersion,
		ReaderCeiling: watchSchemaVersion,
		Steps: []migrate.MigrationStep{
			step("repo_watch_cursor: how far the watcher's changes are applied", tableWatchCursor,
				text("repository_id", true), num("fs_seq"), text("commit_oid", false), text("tree_oid", false), num("generation")),
			step("repo_watch_path: per-path content hash and the chunk ids written for it", tableWatchPath,
				text("repository_id", true), text("rel", true), text("content_sha256", false),
				text("chunk_ids_json", false), num("vector_pending")),
			step("repo_watch_intent: a path whose legs have not all applied", tableWatchIntent,
				text("repository_id", true), text("rel", true), text("content_sha256", false),
				num("generation"), num("deleted"), num("legs_done")),
		},
	}
}

// ApplyWatchJournalSchema idempotently applies WatchJournalMigrationSet.
func ApplyWatchJournalSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "repo: ApplyWatchJournalSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "repo: ApplyWatchJournalSchema requires a non-nil Clock")
	}
	return migrate.Apply(ctx, migrate.ApplyConfig{
		DB: db, Dialect: dialect, Clock: clock, DBPath: dbPath, BackupDir: backupDir,
	}, WatchJournalMigrationSet())
}

// Cursor is how far a repository's changes are fully applied.
type Cursor struct {
	RepositoryID string
	FSSeq        uint64
	Commit       string
	TreeOID      string
	Generation   int
}

// PathState is one indexed path's journaled state.
type PathState struct {
	Rel           string
	ContentSHA256 string
	ChunkIDs      []string
	VectorPending bool
}

// intent is a path whose legs have not all applied. LegsDone is a bitmask.
type intent struct {
	Rel           string
	ContentSHA256 string
	Generation    int
	Deleted       bool
	LegsDone      int
}

// WatchJournal reads and writes the repo_watch tables.
type WatchJournal struct{ db *sql.DB }

// NewWatchJournal wraps db, which must carry WatchJournalMigrationSet.
func NewWatchJournal(db *sql.DB) (*WatchJournal, error) {
	if db == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "repo: NewWatchJournal requires a non-nil db")
	}
	return &WatchJournal{db: db}, nil
}

func jerr(err error, what string) error {
	return cascade.Wrap(cascade.KindUnavailable, err, "repo: watch journal "+what)
}

// Cursor reads repoID's cursor; ok=false when none was ever written.
func (j *WatchJournal) Cursor(ctx context.Context, repoID string) (Cursor, bool, error) {
	var c Cursor
	var seq int64
	err := j.db.QueryRowContext(ctx,
		`SELECT fs_seq, commit_oid, tree_oid, generation FROM `+tableWatchCursor+` WHERE repository_id = ?`,
		repoID).Scan(&seq, &c.Commit, &c.TreeOID, &c.Generation)
	if err == sql.ErrNoRows {
		return Cursor{}, false, nil
	}
	if err != nil {
		return Cursor{}, false, jerr(err, "read cursor")
	}
	c.RepositoryID, c.FSSeq = repoID, uint64(seq)
	return c, true, nil
}

// AdvanceCursor stores c; a lower Generation than the stored one is a
// KindConflict, so a stale writer cannot move the cursor back.
func (j *WatchJournal) AdvanceCursor(ctx context.Context, c Cursor) error {
	if c.RepositoryID == "" {
		return cascade.New(cascade.KindInvalidInput, "repo: cursor requires a repository id")
	}
	res, err := j.db.ExecContext(ctx,
		`INSERT INTO `+tableWatchCursor+` (repository_id, fs_seq, commit_oid, tree_oid, generation)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(repository_id) DO UPDATE SET fs_seq=excluded.fs_seq, commit_oid=excluded.commit_oid,
		   tree_oid=excluded.tree_oid, generation=excluded.generation
		 WHERE excluded.generation >= `+tableWatchCursor+`.generation`,
		c.RepositoryID, int64(c.FSSeq), c.Commit, c.TreeOID, c.Generation)
	if err != nil {
		return jerr(err, "advance cursor")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return jerr(err, "advance cursor rows")
	}
	if n == 0 {
		return cascade.Newf(cascade.KindConflict, "repo: cursor generation %d is below the stored generation", c.Generation)
	}
	return nil
}

// Paths returns every journaled path of repoID, keyed by repo-relative path.
func (j *WatchJournal) Paths(ctx context.Context, repoID string) (map[string]PathState, error) {
	rows, err := j.db.QueryContext(ctx,
		`SELECT rel, content_sha256, chunk_ids_json, vector_pending FROM `+tableWatchPath+` WHERE repository_id = ?`, repoID)
	if err != nil {
		return nil, jerr(err, "list paths")
	}
	defer func() { _ = rows.Close() }()
	out := map[string]PathState{}
	for rows.Next() {
		var p PathState
		var raw string
		var pending int
		if err := rows.Scan(&p.Rel, &p.ContentSHA256, &raw, &pending); err != nil {
			return nil, jerr(err, "scan path")
		}
		if err := json.Unmarshal([]byte(raw), &p.ChunkIDs); err != nil {
			return nil, cascade.Wrap(cascade.KindIntegrity, err, "repo: decode journaled chunk ids")
		}
		p.VectorPending = pending != 0
		out[p.Rel] = p
	}
	if err := rows.Err(); err != nil {
		return nil, jerr(err, "iterate paths")
	}
	return out, nil
}

// intents returns the unfinished intents of repoID, sorted by path.
func (j *WatchJournal) intents(ctx context.Context, repoID string) ([]intent, error) {
	rows, err := j.db.QueryContext(ctx,
		`SELECT rel, content_sha256, generation, deleted, legs_done FROM `+tableWatchIntent+
			` WHERE repository_id = ? ORDER BY rel`, repoID)
	if err != nil {
		return nil, jerr(err, "list intents")
	}
	defer func() { _ = rows.Close() }()
	var out []intent
	for rows.Next() {
		var in intent
		var del int
		if err := rows.Scan(&in.Rel, &in.ContentSHA256, &in.Generation, &del, &in.LegsDone); err != nil {
			return nil, jerr(err, "scan intent")
		}
		in.Deleted = del != 0
		out = append(out, in)
	}
	if err := rows.Err(); err != nil {
		return nil, jerr(err, "iterate intents")
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Rel < out[b].Rel })
	return out, nil
}

// beginIntent records (or restarts) the intent for one path.
func (j *WatchJournal) beginIntent(ctx context.Context, repoID string, in intent) error {
	del := 0
	if in.Deleted {
		del = 1
	}
	_, err := j.db.ExecContext(ctx,
		`INSERT INTO `+tableWatchIntent+` (repository_id, rel, content_sha256, generation, deleted, legs_done)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(repository_id, rel) DO UPDATE SET
		   legs_done = CASE WHEN content_sha256 = excluded.content_sha256 THEN legs_done ELSE 0 END,
		   content_sha256=excluded.content_sha256, generation=excluded.generation, deleted=excluded.deleted`,
		repoID, in.Rel, in.ContentSHA256, in.Generation, del, in.LegsDone)
	if err != nil {
		return jerr(err, "begin intent")
	}
	return nil
}

// markLegs ORs bits into the intent's legs_done.
func (j *WatchJournal) markLegs(ctx context.Context, repoID, rel string, bits int) error {
	_, err := j.db.ExecContext(ctx,
		`UPDATE `+tableWatchIntent+` SET legs_done = (legs_done | ?) WHERE repository_id = ? AND rel = ?`,
		bits, repoID, rel)
	if err != nil {
		return jerr(err, "mark leg")
	}
	return nil
}

// complete finishes a path in ONE transaction: the intent row goes away and
// the path row is written (st != nil) or removed (st == nil).
func (j *WatchJournal) complete(ctx context.Context, repoID, rel string, st *PathState) error {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return jerr(err, "begin tx")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+tableWatchIntent+` WHERE repository_id = ? AND rel = ?`, repoID, rel); err != nil {
		return jerr(err, "clear intent")
	}
	if st == nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+tableWatchPath+` WHERE repository_id = ? AND rel = ?`, repoID, rel); err != nil {
			return jerr(err, "clear path")
		}
		return commitTx(tx)
	}
	ids, merr := json.Marshal(append([]string{}, st.ChunkIDs...))
	if merr != nil {
		return cascade.Wrap(cascade.KindInvalidInput, merr, "repo: encode chunk ids")
	}
	pending := 0
	if st.VectorPending {
		pending = 1
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO `+tableWatchPath+` (repository_id, rel, content_sha256, chunk_ids_json, vector_pending)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(repository_id, rel) DO UPDATE SET content_sha256=excluded.content_sha256,
		   chunk_ids_json=excluded.chunk_ids_json, vector_pending=excluded.vector_pending`,
		repoID, rel, st.ContentSHA256, string(ids), pending)
	if err != nil {
		return jerr(err, "write path")
	}
	return commitTx(tx)
}

func commitTx(tx *sql.Tx) error {
	if err := tx.Commit(); err != nil {
		return jerr(err, "commit")
	}
	return nil
}

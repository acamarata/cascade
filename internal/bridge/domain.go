// Package bridge is the durable store behind Epic W's personal-assistant
// bridges (P1-E23-W5-S48-T1): one row per bridge subject carrying its
// pairing binding, the outstanding pairing code's digest, the wrong-attempt
// counter, the long-poll offset and a bounded replay window.
//
// Purpose (this file): the bridge_subject table, authored through the
//
//	B/S-02.T3 portable migration builder (internal/storage/migrate),
//	following internal/ci/domain.go's and internal/repo/store.go's exact
//	precedent, plus the idempotent upsert a bridge's read-modify-write cycle
//	needs.
//
// DOMAIN REGISTRATION. This package claims NO new DomainID. The
// cascade.db domain set is CLOSED (R-14.5, amended R-16.51/R-16.75) and
// R-21.22 settles what a later ticket does instead: "a new table joins an
// existing domain rather than adding a twelfth DomainID" — the rule
// internal/repo/store.go already follows for context_repo_inventory. A
// bridge subject is a paired-device session record, so bridge_subject joins
// DomainSessions (internal/storage/domains.go's own entry names "nodes,
// lanes, journal" as that domain's tenants). This file reads no DomainID at
// all and adds no entry to AllDomains.
//
// SCHEMA VERSION: R-16.77 keys applied_migrations by (SetID,
// schema_version), so this set's own sequence starts at 1 under the SetID
// "bridge"; it claims no slot in any other set's numbering.
//
// WHY THE TWO LISTS ARE JSON. AllowedFrom and SeenUpdateIDs are small,
// bounded, read-and-written whole, and never queried BY element — the only
// question either answers is "does this set contain X", asked in Go after
// the row is loaded. A child table would add two joins and a delete cascade
// to buy a query nothing performs. The encode/decode is total: a malformed
// value is an error, never a silently empty list, because an empty
// allowlist reads as "this bot is unpaired" and would re-open a paired bot.
//
// SPORT: internal.bridge.MigrationSet/ADDED, internal.bridge.Store/ADDED
//
//	(P1-E23-W5-S48-T1).
package bridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// tableSubject is this package's one table.
const tableSubject = "bridge_subject"

// bridgeSchemaVersion is this MigrationSet's target version, in its own
// per-SetID sequence (R-16.77).
const bridgeSchemaVersion = 1

// SchemaVersion is bridgeSchemaVersion exported for a composition root's
// reader-ceiling max(), matching every sibling schema package's pattern.
const SchemaVersion = bridgeSchemaVersion

// SubjectRow is one bridge subject's persisted state. Times are stored as
// milliseconds since the epoch, matching internal/ci's own convention, and
// zero means "not set" rather than 1970.
type SubjectRow struct {
	Subject       string
	TrustTier     string
	PairedAt      time.Time
	AllowedFrom   []string
	CodeDigest    string
	CodeExpiresAt time.Time
	WrongAttempts int
	Offset        int64
	SeenUpdateIDs []int64
	// Version is the row's compare-and-swap token: the value Load read, and
	// the value Save requires to still be current before it writes. A zero
	// Version means "no row existed when I read", which Save turns into an
	// insert that refuses if one has appeared since. Callers never compute
	// it; they carry back what Load handed them.
	Version int64
}

// MigrationSet is the bridge subject schema.
func MigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         "bridge",
		SchemaVersion: bridgeSchemaVersion,
		ReaderCeiling: bridgeSchemaVersion,
		Steps:         []migrate.MigrationStep{subjectTableStep()},
	}
}

func subjectTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "bridge_subject: one row per bridge instance (pairing binding, code digest, poll offset)",
		Table: &migrate.TableDef{
			Name: tableSubject,
			Columns: []migrate.ColumnDef{
				{Name: "subject", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "trust_tier", Type: migrate.TypeText, NotNull: true},
				{Name: "paired_at", Type: migrate.TypeInteger, NotNull: true},
				{Name: "allowed_from", Type: migrate.TypeText, NotNull: true},
				{Name: "code_digest", Type: migrate.TypeText, NotNull: true},
				{Name: "code_expires_at", Type: migrate.TypeInteger, NotNull: true},
				{Name: "wrong_attempts", Type: migrate.TypeInteger, NotNull: true},
				{Name: "poll_offset", Type: migrate.TypeInteger, NotNull: true},
				{Name: "seen_update_ids", Type: migrate.TypeText, NotNull: true},
				// The compare-and-swap token (see cas.go). It is a column on
				// THIS step rather than an added one because the DSL has
				// exactly two step kinds, CreateTable and CreateIndex
				// (internal/storage/migrate/dsl.go StepKind) — there is no
				// add-column step to emit — and bridge_subject is introduced
				// by this same unreleased ticket, so no deployed ledger can
				// diverge from it. A future column on a SHIPPED table needs a
				// new step kind in the DSL, not a second edit here.
				{Name: "version", Type: migrate.TypeInteger, NotNull: true},
			},
		},
	}
}

// ApplyMigrationSchema idempotently applies MigrationSet against db.
func ApplyMigrationSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect,
	clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "bridge: ApplyMigrationSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "bridge: ApplyMigrationSchema requires a non-nil Clock")
	}
	return migrate.Apply(ctx, migrate.ApplyConfig{
		DB: db, Dialect: dialect, Clock: clock, DBPath: dbPath, BackupDir: backupDir,
	}, MigrationSet())
}

// Store is the bridge_subject CRUD layer.
type Store struct {
	db *sql.DB
}

// NewStore builds a store over db.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Load reads one subject's row. A subject with no row is (zero, false, nil):
// "nothing stored yet" is a valid state, not a failure.
func (s *Store) Load(ctx context.Context, subject string) (SubjectRow, bool, error) {
	if s == nil || s.db == nil {
		return SubjectRow{}, false, cascade.New(cascade.KindUnavailable, "bridge: no database configured")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT subject, trust_tier, paired_at, allowed_from, code_digest,
		       code_expires_at, wrong_attempts, poll_offset, seen_update_ids, version
		FROM `+tableSubject+` WHERE subject = ?`, subject)
	var (
		out                   SubjectRow
		pairedAt, codeExpires int64
		allowedRaw, seenRaw   string
	)
	err := row.Scan(&out.Subject, &out.TrustTier, &pairedAt, &allowedRaw,
		&out.CodeDigest, &codeExpires, &out.WrongAttempts, &out.Offset, &seenRaw, &out.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return SubjectRow{}, false, nil
	}
	if err != nil {
		return SubjectRow{}, false, cascade.Wrapf(cascade.KindUnavailable, err, "bridge: read subject %q", subject)
	}
	out.PairedAt, out.CodeExpiresAt = fromMillis(pairedAt), fromMillis(codeExpires)
	if err := decodeLists(&out, allowedRaw, seenRaw); err != nil {
		return SubjectRow{}, false, err
	}
	return out, true, nil
}

// decodeLists parses the two JSON list columns. A malformed value is an
// error: an empty allowlist would read as "unpaired" and re-open a bound bot.
func decodeLists(out *SubjectRow, allowedRaw, seenRaw string) error {
	if err := json.Unmarshal([]byte(allowedRaw), &out.AllowedFrom); err != nil {
		return cascade.Wrapf(cascade.KindIntegrity, err,
			"bridge: subject %q has an unreadable allowlist", out.Subject)
	}
	if err := json.Unmarshal([]byte(seenRaw), &out.SeenUpdateIDs); err != nil {
		return cascade.Wrapf(cascade.KindIntegrity, err,
			"bridge: subject %q has an unreadable replay window", out.Subject)
	}
	return nil
}

// encodeList renders a list column. A nil slice encodes as "[]", never as
// the JSON null a decoder would hand back as a nil slice with no complaint.
func encodeList[T any](list []T) (string, error) {
	if list == nil {
		return "[]", nil
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "bridge: encode subject list column")
	}
	return string(raw), nil
}

// millis renders t as milliseconds since the epoch, 0 for the zero time.
func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// fromMillis is millis' inverse: 0 reads back as the zero time, not 1970.
func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

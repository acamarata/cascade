package learn

// Purpose: ConfigStore, the learned-config store. Submit (and SubmitWith,
//   its transactional form) is THE single ingress for a learned proposal;
//   UpdateStatus is the one status writer (compare-and-set).
// Inputs: Submissions from P1-LRN-02's proposal engine; status moves from
//   P1-LRN-03's router.
// Outputs: rows in jobs_learned_config and jobs_learned_config_submission.
// Constraints: C11. The only path from internal/learn to configuration is
//   ConfigStore.Submit -> Router.Route (P1-LRN-03) -> StoreApplier; this
//   store classifies and stores, it never writes config. A Submission has
//   no tier field: the tier is computed by ClassifyChange and re-verified on
//   every read, so a stored row that claims another tier is refused. A
//   security-tier row is never marked applied. Every free string passes
//   ValidateNoIdentifiers before any SQL runs. Real SQL errors are
//   KindUnavailable and never read as absence.
// SPORT: internal.learn.ConfigStore/ADDED (P1-LRN-01).

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ConfigStore is the learned-config store over a migrated jobs-domain db.
type ConfigStore struct {
	db    *sql.DB
	clock runtime.Clock
}

// NewConfigStore returns a store over db. Both arguments are required. The
// schema (ConfigMigrationSet) is applied by the composition root.
func NewConfigStore(db *sql.DB, clock runtime.Clock) (*ConfigStore, error) {
	if db == nil || clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "learn: NewConfigStore needs a db and a clock")
	}
	return &ConfigStore{db: db, clock: clock}, nil
}

// Submission is a learned proposal as its producer states it. It carries
// no tier, area or bound flag: those are computed, never stated.
type Submission struct {
	Source       SourceRef
	Target       string
	Scope, Label string
	Confidence   float64
	Change       Change
	Evidence     []EvidenceRef
}

// querier is the part of *sql.DB and *sql.Tx the store reads through.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Submit stores sub as a pending learned config (Version 0, nothing
// applied) and returns it. It is the single ingress.
func (s *ConfigStore) Submit(ctx context.Context, sub Submission) (LearnedConfig, error) {
	return s.SubmitWith(ctx, sub, nil)
}

// SubmitWith is Submit with extra run inside the same transaction; an error
// from extra rolls both rows back and is returned unchanged.
func (s *ConfigStore) SubmitWith(ctx context.Context, sub Submission, extra func(tx *sql.Tx, c LearnedConfig) error) (LearnedConfig, error) {
	c, err := s.buildConfig(sub)
	if err != nil {
		return LearnedConfig{}, err
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if err := insertConfig(ctx, tx, c); err != nil {
			return err
		}
		if extra != nil {
			return extra(tx, c)
		}
		return nil
	})
	if err != nil {
		return LearnedConfig{}, err
	}
	return c, nil
}

// buildConfig validates sub, classifies its change and builds the row.
func (s *ConfigStore) buildConfig(sub Submission) (LearnedConfig, error) {
	if err := validateSubmission(sub); err != nil {
		return LearnedConfig{}, err
	}
	cls, err := ClassifyChange(sub.Target, sub.Change)
	if err != nil {
		return LearnedConfig{}, err
	}
	id, err := cascade.NewID()
	if err != nil {
		return LearnedConfig{}, err
	}
	now := s.clock.Now().UTC()
	return NewLearnedConfig(LearnedConfig{
		ID: id.String(), Source: sub.Source, Target: cls.Target, Scope: sub.Scope, Label: sub.Label,
		Confidence: sub.Confidence, Tier: cls.Tier, Areas: cls.Areas, LoosensBound: cls.LoosensBound,
		Change: sub.Change, Evidence: append([]EvidenceRef(nil), sub.Evidence...), Status: StatusPending,
		Created: now, LastVerified: now,
	})
}

// validateSubmission checks the closed source kind, required ids, at least
// one evidence ref, a confidence in [0,1], and every free string's privacy.
func validateSubmission(sub Submission) error {
	if _, err := parseSourceKind(sub.Source.Kind); err != nil {
		return err
	}
	if strings.TrimSpace(sub.Source.ID) == "" {
		return cascade.New(cascade.KindInvalidInput, "learn: submission needs a source id")
	}
	if len(sub.Evidence) == 0 {
		return cascade.New(cascade.KindInvalidInput, "learn: submission needs at least one evidence ref")
	}
	if math.IsNaN(sub.Confidence) || sub.Confidence < 0 || sub.Confidence > 1 {
		return cascade.New(cascade.KindInvalidInput, "learn: submission confidence must lie in [0, 1]")
	}
	fields := [][2]string{
		{"Label", sub.Label}, {"Scope", sub.Scope}, {"Source.ID", sub.Source.ID}, {"Change.Value", sub.Change.Value},
	}
	for i, ev := range sub.Evidence {
		if strings.TrimSpace(ev.ID) == "" || strings.TrimSpace(ev.Kind) == "" {
			return cascade.Newf(cascade.KindInvalidInput, "learn: evidence ref %d needs a kind and an id", i)
		}
		p := fmt.Sprintf("Evidence[%d].", i)
		fields = append(fields, [2]string{p + "Kind", ev.Kind}, [2]string{p + "ID", ev.ID},
			[2]string{p + "RepoID", ev.RepoID}, [2]string{p + "LaneID", ev.LaneID})
	}
	for _, f := range fields {
		if err := ValidateNoIdentifiers(f[0], f[1]); err != nil {
			return err
		}
	}
	return nil
}

// inTx runs fn in one transaction: fn's error rolls back and is returned;
// a begin or commit failure is KindUnavailable.
func (s *ConfigStore) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "learn: begin learned-config transaction")
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "learn: commit learned-config transaction")
	}
	return nil
}

// allowedMoves is the closed proposal lifecycle; rejected is terminal.
var allowedMoves = map[Status]map[Status]bool{
	StatusPending:  {StatusApplied: true, StatusRejected: true},
	StatusApplied:  {StatusReverted: true},
	StatusReverted: {StatusApplied: true},
}

// UpdateStatus moves id from one status to another, compare-and-set: it is
// the one status writer. A row not at from is KindConflict naming its
// current status; an unknown id is KindNotFound; a status outside the
// closed set is KindInvalidInput. A disallowed move is KindConflict naming
// both statuses. A security-tier row is never marked applied
// (KindPermissionDenied, C11), checked before the stored-status comparison.
func (s *ConfigStore) UpdateStatus(ctx context.Context, id string, from, to Status) error {
	if _, err := parseStatus(from); err != nil {
		return err
	}
	if _, err := parseStatus(to); err != nil {
		return err
	}
	if !allowedMoves[from][to] {
		return cascade.Newf(cascade.KindConflict, "learn: status move from %q to %q is not allowed", from, to)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		c, err := getConfig(ctx, tx, id)
		if err != nil {
			return err
		}
		if to == StatusApplied && c.Tier == TierSecurity {
			return cascade.New(cascade.KindPermissionDenied, "learn: a security-tier learned config is never applied (C11)")
		}
		if c.Status != from {
			return cascade.Newf(cascade.KindConflict, "learn: learned config status is %q, not %q", c.Status, from)
		}
		res, err := tx.ExecContext(ctx, `UPDATE jobs_learned_config_submission SET status = ?, status_changed_at = ?
			WHERE config_id = ? AND status = ?`, string(to), s.clock.Now().UTC().UnixNano(), id, string(from))
		return expectOneRow(res, err, "update learned-config status")
	})
}

// RecordOutcome counts one success or failure of id and stamps
// LastVerified. An unknown id is KindNotFound.
func (s *ConfigStore) RecordOutcome(ctx context.Context, id string, success bool) error {
	q := `UPDATE jobs_learned_config SET failure_count = failure_count + 1, last_verified_at = ? WHERE id = ?`
	if success {
		q = `UPDATE jobs_learned_config SET success_count = success_count + 1, last_verified_at = ? WHERE id = ?`
	}
	res, err := s.db.ExecContext(ctx, q, s.clock.Now().UTC().UnixNano(), id)
	return expectOneRow(res, err, "record learned-config outcome")
}

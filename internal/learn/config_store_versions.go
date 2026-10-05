package learn

// Purpose: the applied-value history of a learned config: AppendVersion,
//   ActiveVersion, VersionHistory and RevertToVersion (the one revert).
// Inputs: a config id and the value P1-LRN-03's applier wrote.
// Outputs: rows in jobs_learned_config_version; LearnedConfig.Rollback.
// Constraints: append-only. A revert stamps the active version and appends
//   a new one carrying the earlier value; nothing is deleted, so every
//   revert stays revertible. A security-tier config never gets a version
//   (C11), and every value stays inside its target's change envelope
//   (learned values only tighten). Each write is one transaction; a SQL
//   failure is KindUnavailable with no partial row.
// SPORT: internal.learn.ConfigStore.RevertToVersion/ADDED (P1-LRN-01).

import (
	"context"
	"database/sql"
	"math"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Version is one applied value of a learned config.
type Version struct {
	ConfigID     string
	Version      int
	Value        string
	Baseline     *float64
	AppliedAt    time.Time
	AppliedBy    string
	RevertedAt   *time.Time
	RevertReason string
}

// appliedByRevert is the AppliedBy of a version RevertToVersion appends.
const appliedByRevert = "revert"

// AppendVersion records value as the next applied version of id and makes
// it active. An unknown id is KindNotFound; a non-applied status is
// KindConflict. Values must satisfy the submitted operation's shape and
// bound, and an add-only value must equal the submitted value.
func (s *ConfigStore) AppendVersion(ctx context.Context, id, value string, baseline *float64, appliedBy string) (Version, error) {
	if err := validateVersionInput(value, baseline, appliedBy); err != nil {
		return Version{}, err
	}
	var out Version
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		c, err := getConfig(ctx, tx, id)
		if err != nil {
			return err
		}
		if c.Status != StatusApplied {
			return cascade.Newf(cascade.KindConflict, "learn: cannot append a version while status is %q; requires applied", c.Status)
		}
		if err := versionAllowed(c, value); err != nil {
			return err
		}
		out, err = s.appendVersion(ctx, tx, id, value, baseline, appliedBy)
		return err
	})
	return out, err
}

// validateVersionInput checks a version's value, baseline and author.
func validateVersionInput(value string, baseline *float64, appliedBy string) error {
	if baseline != nil && (math.IsNaN(*baseline) || math.IsInf(*baseline, 0)) {
		return cascade.New(cascade.KindInvalidInput, "learn: version baseline must be finite")
	}
	if strings.TrimSpace(appliedBy) == "" {
		return cascade.New(cascade.KindInvalidInput, "learn: version needs an applied-by id")
	}
	if err := ValidateNoIdentifiers("AppliedBy", appliedBy); err != nil {
		return err
	}
	return ValidateNoIdentifiers("Value", value)
}

// versionAllowed refuses security and rejected configs and checks every
// value against the resolved target's envelope and submitted operation.
func versionAllowed(c LearnedConfig, value string) error {
	if c.Tier == TierSecurity {
		return cascade.New(cascade.KindPermissionDenied, "learn: a security-tier learned config is never applied (C11)")
	}
	if c.Status == StatusRejected {
		return cascade.New(cascade.KindPermissionDenied, "learn: a rejected learned config is never versioned")
	}
	id, err := ResolveTarget(string(c.Target))
	if err != nil {
		return err
	}
	t, ok := defaultRegistry.lookup(id)
	if !ok {
		return cascade.New(cascade.KindNotFound, "learn: learned config target has no registry row")
	}
	l, err := loosensEnvelope(t, Change{Op: c.Change.Op, Path: c.Change.Path, Value: value})
	if err != nil {
		return err
	}
	if l {
		return cascade.New(cascade.KindPermissionDenied, "learn: a value outside the target's bound is never stored (tighten-only)")
	}
	if t.Shape == ShapeAddOnly && value != c.Change.Value {
		return cascade.New(cascade.KindPermissionDenied, "learn: an add-only version must match the submitted change value")
	}
	return nil
}

// appendVersion inserts version max+1 and points the config at it.
func (s *ConfigStore) appendVersion(ctx context.Context, tx *sql.Tx, id, value string, baseline *float64, by string) (Version, error) {
	var next int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM jobs_learned_config_version
		WHERE config_id = ?`, id).Scan(&next); err != nil {
		return Version{}, cascade.Wrap(cascade.KindUnavailable, err, "learn: read next version")
	}
	now := s.clock.Now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs_learned_config_version (config_id, version, value,
		baseline_metric, applied_at, applied_by, reverted_at, revert_reason) VALUES (?, ?, ?, ?, ?, ?, NULL, '')`,
		id, next, value, baseline, now.UnixNano(), by); err != nil {
		return Version{}, cascade.Wrap(cascade.KindUnavailable, err, "learn: insert version")
	}
	res, err := tx.ExecContext(ctx, `UPDATE jobs_learned_config SET version = ? WHERE id = ?`, next, id)
	if err := expectOneRow(res, err, "point learned config at its version"); err != nil {
		return Version{}, err
	}
	return Version{ConfigID: id, Version: next, Value: value, Baseline: baseline, AppliedAt: now, AppliedBy: by}, nil
}

// ActiveVersion returns the newest version of id not reverted, or false
// when none is. An unknown id is KindNotFound.
func (s *ConfigStore) ActiveVersion(ctx context.Context, id string) (Version, bool, error) {
	c, err := getConfig(ctx, s.db, id)
	if err != nil {
		return Version{}, false, err
	}
	history, err := readVersions(ctx, s.db, id)
	if err != nil {
		return Version{}, false, err
	}
	i := activeIndex(history)
	if i < 0 {
		return Version{}, false, nil
	}
	if err := versionAllowed(c, history[i].Value); err != nil {
		return Version{}, false, err
	}
	return history[i], true, nil
}

// VersionHistory returns every version of id, oldest first. An unknown id
// is KindNotFound.
func (s *ConfigStore) VersionHistory(ctx context.Context, id string) ([]Version, error) {
	if _, err := getConfig(ctx, s.db, id); err != nil {
		return nil, err
	}
	return readVersions(ctx, s.db, id)
}

// readVersions reads id's versions through q, oldest first.
func readVersions(ctx context.Context, q querier, id string) ([]Version, error) {
	rows, err := q.QueryContext(ctx, `SELECT version, value, baseline_metric, applied_at, applied_by, reverted_at,
		revert_reason FROM jobs_learned_config_version WHERE config_id = ? ORDER BY version`, id)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: query versions")
	}
	defer func() { _ = rows.Close() }()
	var out []Version
	for rows.Next() {
		v := Version{ConfigID: id}
		var applied int64
		var reverted sql.NullInt64
		var baseline sql.NullFloat64
		if err := rows.Scan(&v.Version, &v.Value, &baseline, &applied, &v.AppliedBy, &reverted, &v.RevertReason); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: scan version")
		}
		v.AppliedAt = time.Unix(0, applied).UTC()
		if baseline.Valid {
			v.Baseline = &baseline.Float64
		}
		if reverted.Valid {
			at := time.Unix(0, reverted.Int64).UTC()
			v.RevertedAt = &at
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: read versions")
	}
	return out, nil
}

// activeIndex is the index of the newest unreverted version, or -1.
func activeIndex(history []Version) int {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].RevertedAt == nil {
			return i
		}
	}
	return -1
}

// withRollback fills c.Rollback from the version the active one replaced.
func withRollback(ctx context.Context, q querier, c LearnedConfig) (LearnedConfig, error) {
	history, err := readVersions(ctx, q, c.ID)
	if err != nil {
		return LearnedConfig{}, err
	}
	if i := activeIndex(history); i > 0 {
		c.Rollback = &RollbackSnapshot{PreviousValue: history[i-1].Value, SnapshotAt: history[i].AppliedAt}
	}
	return c, nil
}

// RevertToVersion makes version's value active again: it stamps the active
// version reverted with reason and appends a new version carrying the
// earlier value. An unknown config or version is KindNotFound; reverting to
// the active version, or with none active, is KindConflict.
func (s *ConfigStore) RevertToVersion(ctx context.Context, id string, version int, reason string) (Version, error) {
	if strings.TrimSpace(reason) == "" {
		return Version{}, cascade.New(cascade.KindInvalidInput, "learn: a revert needs a reason")
	}
	if err := ValidateNoIdentifiers("RevertReason", reason); err != nil {
		return Version{}, err
	}
	var out Version
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		target, active, err := revertPlan(ctx, tx, id, version)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE jobs_learned_config_version SET reverted_at = ?, revert_reason = ?
			WHERE config_id = ? AND version = ? AND reverted_at IS NULL`,
			s.clock.Now().UTC().UnixNano(), reason, id, active.Version)
		if err := expectOneRow(res, err, "stamp reverted version"); err != nil {
			return err
		}
		out, err = s.appendVersion(ctx, tx, id, target.Value, target.Baseline, appliedByRevert)
		return err
	})
	return out, err
}

// revertPlan finds the version to restore and the active one it replaces.
func revertPlan(ctx context.Context, tx *sql.Tx, id string, version int) (Version, Version, error) {
	c, err := getConfig(ctx, tx, id)
	if err != nil {
		return Version{}, Version{}, err
	}
	if c.Tier == TierSecurity {
		return Version{}, Version{}, cascade.New(cascade.KindPermissionDenied,
			"learn: a security-tier learned config is never applied (C11)")
	}
	history, err := readVersions(ctx, tx, id)
	if err != nil {
		return Version{}, Version{}, err
	}
	if version < 1 || version > len(history) {
		return Version{}, Version{}, cascade.New(cascade.KindNotFound, "learn: unknown learned-config version")
	}
	if err := versionAllowed(c, history[version-1].Value); err != nil {
		return Version{}, Version{}, err
	}
	a := activeIndex(history)
	if a < 0 || history[a].Version == version {
		return Version{}, Version{}, cascade.New(cascade.KindConflict, "learn: revert target is already active or nothing is active")
	}
	return history[version-1], history[a], nil
}

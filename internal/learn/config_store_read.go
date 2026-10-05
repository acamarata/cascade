package learn

// Purpose: ConfigStore's read side (Get, List), the row codec and the exec
//   helpers every store method shares (insertConfig, expectOneRow). Split
//   from config_store.go under the 300-line cap.
// Inputs: a config id or a ConfigFilter.
// Outputs: LearnedConfig values, newest first for List.
// Constraints: every row read is re-validated by NewLearnedConfig, so a
//   stored tier, area list or bound flag that disagrees with ClassifyChange
//   is refused rather than returned; an unknown stored enum is refused, never
//   defaulted. A SQL failure is KindUnavailable, never read as absence.
// SPORT: internal.learn.ConfigStore.Get/ADDED, ConfigStore.List/ADDED
//   (P1-LRN-01).

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ConfigFilter narrows List. A zero field matches any value.
type ConfigFilter struct {
	Tier   ConfigTier
	Status Status
	Scope  string
}

// selectConfig is the joined row every read decodes.
const selectConfig = `SELECT c.id, c.target_id, c.scope_ref, c.label, c.confidence, c.tier, c.areas,
	c.loosens_bound, c.source_kind, c.source_ref, c.created_at, c.last_verified_at, c.success_count,
	c.failure_count, c.version, s.change_json, s.evidence_json, s.status
	FROM jobs_learned_config c JOIN jobs_learned_config_submission s ON s.config_id = c.id`

// Get returns id with its rollback snapshot. An unknown id is KindNotFound.
func (s *ConfigStore) Get(ctx context.Context, id string) (LearnedConfig, error) {
	c, err := getConfig(ctx, s.db, id)
	if err != nil {
		return LearnedConfig{}, err
	}
	return withRollback(ctx, s.db, c)
}

// List returns the configs matching f, newest first.
func (s *ConfigStore) List(ctx context.Context, f ConfigFilter) ([]LearnedConfig, error) {
	var where []string
	var args []any
	if f.Tier != "" {
		if _, err := ParseConfigTier(string(f.Tier)); err != nil {
			return nil, err
		}
		where, args = append(where, "c.tier = ?"), append(args, string(f.Tier))
	}
	if f.Status != "" {
		if _, err := parseStatus(f.Status); err != nil {
			return nil, err
		}
		where, args = append(where, "s.status = ?"), append(args, string(f.Status))
	}
	if f.Scope != "" {
		where, args = append(where, "c.scope_ref = ?"), append(args, f.Scope)
	}
	q := selectConfig
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	configs, err := queryConfigs(ctx, s.db, q+" ORDER BY c.created_at DESC, c.id DESC", args...)
	if err != nil {
		return nil, err
	}
	for i := range configs {
		if configs[i], err = withRollback(ctx, s.db, configs[i]); err != nil {
			return nil, err
		}
	}
	return configs, nil
}

// getConfig reads one config through q (a db or a transaction).
func getConfig(ctx context.Context, q querier, id string) (LearnedConfig, error) {
	configs, err := queryConfigs(ctx, q, selectConfig+" WHERE c.id = ?", id)
	if err != nil {
		return LearnedConfig{}, err
	}
	if len(configs) == 0 {
		return LearnedConfig{}, cascade.New(cascade.KindNotFound, "learn: unknown learned config")
	}
	return configs[0], nil
}

// queryConfigs runs a selectConfig query and decodes every row.
func queryConfigs(ctx context.Context, q querier, query string, args ...any) ([]LearnedConfig, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: query learned configs")
	}
	defer func() { _ = rows.Close() }()
	var out []LearnedConfig
	for rows.Next() {
		c, err := scanConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: read learned configs")
	}
	return out, nil
}

// scanConfig decodes one joined row and re-validates it.
func scanConfig(rows *sql.Rows) (LearnedConfig, error) {
	var c LearnedConfig
	var target, tier, areas, kind, changeJSON, evidenceJSON, status string
	var loosens int
	var created, verified int64
	if err := rows.Scan(&c.ID, &target, &c.Scope, &c.Label, &c.Confidence, &tier, &areas, &loosens, &kind,
		&c.Source.ID, &created, &verified, &c.SuccessCount, &c.FailureCount, &c.Version,
		&changeJSON, &evidenceJSON, &status); err != nil {
		return LearnedConfig{}, cascade.Wrap(cascade.KindUnavailable, err, "learn: scan learned config")
	}
	var err error
	if c.Tier, err = ParseConfigTier(tier); err != nil {
		return LearnedConfig{}, err
	}
	if c.Areas, err = splitAreas(areas); err != nil {
		return LearnedConfig{}, err
	}
	if c.Source.Kind, err = parseSourceKind(SourceKind(kind)); err != nil {
		return LearnedConfig{}, err
	}
	if c.Status, err = parseStatus(Status(status)); err != nil {
		return LearnedConfig{}, err
	}
	if loosens != 0 && loosens != 1 {
		return LearnedConfig{}, cascade.New(cascade.KindInvalidInput, "learn: stored bound flag is not 0 or 1")
	}
	if err := decodeStrict(changeJSON, &c.Change); err != nil {
		return LearnedConfig{}, err
	}
	if err := decodeStrict(evidenceJSON, &c.Evidence); err != nil {
		return LearnedConfig{}, err
	}
	c.Target, c.LoosensBound = TargetID(target), loosens == 1
	c.Created, c.LastVerified = time.Unix(0, created).UTC(), time.Unix(0, verified).UTC()
	return NewLearnedConfig(c)
}

// decodeStrict decodes stored JSON, refusing unknown fields and trailing data.
func decodeStrict(raw string, v any) error {
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "learn: stored learned-config json is unreadable")
	}
	if dec.More() {
		return cascade.New(cascade.KindInvalidInput, "learn: stored learned-config json has trailing data")
	}
	return nil
}

// joinAreas and splitAreas are the areas column codec (comma-separated).
func joinAreas(areas []DenylistArea) string {
	parts := make([]string, len(areas))
	for i, a := range areas {
		parts[i] = string(a)
	}
	return strings.Join(parts, ",")
}

func splitAreas(raw string) ([]DenylistArea, error) {
	if raw == "" {
		return nil, nil
	}
	var out []DenylistArea
	for _, part := range strings.Split(raw, ",") {
		a, err := ParseDenylistArea(part)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// boolInt is the 0/1 encoding of a stored flag.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// insertConfig writes the config row and its pending submission row.
func insertConfig(ctx context.Context, tx *sql.Tx, c LearnedConfig) error {
	changeJSON, err := json.Marshal(c.Change)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "learn: encode change")
	}
	evidenceJSON, err := json.Marshal(c.Evidence)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "learn: encode evidence")
	}
	at := c.Created.UnixNano()
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs_learned_config (id, target_id, scope_ref, label, confidence,
		tier, areas, loosens_bound, source_kind, source_ref, created_at, last_verified_at, success_count,
		failure_count, version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0)`,
		c.ID, string(c.Target), c.Scope, c.Label, c.Confidence, string(c.Tier), joinAreas(c.Areas),
		boolInt(c.LoosensBound), string(c.Source.Kind), c.Source.ID, at, at); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "learn: insert learned config")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs_learned_config_submission (config_id, change_json,
		evidence_json, status, submitted_at, status_changed_at) VALUES (?, ?, ?, ?, ?, ?)`,
		c.ID, string(changeJSON), string(evidenceJSON), string(c.Status), at, at); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "learn: insert learned-config submission")
	}
	return nil
}

// expectOneRow maps an exec result: an error is KindUnavailable, zero rows
// is KindNotFound, anything but one row is KindConflict.
func expectOneRow(res sql.Result, err error, what string) error {
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "learn: %s", what)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "learn: %s", what)
	}
	switch n {
	case 1:
		return nil
	case 0:
		return cascade.New(cascade.KindNotFound, "learn: unknown learned config")
	default:
		return cascade.Newf(cascade.KindConflict, "learn: %s touched %d rows", what, n)
	}
}

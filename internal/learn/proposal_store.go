package learn

// Purpose: ProposalStore, the transactional store of learned proposals: a
//   learned config (through ConfigStore.SubmitWith, the one ingress) and
//   the detection trigger that produced it, written in one transaction.
// Inputs: a Submission and its Trigger from DetectPatterns (P1-LRN-02).
// Outputs: Proposal values; rows in jobs_learn_proposal_trigger.
// Constraints: a proposal is created pending and this store never moves
//   its status, applies or routes it (P1-LRN-03 owns those). The tier comes
//   only from ClassifyChange inside SubmitWith. A trigger carries a sha256
//   signature, counts, times, a closed kind and opaque lane ids, never free
//   text; every lane id passes ValidateNoIdentifiers before any SQL runs.
// SPORT: internal.learn.ProposalStore/ADDED (P1-LRN-02).

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ProposedChangeKind is the closed kind of change a proposal makes.
type ProposedChangeKind string

// The closed ProposedChangeKind set.
const (
	ProposedRule     ProposedChangeKind = "rule"
	ProposedPolicy   ProposedChangeKind = "policy"
	ProposedWorkflow ProposedChangeKind = "workflow"
	ProposedCI       ProposedChangeKind = "ci"
)

// parseProposedChangeKind refuses (KindInvalidInput) a kind outside the set.
func parseProposedChangeKind(k ProposedChangeKind) (ProposedChangeKind, error) {
	switch k {
	case ProposedRule, ProposedPolicy, ProposedWorkflow, ProposedCI:
		return k, nil
	}
	return "", cascade.New(cascade.KindInvalidInput, "learn: change kind must be rule, policy, workflow or ci")
}

// LaneIdentity names a lane by its tier and node, both opaque ids.
type LaneIdentity struct {
	LaneTier, NodeID string
}

// Trigger is the detection evidence behind one proposal: the failure
// signature, its support inside the window and the lane that would gain.
type Trigger struct {
	Signature                    string
	Occurrences, DistinctSources int
	Beneficiary                  LaneIdentity
	WindowStart, WindowEnd       time.Time
	Kind                         ProposedChangeKind
}

// Proposal is a stored learned config with the trigger that produced it.
// Config carries the Change, Evidence, Status and computed Tier.
type Proposal struct {
	Config  LearnedConfig
	Trigger Trigger
}

// signaturePattern is a sha256 hex digest (FailureSignature's output).
var signaturePattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// validateTrigger refuses (KindInvalidInput) a malformed trigger.
func validateTrigger(t Trigger) error {
	if !signaturePattern.MatchString(t.Signature) {
		return cascade.New(cascade.KindInvalidInput, "learn: trigger signature must be a sha256 hex digest")
	}
	if t.Occurrences < 1 || t.DistinctSources < 1 {
		return cascade.New(cascade.KindInvalidInput, "learn: trigger counts must be positive")
	}
	if t.WindowStart.IsZero() || t.WindowEnd.IsZero() || t.WindowEnd.Before(t.WindowStart) {
		return cascade.New(cascade.KindInvalidInput, "learn: trigger window must be set and ordered")
	}
	if _, err := parseProposedChangeKind(t.Kind); err != nil {
		return err
	}
	for _, f := range [][2]string{{"Beneficiary.LaneTier", t.Beneficiary.LaneTier}, {"Beneficiary.NodeID", t.Beneficiary.NodeID}} {
		if err := ValidateNoIdentifiers(f[0], f[1]); err != nil {
			return err
		}
	}
	return nil
}

// ProposalStore stores proposals over a ConfigStore and the same db.
type ProposalStore struct {
	configs *ConfigStore
	db      *sql.DB
}

// NewProposalStore returns a store over c and db. Both are required, and
// db must be the database c writes (the schema is ConfigMigrationSet v2).
func NewProposalStore(c *ConfigStore, db *sql.DB) (*ProposalStore, error) {
	if c == nil || db == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "learn: NewProposalStore needs a config store and a db")
	}
	return &ProposalStore{configs: c, db: db}, nil
}

// Create stores sub as a pending learned config and t as its trigger in one
// transaction: either both rows land or neither does.
func (s *ProposalStore) Create(ctx context.Context, sub Submission, t Trigger) (Proposal, error) {
	if err := validateTrigger(t); err != nil {
		return Proposal{}, err
	}
	t.WindowStart, t.WindowEnd = time.Unix(0, t.WindowStart.UnixNano()).UTC(), time.Unix(0, t.WindowEnd.UnixNano()).UTC()
	c, err := s.configs.SubmitWith(ctx, sub, func(tx *sql.Tx, c LearnedConfig) error {
		return insertTrigger(ctx, tx, c.ID, t)
	})
	if err != nil {
		return Proposal{}, err
	}
	return Proposal{Config: c, Trigger: t}, nil
}

// insertTrigger writes the trigger row of config id inside tx.
func insertTrigger(ctx context.Context, tx *sql.Tx, id string, t Trigger) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO `+tableProposalTrigger+` (config_id, trigger_signature,
		occurrence_count, distinct_source_count, beneficiary_lane_tier, beneficiary_node_id, window_start,
		window_end, change_kind) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, t.Signature, t.Occurrences, t.DistinctSources, t.Beneficiary.LaneTier, t.Beneficiary.NodeID,
		t.WindowStart.UnixNano(), t.WindowEnd.UnixNano(), string(t.Kind))
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "learn: insert proposal trigger")
	}
	return nil
}

// selectTrigger reads trigger rows; callers append the WHERE clause.
const selectTrigger = `SELECT config_id, trigger_signature, occurrence_count, distinct_source_count,
	beneficiary_lane_tier, beneficiary_node_id, window_start, window_end, change_kind FROM ` + tableProposalTrigger

// queryTriggers runs a selectTrigger query and decodes every row by id.
func (s *ProposalStore) queryTriggers(ctx context.Context, query string, args ...any) (map[string]Trigger, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: query proposal triggers")
	}
	defer func() { _ = rows.Close() }()
	out := map[string]Trigger{}
	for rows.Next() {
		var id, kind string
		var t Trigger
		var start, end int64
		if err := rows.Scan(&id, &t.Signature, &t.Occurrences, &t.DistinctSources, &t.Beneficiary.LaneTier,
			&t.Beneficiary.NodeID, &start, &end, &kind); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: scan proposal trigger")
		}
		t.WindowStart, t.WindowEnd, t.Kind = time.Unix(0, start).UTC(), time.Unix(0, end).UTC(), ProposedChangeKind(kind)
		if err := validateTrigger(t); err != nil {
			return nil, err
		}
		out[id] = t
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: read proposal triggers")
	}
	return out, nil
}

// Get returns the proposal with config id. A config without a trigger row
// is not a proposal: KindNotFound.
func (s *ProposalStore) Get(ctx context.Context, id string) (Proposal, error) {
	triggers, err := s.queryTriggers(ctx, selectTrigger+" WHERE config_id = ?", id)
	if err != nil {
		return Proposal{}, err
	}
	t, ok := triggers[id]
	if !ok {
		return Proposal{}, cascade.New(cascade.KindNotFound, "learn: unknown proposal")
	}
	c, err := s.configs.Get(ctx, id)
	if err != nil {
		return Proposal{}, err
	}
	return Proposal{Config: c, Trigger: t}, nil
}

// List returns the proposals whose config matches f, newest first. Configs
// submitted without a trigger are not proposals and are left out.
func (s *ProposalStore) List(ctx context.Context, f ConfigFilter) ([]Proposal, error) {
	configs, err := s.configs.List(ctx, f)
	if err != nil {
		return nil, err
	}
	triggers, err := s.queryTriggers(ctx, selectTrigger)
	if err != nil {
		return nil, err
	}
	var out []Proposal
	for _, c := range configs {
		if t, ok := triggers[c.ID]; ok {
			out = append(out, Proposal{Config: c, Trigger: t})
		}
	}
	return out, nil
}

// LatestBySignature returns the proposal for sig with the latest window
// end (ties broken by config id, highest first); none is (zero, false, nil).
func (s *ProposalStore) LatestBySignature(ctx context.Context, sig string) (Proposal, bool, error) {
	if !signaturePattern.MatchString(sig) {
		return Proposal{}, false, cascade.New(cascade.KindInvalidInput, "learn: trigger signature must be a sha256 hex digest")
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT config_id FROM `+tableProposalTrigger+`
		WHERE trigger_signature = ? ORDER BY window_end DESC, config_id DESC LIMIT 1`, sig).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Proposal{}, false, nil
	}
	if err != nil {
		return Proposal{}, false, cascade.Wrap(cascade.KindUnavailable, err, "learn: look up proposal by signature")
	}
	p, err := s.Get(ctx, id)
	if err != nil {
		return Proposal{}, false, err
	}
	return p, true, nil
}

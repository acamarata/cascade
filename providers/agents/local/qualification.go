// Purpose: the R-21.170 per-model authoring qualification: running the
//   named fixture at providers/ollama/testdata/qualification/ against a
//   model id, recording the result in the `config` storage domain, and
//   resolving `authoring` from that recorded row alone.
// Inputs: a ModelExecutor (the sel-based model.execute leaf-dispatch seam
//   driver.go declares — see its package doc comment for why a raw
//   provider.ModelProvider is no longer held here), a ConfigStore (the
//   config-domain seam, injected — providers/** may import pkg/** only, so
//   internal/storage is never imported here), a Clock, and the fixture's
//   raw bytes.
// Outputs: QualificationRow (Run's result); a bool (Resolve's result).
// Constraints: Run is the ONLY write path; Resolve is read-only. A
//   missing row, a failing row, or a row whose FixtureHash does not match
//   the CURRENT fixture all resolve to false — fail closed, never a
//   permissive default (R-21.170). No exported setter exists anywhere in
//   this file.
// SPORT: providers.agents.local/ADD (P1-E30-W6-S62-T1); model-seam CHANGE
//   (raw ModelProvider -> ModelExecutor) —
//   FIX-manifest-collision-and-conductor-seam.

package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Clock abstracts the wall clock (no bare time.Now — repo-wide gate).
// Declared locally, structurally identical to other providers/** clocks:
// providers/** may import pkg/** only, so internal/runtime.Clock is
// never imported here.
type Clock interface {
	Now() time.Time
}

// ConfigStore is the `config` storage-domain seam this package persists
// and reads qualification rows through. The composition root binds it to
// internal/storage's config domain; this package never imports
// internal/storage directly (Art.7.2).
type ConfigStore interface {
	// Get returns key's value and true, or ok=false when key is unset.
	Get(ctx context.Context, key string) (value []byte, ok bool, err error)
	// Set writes key's value, replacing any prior value.
	Set(ctx context.Context, key string, value []byte) error
}

// FixtureCase is one qualification prompt and the substring its response
// must contain (case-insensitive) to count as a pass.
type FixtureCase struct {
	Prompt      string `json:"prompt"`
	MustContain string `json:"must_contain"`
}

// Fixture is the named qualification fixture's parsed shape.
type Fixture struct {
	Cases []FixtureCase `json:"cases"`
}

// ParseFixture parses raw fixture JSON. It fails closed on malformed JSON
// or a fixture with no cases — an empty fixture could never fail, which
// would make every model trivially "qualified".
func ParseFixture(data []byte) (Fixture, error) {
	var f Fixture
	if err := json.Unmarshal(data, &f); err != nil {
		return Fixture{}, cascade.Wrap(cascade.KindInvalidInput, err, "local: parse qualification fixture")
	}
	if len(f.Cases) == 0 {
		return Fixture{}, cascade.New(cascade.KindInvalidInput, "local: qualification fixture has no cases")
	}
	return f, nil
}

// FixtureHash returns the fixture's content-addressed hash. A recorded
// QualificationRow whose FixtureHash does not match the CURRENT fixture's
// hash is stale and reads as unqualified (R-21.170).
func FixtureHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// QualificationRow is the record Run persists per model id under
// agents.local.authoring_qualified.<model_id> in the `config` domain.
type QualificationRow struct {
	Passed      bool      `json:"passed"`
	FixtureHash string    `json:"fixture_hash"`
	ModelID     string    `json:"model_id"`
	RecordedAt  time.Time `json:"recorded_at"`
}

// configKey returns modelID's config-domain key.
func configKey(modelID string) string {
	return "agents.local.authoring_qualified." + modelID
}

// Qualifier runs the named fixture against a model and resolves whether
// that model currently carries the `authoring` capability. Run is the
// only write path; Resolve is read-only. Neither this type nor Driver
// exposes any other way to set a row.
type Qualifier struct {
	model       ModelExecutor
	store       ConfigStore
	clock       Clock
	fixture     Fixture
	fixtureData []byte
}

// NewQualifier validates its seams and the fixture, and returns a ready
// Qualifier.
func NewQualifier(model ModelExecutor, store ConfigStore, clock Clock, fixtureData []byte) (*Qualifier, error) {
	if model == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "local: qualifier model must not be nil")
	}
	if store == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "local: qualifier store must not be nil")
	}
	if clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "local: qualifier clock must not be nil")
	}
	fixture, err := ParseFixture(fixtureData)
	if err != nil {
		return nil, err
	}
	return &Qualifier{model: model, store: store, clock: clock, fixture: fixture, fixtureData: fixtureData}, nil
}

// Run executes every fixture case against modelID via the injected
// ModelProvider and records {passed, fixture_hash, model_id, recorded_at}
// — the ONLY write path for a qualification row. A single failing or
// erroring case fails the whole run.
func (q *Qualifier) Run(ctx context.Context, modelID string) (QualificationRow, error) {
	passed := q.runCases(ctx, modelID)
	row := QualificationRow{
		Passed:      passed,
		FixtureHash: FixtureHash(q.fixtureData),
		ModelID:     modelID,
		RecordedAt:  q.clock.Now(),
	}
	data, err := json.Marshal(row)
	if err != nil {
		return QualificationRow{}, cascade.Wrap(cascade.KindInternal, err, "local: marshal qualification row")
	}
	if err := q.store.Set(ctx, configKey(modelID), data); err != nil {
		return QualificationRow{}, err
	}
	return row, nil
}

// runCases reports whether every fixture case passes against modelID.
func (q *Qualifier) runCases(ctx context.Context, modelID string) bool {
	for _, c := range q.fixture.Cases {
		resp, err := q.model.Chat(ctx, modelSelection(modelID), provider.ChatRequest{
			Messages: []provider.ChatMessage{{Role: "user", Content: c.Prompt}},
			Model:    modelID,
		})
		if err != nil || !containsFold(resp.Message.Content, c.MustContain) {
			return false
		}
	}
	return true
}

// Resolve reports whether modelID currently carries the authoring
// capability: a passing row whose fixture_hash matches the CURRENT
// fixture. A missing row, a failing row, a stale fixture_hash, or a
// corrupt row all resolve to false — fail closed, with no other path to
// true (R-21.170).
func (q *Qualifier) Resolve(ctx context.Context, modelID string) (bool, error) {
	data, ok, err := q.store.Get(ctx, configKey(modelID))
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	var row QualificationRow
	if err := json.Unmarshal(data, &row); err != nil {
		return false, nil
	}
	if !row.Passed || row.FixtureHash != FixtureHash(q.fixtureData) {
		return false, nil
	}
	return true, nil
}

// containsFold reports whether s contains substr, case-insensitively. An
// empty substr always matches (a fixture case that asserts nothing beyond
// "the call did not error").
func containsFold(s, substr string) bool {
	if substr == "" {
		return true
	}
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

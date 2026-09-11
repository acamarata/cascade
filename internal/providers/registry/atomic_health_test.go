package registry

// Purpose: AtomicHealthUpdate's contract, branch by branch (its own file
// mirrors atomic_health.go). The persisted round-trip, including that fn
// sees the CURRENT row; the fn abort; the invalid-result abort; the
// not-found refusal; the corrupt-row refusal; the refused write; the
// closed handle. Every abort also asserts the stored row afterwards, so
// an abort can be told apart from a silent no-op.

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// upsertSeededProvider upserts one sample provider and fails loudly.
func upsertSeededProvider(t *testing.T, reg *Registry, name string) {
	t.Helper()
	if err := reg.UpsertProvider(context.Background(), sampleProvider(name)); err != nil {
		t.Fatalf("UpsertProvider(%s): %v", name, err)
	}
}

func TestAtomicHealthUpdatePersistsAndSeesCurrentRow(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	upsertSeededProvider(t, reg, "demote-me")

	updated, err := reg.AtomicHealthUpdate(ctx, "demote-me", func(cur ProviderRecord) (ProviderRecord, error) {
		cur.HealthStatus = HealthDead
		cur.DemotionCount = cur.DemotionCount + 1
		return cur, nil
	})
	if err != nil {
		t.Fatalf("AtomicHealthUpdate: %v", err)
	}
	if updated.DemotionCount != 1 || updated.HealthStatus != HealthDead {
		t.Fatalf("returned record = %+v", updated)
	}
	got, err := reg.GetProvider(ctx, "demote-me")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.DemotionCount != 1 || got.HealthStatus != HealthDead {
		t.Fatalf("the transform must be persisted, got %+v", got)
	}

	// fn runs against the CURRENT row, so a second call builds on the
	// first rather than resetting the counter.
	again, err := reg.AtomicHealthUpdate(ctx, "demote-me", func(cur ProviderRecord) (ProviderRecord, error) {
		cur.DemotionCount = cur.DemotionCount + 1
		return cur, nil
	})
	if err != nil {
		t.Fatalf("second AtomicHealthUpdate: %v", err)
	}
	if again.DemotionCount != 2 {
		t.Fatalf("second update must see the first: DemotionCount = %d", again.DemotionCount)
	}
}

func TestAtomicHealthUpdateAbortPaths(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	upsertSeededProvider(t, reg, "keep-me")

	refused := errors.New("probe refused")
	if _, err := reg.AtomicHealthUpdate(ctx, "keep-me", func(ProviderRecord) (ProviderRecord, error) {
		return ProviderRecord{}, refused
	}); !errors.Is(err, refused) {
		t.Fatalf("fn's own error must abort and pass through unchanged, got %v", err)
	}

	if _, err := reg.AtomicHealthUpdate(ctx, "keep-me", func(cur ProviderRecord) (ProviderRecord, error) {
		cur.Driver = "bogus-driver"
		return cur, nil
	}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("a transformed record failing Validate must abort as invalid input, got %v", err)
	}

	if _, err := reg.AtomicHealthUpdate(ctx, "never-there", func(cur ProviderRecord) (ProviderRecord, error) {
		return cur, nil
	}); !errors.Is(err, ErrProviderNotFound) {
		t.Fatalf("an unknown name must refuse with ErrProviderNotFound, got %v", err)
	}

	// Neither abort reached the database.
	got, err := reg.GetProvider(ctx, "keep-me")
	if err != nil {
		t.Fatalf("GetProvider after the aborts: %v", err)
	}
	if got.DemotionCount != 0 || got.Driver != DriverAnthropic {
		t.Fatalf("an aborted update must not write, got %+v", got)
	}
}

// TestAtomicHealthUpdateInfrastructureRefusals: a row the scanner cannot
// decode refuses as unavailable, a database trigger refusing the UPDATE
// refuses as unavailable, and a closed handle refuses at BEGIN.
func TestAtomicHealthUpdateInfrastructureRefusals(t *testing.T) {
	ctx := context.Background()

	corruptRow := newTestRegistry(t)
	upsertSeededProvider(t, corruptRow, "broken-row")
	if _, err := corruptRow.db.ExecContext(ctx,
		`UPDATE `+tableProviderRecords+` SET known_models = ? WHERE name = ?`, "{not-json", "broken-row"); err != nil {
		t.Fatalf("corrupt known_models: %v", err)
	}
	if _, err := corruptRow.AtomicHealthUpdate(ctx, "broken-row", func(cur ProviderRecord) (ProviderRecord, error) {
		return cur, nil
	}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("a corrupt row must refuse as unavailable, got %v", err)
	}

	execRefused := newTestRegistry(t)
	upsertSeededProvider(t, execRefused, "triggered")
	mustExecTrigger(t, execRefused.db, `CREATE TRIGGER refuse_provider_update BEFORE UPDATE ON `+
		tableProviderRecords+` BEGIN SELECT RAISE(ABORT, 'update refused'); END`)
	if _, err := execRefused.AtomicHealthUpdate(ctx, "triggered", func(cur ProviderRecord) (ProviderRecord, error) {
		cur.DemotionCount = cur.DemotionCount + 1
		return cur, nil
	}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("a refused UPDATE must surface as unavailable, got %v", err)
	}
	still, err := execRefused.GetProvider(ctx, "triggered")
	if err != nil || still.DemotionCount != 0 {
		t.Fatalf("the refused write must leave the row untouched, got %+v, %v", still, err)
	}

	closed := newTestRegistry(t)
	upsertSeededProvider(t, closed, "gone")
	if err := closed.db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := closed.AtomicHealthUpdate(ctx, "gone", func(cur ProviderRecord) (ProviderRecord, error) {
		return cur, nil
	}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("a closed handle must refuse at BEGIN, got %v", err)
	}
}

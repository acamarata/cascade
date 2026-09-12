package registry

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestImportProviders_InsertFailureRollsBackWholeTransaction(t *testing.T) {
	reg := newTestRegistry(t)
	mustExecTrigger(t, reg.db, `CREATE TRIGGER refuse_second_import BEFORE INSERT ON `+
		tableProviderRecords+` WHEN NEW.name = 'second' BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	_, err := reg.ImportProviders(context.Background(), []ProviderRecord{
		sampleProvider("first"), sampleProvider("second"),
	}, false)
	if err == nil || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("insert refusal = %v", err)
	}
	records, listErr := reg.ListProviders(context.Background())
	if listErr != nil || len(records) != 0 {
		t.Fatalf("failed transaction left rows: records=%v err=%v", records, listErr)
	}
}

func TestImportProviders_ValidatesWholeBatchBeforeWrite(t *testing.T) {
	reg := newTestRegistry(t)
	invalid := sampleProvider("second")
	invalid.Driver = "future"
	_, err := reg.ImportProviders(context.Background(), []ProviderRecord{
		sampleProvider("first"), invalid,
	}, false)
	if err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("invalid batch = %v", err)
	}
	records, listErr := reg.ListProviders(context.Background())
	if listErr != nil || len(records) != 0 {
		t.Fatalf("preflight refusal left rows: records=%v err=%v", records, listErr)
	}
}

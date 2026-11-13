package ingest

import (
	"errors"
	"testing"
)

// TestDeclarationsExist asserts that the stub declarations exist, have zero
// values, and return ErrNotImplemented.
func TestDeclarationsExist(t *testing.T) {
	if ErrNotImplemented == nil {
		t.Fatal("ErrNotImplemented is nil")
	}

	var cfg Config
	if cfg != (Config{}) {
		t.Fatalf("zero Config = %#v, want empty struct", cfg)
	}

	var row TicketRow
	if row.Key != "" {
		t.Fatalf("zero TicketRow.Key = %q, want empty string", row.Key)
	}

	roots, err := DiscoverPlanRoots(cfg)
	if roots != nil {
		t.Fatalf("DiscoverPlanRoots returned %v, want nil", roots)
	}
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("DiscoverPlanRoots error = %v, want ErrNotImplemented", err)
	}

	if err := Ingest(nil); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Ingest error = %v, want ErrNotImplemented", err)
	}
}

package secrets

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestHelperSpecTimeoutDefaultsToTenSeconds(t *testing.T) {
	if got := (HelperSpec{}).timeout(); got != 10*time.Second {
		t.Fatalf("zero timeout = %v, want 10s", got)
	}
	if got := (HelperSpec{Timeout: 3 * time.Second}).timeout(); got != 3*time.Second {
		t.Fatalf("explicit timeout = %v, want 3s", got)
	}
}

func TestPluralTail(t *testing.T) {
	if plural(1) != "y" || plural(0) != "ies" || plural(2) != "ies" {
		t.Fatalf("plural(1,0,2) = %q %q %q, want y ies ies", plural(1), plural(0), plural(2))
	}
}

func TestEmptyCustodyIsReadOnlyAndEmpty(t *testing.T) {
	ctx, c := context.Background(), emptyCustody{}
	if c.Name() != fileVaultName || !c.Available() {
		t.Fatalf("Name/Available = %q/%v, want the file vault, available", c.Name(), c.Available())
	}
	if err := c.Set(ctx, "k", []byte("v")); err == nil {
		t.Fatal("Set on an empty read-only custody succeeded")
	}
	if err := c.Delete(ctx, "k"); err == nil {
		t.Fatal("Delete on an empty read-only custody succeeded")
	}
	if names, err := c.List(ctx); err != nil || len(names) != 0 {
		t.Fatalf("List = %v, %v; want empty", names, err)
	}
	if _, err := c.Get(ctx, "k"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Get = %v, want not-found", err)
	}
}

func TestReadOnlyCustodyWrapperReportsInnerAndRefusesWrites(t *testing.T) {
	r := readOnlyCustody{inner: emptyCustody{}}
	if r.Name() != fileVaultName || !r.Available() {
		t.Fatalf("wrapper Name/Available = %q/%v", r.Name(), r.Available())
	}
	if err := r.Set(context.Background(), "k", nil); err == nil {
		t.Fatal("wrapper Set succeeded")
	}
	if err := r.Delete(context.Background(), "k"); err == nil {
		t.Fatal("wrapper Delete succeeded")
	}
	if names, err := r.List(context.Background()); err != nil || len(names) != 0 {
		t.Fatalf("wrapper List = %v, %v", names, err)
	}
}

package secrets

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestLazyCustodySelectsOnFirstUseOnce: nothing is selected until a method
// is called, the selection happens once, and a failed or nil selection is
// returned by every method.
func TestLazyCustodySelectsOnFirstUseOnce(t *testing.T) {
	ctx := context.Background()
	opens := 0
	vault := newMemCustody()
	lazy := LazyCustody(func() (Custody, error) { opens++; return vault, nil })
	if opens != 0 {
		t.Fatal("constructing the lazy custody selected one")
	}
	if err := lazy.Set(ctx, "FAKE_NAME", []byte("fake-value")); err != nil {
		t.Fatal(err)
	}
	if v, err := lazy.Get(ctx, "FAKE_NAME"); err != nil || string(v) != "fake-value" || opens != 1 {
		t.Fatalf("get = %q %v after %d selections", v, err, opens)
	}
	if names, err := lazy.List(ctx); err != nil || len(names) != 1 || !lazy.Available() || lazy.Name() != vault.Name() {
		t.Fatalf("list = %v %v", names, err)
	}
	if err := lazy.Delete(ctx, "FAKE_NAME"); err != nil || opens != 1 {
		t.Fatalf("delete = %v after %d selections", err, opens)
	}
	failing := LazyCustody(func() (Custody, error) { return nil, errors.New("fake: no vault") })
	if _, err := failing.Get(ctx, "FAKE_NAME"); err == nil || failing.Available() || failing.Name() != "none" {
		t.Fatalf("a failed selection = %v", err)
	}
	if err := failing.Set(ctx, "FAKE_NAME", nil); err == nil {
		t.Fatal("set on a failed selection succeeded")
	}
	if err := failing.Delete(ctx, "FAKE_NAME"); err == nil {
		t.Fatal("delete on a failed selection succeeded")
	}
	if _, err := failing.List(ctx); err == nil {
		t.Fatal("list on a failed selection succeeded")
	}
	empty := LazyCustody(func() (Custody, error) { return nil, nil })
	if _, err := empty.Get(ctx, "FAKE_NAME"); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("a nil selection = %v", err)
	}
}

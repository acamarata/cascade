// Purpose: unit coverage for registryAdapter.ListPool: the file's own
//   header comment documents this as an honest, disclosed stub (pool
//   membership has no home on registry.ProviderRecord yet), so the test
//   asserts exactly that documented contract - a nil result and a nil
//   error, regardless of input - rather than merely calling it.
// SPORT: cmd/cascade (ADD, coverage-floor fix).

package main

import (
	"context"
	"testing"
)

func TestRegistryAdapter_ListPool_HonestStub(t *testing.T) {
	a := registryAdapter{}
	items, err := a.ListPool(context.Background(), "any-pool")
	if err != nil {
		t.Fatalf("ListPool: unexpected error %v", err)
	}
	if items != nil {
		t.Errorf("ListPool = %#v, want nil (documented stub, no PoolIndex home yet)", items)
	}
}

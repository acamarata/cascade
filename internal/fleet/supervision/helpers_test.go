package supervision

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// brokenKVStore is a provider.Store that fails every call, for asserting
// Art.1's "an unverifiable subject reports StatusError" behavior.
type brokenKVStore struct{}

func (brokenKVStore) Get(context.Context, string, string) ([]byte, error) {
	return nil, cascade.New(cascade.KindUnavailable, "brokenKVStore: unreachable")
}

func (brokenKVStore) Put(context.Context, string, string, []byte) error {
	return cascade.New(cascade.KindUnavailable, "brokenKVStore: unreachable")
}

func (brokenKVStore) Delete(context.Context, string, string) error {
	return cascade.New(cascade.KindUnavailable, "brokenKVStore: unreachable")
}

func (brokenKVStore) Scan(context.Context, string, string) (provider.Iterator, error) {
	return nil, cascade.New(cascade.KindUnavailable, "brokenKVStore: unreachable")
}

func (brokenKVStore) Tx(context.Context, func(context.Context, provider.Tx) error) error {
	return cascade.New(cascade.KindUnavailable, "brokenKVStore: unreachable")
}

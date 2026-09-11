// Package fixture is a seeded fixture for TestRPCMethodGate_
// CatchesUnregisteredMethod: a `.Do(ctx, <method>, params, out)` call
// naming a method nothing in this file registers - the exact shape
// "conductor.execute" had before R-16.80's fix.
package fixture

import "context"

type doer struct{}

func (doer) Do(ctx context.Context, method string, params, out any) error { return nil }

func callUnregistered(ctx context.Context) error {
	var d doer
	var out any
	return d.Do(ctx, "fixture.unregistered_method", struct{}{}, &out)
}

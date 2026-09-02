//go:build postgres

// Package postgres is the build-tagged seam for cascade v2's server-profile
// provider.Store driver (02-TARGET-STRUCTURE.md §providers: the server
// profile maps postgres -> Store; pgvector/s3/redis are separate provider
// seams, out of this ticket's scope).
//
// Purpose: reserve providers/postgres/ (matching providers/sqlite from
//
//	S-02.T2) as the home Q/S-38.T4 completes with a real
//	jackc/pgx-backed driver, and prove — in W1, ahead of that ticket —
//	that the package compiles under `-tags=postgres`, every S-02.T1
//	provider.Store method is present with the right signature, and a
//	CI lane exists that can exercise it against a real postgres:17
//	service container (providers/postgres/testdata/README.md records
//	that lane's provenance and its allowed-fail status).
//
// Inputs: none — Driver holds no connection state. Open accepts a dsn
//
//	purely to fix the constructor's future signature for Q/S-38.T4; the
//	stub never dials it.
//
// Outputs: every Driver method returns cascade.ErrUnsupported
//
//	(cascade.KindUnsupported, A-T7 taxonomy) — never a panic, never nil,
//	never a silent no-op. That is the correct, honest stub contract for
//	an unimplemented seam (12-QUALITY-CONSTITUTION.md Art.1.3): the
//	`//go:build postgres` tag is the compile-time feature flag, so the
//	default build (`go build ./...`, no tag) never links this package
//	and makes no Postgres capability claim anywhere.
//
// Constraints: providers/** may import pkg/** only, never internal/**
//
//	(Art.10.2). This ticket (P1-E02-W1-S03-T5) delivers ONLY the Store
//	family seam and the docker CI lane; it does not implement wire
//	behavior, does not add a Postgres client dependency, and does not
//	register any other storage family (VectorStore/BlobStore/Cache/
//	Queue) — those are separate provider seams per 02-TARGET-STRUCTURE.
//
// SPORT: providers.postgres.Store/ADDED (P1-E02-W1-S03-T5).
package postgres

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Driver is the build-tagged Postgres provider.Store seam. It holds no
// connection state: every method refuses with cascade.ErrUnsupported
// rather than attempting any wire I/O. Q/S-38.T4 replaces this zero-value
// struct with a real jackc/pgx-backed implementation in this same package.
type Driver struct{}

// Open constructs the stub Driver. dsn is accepted (not validated, not
// dialed) so this signature already matches what Q/S-38.T4's real
// constructor needs — the seam's shape doesn't change out from under
// callers when the real driver lands, only its body does.
func Open(_ context.Context, _ string) (*Driver, error) {
	return &Driver{}, nil
}

// Get always returns cascade.ErrUnsupported: the Postgres wire driver is
// not yet built (Q/S-38.T4).
func (d *Driver) Get(_ context.Context, _, _ string) ([]byte, error) {
	return nil, cascade.ErrUnsupported
}

// Put always returns cascade.ErrUnsupported: the Postgres wire driver is
// not yet built (Q/S-38.T4).
func (d *Driver) Put(_ context.Context, _, _ string, _ []byte) error {
	return cascade.ErrUnsupported
}

// Delete always returns cascade.ErrUnsupported: the Postgres wire driver
// is not yet built (Q/S-38.T4).
func (d *Driver) Delete(_ context.Context, _, _ string) error {
	return cascade.ErrUnsupported
}

// Scan always returns cascade.ErrUnsupported: the Postgres wire driver is
// not yet built (Q/S-38.T4).
func (d *Driver) Scan(_ context.Context, _, _ string) (provider.Iterator, error) {
	return nil, cascade.ErrUnsupported
}

// Tx always returns cascade.ErrUnsupported without ever invoking fn: the
// Postgres wire driver is not yet built (Q/S-38.T4). Never calling fn
// means Tx cannot report a misleading partial-commit outcome.
func (d *Driver) Tx(_ context.Context, _ func(ctx context.Context, tx provider.Tx) error) error {
	return cascade.ErrUnsupported
}

// String identifies this driver in logs/diagnostics.
func (d *Driver) String() string { return "postgres.Driver(stub, Q/S-38.T4)" }

// var _ provider.Store enforces at compile time that Driver satisfies the
// full Store interface — a missing or mis-signed method fails the build
// rather than surfacing as a runtime type-assertion panic elsewhere.
var _ provider.Store = (*Driver)(nil)

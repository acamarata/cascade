//go:build postgres

// Purpose: the concrete server-profile driver construction — the only
//
//	place in the tree that imports both providers/postgres and
//	providers/pgvector, per Art.10.2 ("cmd is the composition root, so
//	internal/ never imports providers/"). internal/runtime/profile_server.go
//	holds the profile-agnostic pieces (DSN env-ref resolution, the live
//	migration closure); this file wires them to the real drivers.
//
// Inputs: a context, an os.Getenv-shaped env accessor, and a Clock.
// Outputs: a *runtime.ServerProfile (both drivers open and ready) or a
//
//	typed, fail-closed error — unreachable server, missing env-ref, auth
//	failure, migration failure, older-reader refusal, or (pgvector only)
//	the extension missing. Never a silent fallback to the local profile:
//	a construction failure is returned to the caller, which is
//	root.go's PersistentPreRunE — the command fails closed rather than
//	silently running against local storage under a `--profile server`
//	flag.
//
// Constraints: go:build postgres — the server profile is only linked into
//
//	binaries built with `-tags=postgres` (profile_server_stub.go is this
//	file's !postgres counterpart, returning a named refusal instead).
//
// SPORT: cmd/cascade.profile-server/ADDED (P1-E17-W4-S38-T4).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/providers/pgvector"
	"github.com/acamarata/cascade/providers/postgres"
)

// envRefPostgresDSN and envRefPgvectorDSN are the [storage] cold-section
// env-ref NAMES (08 §3) the server profile resolves, never literal DSNs.
// envRefPgvectorDSN falls back to the same value as envRefPostgresDSN when
// unset — pgvector is a Postgres extension, so the common case is one
// server hosting both families.
const (
	envRefPostgresDSN = "CASCADE_STORAGE_POSTGRES_DSN"
	envRefPgvectorDSN = "CASCADE_STORAGE_PGVECTOR_DSN"
)

// assembleServerProfile resolves both DSNs and opens the Postgres (Store)
// and pgvector (VectorStore) drivers. Redis (Cache) and S3 (BlobStore)
// slots are genuinely absent (S-38.T6/T7), never stubbed.
func assembleServerProfile(ctx context.Context, getenv runtime.EnvLookup, clock runtime.Clock) (*runtime.ServerProfile, error) {
	pgDSN, err := runtime.ResolveDSNEnvRef(getenv, envRefPostgresDSN)
	if err != nil {
		return nil, err
	}
	store, err := postgres.Open(ctx, pgDSN, postgres.WithMigrator(runtime.PostgresMigrator(clock)))
	if err != nil {
		return nil, err
	}

	vecDSN, err := runtime.ResolveDSNEnvRef(getenv, envRefPgvectorDSN)
	if err != nil {
		vecDSN = pgDSN // fall back to the Postgres DSN — see const doc comment
	}
	vector, err := pgvector.Open(ctx, vecDSN)
	if err != nil {
		_ = store.Close()
		return nil, err
	}

	return &runtime.ServerProfile{
		Store:  store,
		Vector: vector,
		CloseFn: func() error {
			errStore := store.Close()
			errVector := vector.Close()
			if errStore != nil {
				return errStore
			}
			return errVector
		},
	}, nil
}

// serverProfileBuildSupported reports true when this binary was built
// with -tags=postgres, so root.go can distinguish "not configured" from
// "not compiled in" without importing providers/** itself.
const serverProfileBuildSupported = true

//go:build postgres

// Purpose: the concrete server-profile driver construction — the only
//
//	place in the tree that imports providers/postgres, providers/pgvector,
//	providers/redis and providers/s3 together, per Art.10.2 ("cmd is the
//	composition root, so internal/ never imports providers/").
//	internal/runtime/profile_server.go holds the profile-agnostic pieces
//	(DSN/URL/env-ref-family resolution, the live migration closure); this
//	file wires them to the real drivers.
//
// Inputs: a context, an os.Getenv-shaped env accessor, and a Clock.
// Outputs: a *runtime.ServerProfile (every driver open and ready) or a
//
//	typed, fail-closed error — unreachable server, missing env-ref, auth
//	failure, migration failure, older-reader refusal, missing bucket, or
//	(pgvector only) the extension missing. Never a silent fallback to the
//	local profile: a construction failure is returned to the caller,
//	which is root.go's PersistentPreRunE — the command fails closed
//	rather than silently running against local storage under a
//	`--profile server` flag.
//
// Constraints: go:build postgres — the server profile is only linked into
//
//	binaries built with `-tags=postgres` (profile_server_stub.go is this
//	file's !postgres counterpart, returning a named refusal instead). The
//	build tag's name predates the Redis/S3 legs (P1-E17-W4-S38-T6/T7): it
//	was introduced by S-38.T4 for Postgres/pgvector alone and now also
//	gates Redis and S3, since this file is the server profile's one
//	composition site — a naming leftover from S-38.T4, not a claim that
//	either needs Postgres.
//
// SPORT: cmd/cascade.profile-server/CHANGED (P1-E17-W4-S38-T7).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/providers/pgvector"
	"github.com/acamarata/cascade/providers/postgres"
	"github.com/acamarata/cascade/providers/redis"
	"github.com/acamarata/cascade/providers/s3"
)

// envRefPostgresDSN, envRefPgvectorDSN, envRefRedisURL and envRefS3Prefix
// are the [storage]/[server] cold-section env-ref NAMES (08 §2/§3) the
// server profile resolves, never literal DSNs/URLs/credentials.
// envRefPgvectorDSN falls back to the same value as envRefPostgresDSN
// when unset — pgvector is a Postgres extension, so the common case is
// one server hosting both families. envRefRedisURL names 08 §2's
// redis_url_env slot; this file's own env-var naming (CASCADE_STORAGE_*)
// already diverges from 08's literal example names, so the Redis URL and
// S3 prefix follow the same, already-established real-code convention
// rather than 08's literal text.
const (
	envRefPostgresDSN = "CASCADE_STORAGE_POSTGRES_DSN"
	envRefPgvectorDSN = "CASCADE_STORAGE_PGVECTOR_DSN"
	envRefRedisURL    = "CASCADE_STORAGE_REDIS_URL"
	envRefS3Prefix    = "CASCADE_STORAGE_S3"
)

// assembleServerProfile resolves every env-ref and opens the Postgres
// (Store), pgvector (VectorStore), Redis (Cache + Queue) and S3
// (BlobStore) drivers — every family the server profile names (02
// §Profiles) is now real (Art.1.4).
func assembleServerProfile(ctx context.Context, getenv runtime.EnvLookup, clock runtime.Clock) (*runtime.ServerProfile, error) {
	store, vector, err := openStoreAndVector(ctx, getenv, clock)
	if err != nil {
		return nil, err
	}
	redisConn, err := openRedis(ctx, getenv)
	if err != nil {
		_ = store.Close()
		_ = vector.Close()
		return nil, err
	}
	s3Conn, err := openS3(ctx, getenv)
	if err != nil {
		_ = store.Close()
		_ = vector.Close()
		_ = redisConn.Close()
		return nil, err
	}

	return &runtime.ServerProfile{
		Store:  store,
		Vector: vector,
		Cache:  redis.NewCache(redisConn),
		Queue:  redis.NewQueue(redisConn, clock, redis.Config{}),
		Blob:   s3.NewBlobStore(s3Conn),
		CloseFn: func() error {
			return closeServerProfileDrivers(store, vector, redisConn, s3Conn)
		},
	}, nil
}

// openStoreAndVector resolves the Postgres and pgvector DSNs and opens
// both drivers, in the order the caller's cleanup on a later failure
// expects (pgvector.Open failing must still let the caller Close store).
func openStoreAndVector(ctx context.Context, getenv runtime.EnvLookup, clock runtime.Clock) (*postgres.Driver, *pgvector.Driver, error) {
	pgDSN, err := runtime.ResolveDSNEnvRef(getenv, envRefPostgresDSN)
	if err != nil {
		return nil, nil, err
	}
	store, err := postgres.Open(ctx, pgDSN, postgres.WithMigrator(runtime.PostgresMigrator(clock)))
	if err != nil {
		return nil, nil, err
	}
	vecDSN, err := runtime.ResolveDSNEnvRef(getenv, envRefPgvectorDSN)
	if err != nil {
		vecDSN = pgDSN // fall back to the Postgres DSN — see const doc comment
	}
	vector, err := pgvector.Open(ctx, vecDSN)
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	return store, vector, nil
}

// openRedis resolves the Redis URL and opens the shared connection the
// Cache and Queue legs both wrap.
func openRedis(ctx context.Context, getenv runtime.EnvLookup) (*redis.Conn, error) {
	redisURL, err := runtime.ResolveDSNEnvRef(getenv, envRefRedisURL)
	if err != nil {
		return nil, err
	}
	return redis.Open(ctx, redisURL)
}

// openS3 resolves the four-part S3 env-ref family and opens the
// connection the BlobStore leg wraps.
func openS3(ctx context.Context, getenv runtime.EnvLookup) (*s3.Conn, error) {
	endpoint, bucket, accessKeyID, secretAccessKey, err := runtime.ResolveS3EnvRefs(getenv, envRefS3Prefix)
	if err != nil {
		return nil, err
	}
	return s3.Open(ctx, endpoint, bucket, accessKeyID, secretAccessKey)
}

// closeServerProfileDrivers closes every driver, returning the first
// non-nil error but attempting every Close regardless of an earlier one.
func closeServerProfileDrivers(store *postgres.Driver, vector *pgvector.Driver, redisConn *redis.Conn, s3Conn *s3.Conn) error {
	errStore := store.Close()
	errVector := vector.Close()
	errRedis := redisConn.Close()
	errS3 := s3Conn.Close()
	for _, e := range []error{errStore, errVector, errRedis, errS3} {
		if e != nil {
			return e
		}
	}
	return nil
}

// serverProfileBuildSupported reports true when this binary was built
// with -tags=postgres, so root.go can distinguish "not configured" from
// "not compiled in" without importing providers/** itself.
const serverProfileBuildSupported = true

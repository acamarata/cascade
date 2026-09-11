// Purpose: the server-profile composition-root helper (P1-E17-W4-S38-T4,
//
//	extended by P1-E17-W4-S38-T6 for the Redis Cache+Queue legs and
//	P1-E17-W4-S38-T7 for the S3 BlobStore leg).
//	02-TARGET-STRUCTURE §Profiles: "server (Postgres/pgvector/S3/Redis)" —
//	this file holds the profile-agnostic pieces of that composition (DSN/
//	URL/env-ref-family resolution, the live schema migration Apply
//	wrapper) so cmd/ (the sole composition root, Art.10.2) can build the
//	concrete providers/postgres, providers/pgvector, providers/redis and
//	providers/s3 drivers without this package ever importing providers/**
//	itself.
//
// Inputs: an env-ref NAME (never a literal DSN/URL — 08 §3's [storage]
//
//	cold section stores per-family driver + DSN as env-ref names, resolved
//	through the process environment) plus a Clock for the migration
//	ledger's applied_at stamps. The S3 leg instead resolves an env-ref
//	PREFIX (08 §2's s3_env_prefix) expanding to four concrete environment
//	variables, since S3 config is inherently multi-part
//	(endpoint/bucket/key-id/secret), not a single connection string.
//
// Outputs: ResolveDSNEnvRef returns the resolved DSN/URL string or a
//
//	typed, fail-closed refusal (missing env-ref, or a value that looks
//	like a secret literal rather than an env-ref result) — reused as-is
//	for the Redis leg's URL, since a Redis connection URL is the same
//	"one env-ref name resolving to one connection string" shape as a
//	Postgres DSN. ResolveS3EnvRefs resolves the four-part family, each
//	part missing/unset failing closed the same way. PostgresMigrator
//	returns a func(ctx, *sql.DB) error closure — structurally assignable
//	to providers/postgres.Migrator without this package importing that
//	type — that runs the KV table's real migration via
//	internal/storage/migrate.Apply against migrate.PostgresEmitter{}.
//
// Constraints: internal/** may import internal/** freely but never
//
//	providers/** (Art.10.2: cmd is the sole composition root). No slot is
//	stubbed here (Art.1) — this file only ever adds a real resolver once
//	its corresponding ticket lands.
//
// SPORT: runtime.profile_server/CHANGED (P1-E17-W4-S38-T7).

package runtime

import (
	"context"
	"database/sql"
	"strings"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ServerProfile holds the composed server-profile drivers: Postgres
// (Store) + pgvector (VectorStore) + Redis (Cache + Queue) + S3
// (BlobStore) per 02-TARGET-STRUCTURE §Profiles — every family the server
// profile names is now real (Art.1.4). Close releases every driver's
// connection; it is nil-safe and idempotent-per-call-site.
type ServerProfile struct {
	Store   provider.Store
	Vector  provider.VectorStore
	Cache   provider.Cache
	Queue   provider.Queue
	Blob    provider.BlobStore
	CloseFn func() error
}

// Close calls CloseFn if set. Safe to call on a zero-value ServerProfile.
func (p *ServerProfile) Close() error {
	if p == nil || p.CloseFn == nil {
		return nil
	}
	return p.CloseFn()
}

// serverProfileCtxKey is unexported: WithServerProfile/ServerProfileFrom
// are the only way in or out, matching WithDaemonlessState's pattern in
// daemonless.go.
type serverProfileCtxKey struct{}

// WithServerProfile attaches p to ctx for downstream commands to read via
// ServerProfileFrom. cmd/cascade's PersistentPreRunE calls this once, when
// --profile server is selected, after constructing the concrete drivers
// (providers/postgres + providers/pgvector are only reachable from cmd/,
// per Art.10.2, so the construction itself happens there).
func WithServerProfile(ctx context.Context, p *ServerProfile) context.Context {
	return context.WithValue(ctx, serverProfileCtxKey{}, p)
}

// ServerProfileFrom retrieves the ServerProfile attached by
// WithServerProfile. ok is false when the local or worker profile is
// active (nothing was ever attached) — never a guessed answer standing in
// for a real one.
func ServerProfileFrom(ctx context.Context) (p *ServerProfile, ok bool) {
	p, ok = ctx.Value(serverProfileCtxKey{}).(*ServerProfile)
	return p, ok
}

// EnvLookup is the subset of process-environment access this file needs —
// injected so tests never touch the real environment (Art.7.1), matching
// profileEnv's shape in profiles.go.
type EnvLookup func(key string) (value string, ok bool)

// ResolveDSNEnvRef resolves envRefName through getenv and returns its
// value. A missing/unset env-ref is a typed, fail-closed refusal — never a
// silent empty DSN reaching a driver's Open. A value that is itself empty
// after lookup is treated the same as missing.
func ResolveDSNEnvRef(getenv EnvLookup, envRefName string) (string, error) {
	if envRefName == "" {
		return "", cascade.New(cascade.KindInvalidInput, "runtime: server profile requires a [storage] DSN env-ref name, got none")
	}
	if err := RefuseSecretLiteral(envRefName); err != nil {
		return "", err
	}
	if getenv == nil {
		return "", cascade.Newf(cascade.KindInvalidInput, "runtime: server profile env-ref %q: no environment accessor supplied", envRefName)
	}
	v, ok := getenv(envRefName)
	if !ok || v == "" {
		return "", cascade.Newf(cascade.KindInvalidInput, "runtime: server profile env-ref %q is unset — set it before selecting --profile server", envRefName)
	}
	return v, nil
}

// S3EnvRefSuffixes are the four concrete environment-variable name
// suffixes an s3_env_prefix (08 §2) expands to: prefix+"_ENDPOINT",
// prefix+"_BUCKET", prefix+"_KEY_ID", prefix+"_SECRET".
var s3EnvRefSuffixes = [4]string{"_ENDPOINT", "_BUCKET", "_KEY_ID", "_SECRET"}

// ResolveS3EnvRefs resolves the four env vars an s3_env_prefix (08 §2)
// names — endpoint, bucket, access key ID, secret access key — through
// getenv, failing closed on the first missing/unset one exactly like
// ResolveDSNEnvRef. prefix itself is checked with RefuseSecretLiteral
// (it must be a bare name, never a literal endpoint/credential); the
// four resolved VALUES are not — they are the real endpoint/bucket/
// credential themselves, the same as ResolveDSNEnvRef's own resolved
// DSN is not re-checked after lookup.
func ResolveS3EnvRefs(getenv EnvLookup, prefix string) (endpoint, bucket, accessKeyID, secretAccessKey string, err error) {
	if prefix == "" {
		return "", "", "", "", cascade.New(cascade.KindInvalidInput, "runtime: server profile requires a [server] s3_env_prefix, got none")
	}
	if err := RefuseSecretLiteral(prefix); err != nil {
		return "", "", "", "", err
	}
	if getenv == nil {
		return "", "", "", "", cascade.Newf(cascade.KindInvalidInput, "runtime: server profile s3_env_prefix %q: no environment accessor supplied", prefix)
	}
	values := make([]string, len(s3EnvRefSuffixes))
	for i, suffix := range s3EnvRefSuffixes {
		name := prefix + suffix
		v, ok := getenv(name)
		if !ok || v == "" {
			return "", "", "", "", cascade.Newf(cascade.KindInvalidInput, "runtime: server profile env-ref %q is unset — set it before selecting --profile server", name)
		}
		values[i] = v
	}
	return values[0], values[1], values[2], values[3], nil
}

// RefuseSecretLiteral implements 08 §2's rule at the [storage] boundary:
// a config VALUE that itself looks like a credential-bearing DSN (rather
// than an env-ref NAME naming where to find one) is a hard, fail-closed
// error, never silently accepted. A bare env-ref name (letters, digits,
// underscore — the shape config.toml's [storage] keys use) passes; a
// string that parses as a URL carrying userinfo, or that contains "://",
// does not.
func RefuseSecretLiteral(value string) error {
	if strings.Contains(value, "://") || strings.Contains(value, "@") {
		return cascade.New(cascade.KindInvalidInput,
			"runtime: [storage] value looks like a literal DSN, not an env-ref name — 08 §2 requires an env-ref name here, never a secret literal")
	}
	return nil
}

// kvMigrationSet is the real, minimal MigrationSet this ticket proves live
// against Postgres: the same namespace/key/value shape
// providers/postgres's schemaDDL constant creates, but authored through
// the DSL so the B/S-02.T3 golden-SQL contract (ordered apply,
// schema_version, reader_ceiling refusal) is exercised via
// internal/storage/migrate.Apply itself, not merely as generated text.
// providers/postgres.Open creates the same table independently (its own
// schemaDDL, CREATE TABLE IF NOT EXISTS) for callers that skip migration
// entirely; running this set afterward is a documented no-op, since both
// forms are idempotent CREATE TABLE IF NOT EXISTS.
func kvMigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         "runtime-profile-kv",
		SchemaVersion: 1,
		ReaderCeiling: 1,
		Steps: []migrate.MigrationStep{{
			Kind: migrate.StepCreateTable,
			Table: &migrate.TableDef{
				Name: "cascade_server_kv",
				Columns: []migrate.ColumnDef{
					{Name: "namespace", Type: migrate.TypeText, PrimaryKey: true},
					{Name: "key", Type: migrate.TypeText, PrimaryKey: true},
					{Name: "value", Type: migrate.TypeBlob},
				},
			},
			Description: "server profile: proof-of-live-migration kv anchor table",
		}},
	}
}

// PostgresMigrator returns a func(ctx, *sql.DB) error closure that applies
// kvMigrationSet live via internal/storage/migrate.Apply against
// migrate.PostgresEmitter{}. DBPath/BackupDir are left empty: the §D-18
// pre-migration snapshot is a SQLite-file concept (it copies a .db file),
// which has no meaning for a Postgres connection — ApplyConfig's own doc
// comment names this exact case ("Postgres, or an in-memory SQLite test
// database with nothing to copy") as when DBPath="" correctly disables it.
// The returned closure is structurally assignable to
// providers/postgres.Migrator (identical underlying func type) without
// this package importing that type.
func PostgresMigrator(clock Clock) func(ctx context.Context, db *sql.DB) error {
	return func(ctx context.Context, db *sql.DB) error {
		return migrate.Apply(ctx, migrate.ApplyConfig{
			DB:      db,
			Dialect: migrate.PostgresEmitter{},
			Clock:   clock,
		}, kvMigrationSet())
	}
}

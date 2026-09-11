// Purpose: the server-profile composition-root helper (P1-E17-W4-S38-T4).
//
//	02-TARGET-STRUCTURE §Profiles: "server (Postgres/pgvector/S3/Redis)" —
//	this file holds the profile-agnostic pieces of that composition (DSN
//	env-ref resolution, the live schema migration Apply wrapper) so cmd/
//	(the sole composition root, Art.10.2) can build the concrete
//	providers/postgres and providers/pgvector drivers without this
//	package ever importing providers/** itself.
//
// Inputs: an env-ref NAME (never a literal DSN — 08 §3's [storage] cold
//
//	section stores per-family driver + DSN as env-ref names, resolved
//	through the process environment) plus a Clock for the migration
//	ledger's applied_at stamps.
//
// Outputs: ResolveDSNEnvRef returns the resolved DSN string or a typed,
//
//	fail-closed refusal (missing env-ref, or a value that looks like a
//	secret literal rather than an env-ref result). PostgresMigrator
//	returns a func(ctx, *sql.DB) error closure — structurally assignable
//	to providers/postgres.Migrator without this package importing that
//	type — that runs the KV table's real migration via
//	internal/storage/migrate.Apply against migrate.PostgresEmitter{}.
//
// Constraints: internal/** may import internal/** freely but never
//
//	providers/** (Art.10.2: cmd is the sole composition root). Redis/S3
//	slots are genuinely absent from this file — S-38.T6/T7 add them, never
//	stubbed here (Art.1).
//
// SPORT: runtime.profile_server/ADDED (P1-E17-W4-S38-T4).

package runtime

import (
	"context"
	"database/sql"
	"strings"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ServerProfile holds the composed server-profile drivers: Postgres (Store)
// + pgvector (VectorStore) per 02-TARGET-STRUCTURE §Profiles. Redis
// (Cache) and S3 (BlobStore) slots are genuinely absent until S-38.T6/T7
// extend this struct — never stubbed here (Art.1). Close releases both
// drivers' connections; it is nil-safe and idempotent-per-call-site.
type ServerProfile struct {
	Store   provider.Store
	Vector  provider.VectorStore
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

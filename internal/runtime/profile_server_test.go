// Purpose: unit tests for the server-profile composition helpers —
//
//	ResolveDSNEnvRef, RefuseSecretLiteral, and kvMigrationSet's DDL shape
//	via migrate.PostgresEmitter{}. None of these need a live Postgres
//	server: the live proof (ordered apply, schema_version,
//	reader_ceiling refusal, all against a real server) is
//	providers/postgres/integration_test.go's job — this file proves the
//	profile-agnostic pieces this package owns.
//
// SPORT: runtime.profile_server/ADDED (P1-E17-W4-S38-T4).
package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestServerProfileContext_RoundTrip(t *testing.T) {
	ctx := context.Background()
	if _, ok := ServerProfileFrom(ctx); ok {
		t.Fatal("ServerProfileFrom(background) = ok=true, want false")
	}
	p := &ServerProfile{}
	ctx = WithServerProfile(ctx, p)
	got, ok := ServerProfileFrom(ctx)
	if !ok || got != p {
		t.Fatalf("ServerProfileFrom after WithServerProfile = %v, %v, want %v, true", got, ok, p)
	}
}

func TestServerProfile_CloseNilSafe(t *testing.T) {
	var p *ServerProfile
	if err := p.Close(); err != nil {
		t.Fatalf("nil ServerProfile.Close() = %v, want nil", err)
	}
	closed := false
	p2 := &ServerProfile{CloseFn: func() error { closed = true; return nil }}
	if err := p2.Close(); err != nil || !closed {
		t.Fatalf("Close() = %v, closed=%v, want nil/true", err, closed)
	}
}

func TestResolveDSNEnvRef_Success(t *testing.T) {
	getenv := func(k string) (string, bool) {
		if k == "CASCADE_POSTGRES_DSN" {
			return "postgres://u:p@host/db", true
		}
		return "", false
	}
	got, err := ResolveDSNEnvRef(getenv, "CASCADE_POSTGRES_DSN")
	if err != nil {
		t.Fatalf("ResolveDSNEnvRef: %v", err)
	}
	if got != "postgres://u:p@host/db" {
		t.Fatalf("ResolveDSNEnvRef = %q, want the resolved DSN", got)
	}
}

func TestResolveDSNEnvRef_MissingEnvRefName(t *testing.T) {
	_, err := ResolveDSNEnvRef(func(string) (string, bool) { return "", false }, "")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ResolveDSNEnvRef(empty name) = %v, want KindInvalidInput", err)
	}
}

func TestResolveDSNEnvRef_UnsetEnvRef(t *testing.T) {
	_, err := ResolveDSNEnvRef(func(string) (string, bool) { return "", false }, "CASCADE_POSTGRES_DSN")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ResolveDSNEnvRef(unset) = %v, want KindInvalidInput", err)
	}
	if !strings.Contains(err.Error(), "CASCADE_POSTGRES_DSN") {
		t.Fatalf("ResolveDSNEnvRef(unset) error = %v, want it to name the env-ref", err)
	}
}

func TestResolveDSNEnvRef_NilGetenv(t *testing.T) {
	_, err := ResolveDSNEnvRef(nil, "CASCADE_POSTGRES_DSN")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ResolveDSNEnvRef(nil getenv) = %v, want KindInvalidInput", err)
	}
}

func TestRefuseSecretLiteral(t *testing.T) {
	cases := []struct {
		value   string
		wantErr bool
	}{
		{"CASCADE_POSTGRES_DSN", false},
		{"postgres://u:p@host/db", true},
		{"user@host", true},
		{"", false},
	}
	for _, c := range cases {
		err := RefuseSecretLiteral(c.value)
		if c.wantErr && err == nil {
			t.Errorf("RefuseSecretLiteral(%q) = nil, want a refusal", c.value)
		}
		if c.wantErr && !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("RefuseSecretLiteral(%q) = %v, want KindInvalidInput", c.value, err)
		}
		if !c.wantErr && err != nil {
			t.Errorf("RefuseSecretLiteral(%q) = %v, want nil", c.value, err)
		}
	}
}

// TestKVMigrationSet_EmitsPortableDDL proves kvMigrationSet's shape
// against migrate.PostgresEmitter{} directly (text-level, no DB): the
// live proof that this DDL actually applies in order against a real
// server is providers/postgres/integration_test.go's
// TestPostgresLiveMigration.
func TestKVMigrationSet_EmitsPortableDDL(t *testing.T) {
	set := kvMigrationSet()
	if set.SchemaVersion != 1 || set.ReaderCeiling != 1 {
		t.Fatalf("kvMigrationSet versions = %d/%d, want 1/1", set.SchemaVersion, set.ReaderCeiling)
	}
	stmts, err := migrate.PostgresEmitter{}.Emit(set)
	if err != nil {
		t.Fatalf("PostgresEmitter.Emit: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("Emit returned %d statements, want 1", len(stmts))
	}
	if !strings.Contains(stmts[0], "cascade_server_kv") || !strings.Contains(stmts[0], "CREATE TABLE") {
		t.Fatalf("Emit statement = %q, want a CREATE TABLE cascade_server_kv", stmts[0])
	}
}

// stubClock is a minimal migrate.Clock for the constructor smoke test
// below — PostgresMigrator itself is only exercised end-to-end against a
// real DB by the docker-tagged integration lane, but its constructor
// shape (it must return a non-nil closure without touching any I/O) is
// cheap to prove here.
type stubClock struct{ t time.Time }

func (c stubClock) Now() time.Time { return c.t }

func TestPostgresMigrator_ReturnsClosure(t *testing.T) {
	fn := PostgresMigrator(stubClock{t: time.Unix(0, 0)})
	if fn == nil {
		t.Fatal("PostgresMigrator returned a nil closure")
	}
}

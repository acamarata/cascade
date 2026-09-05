package forget

// The refusal paths. Every one of them asserts a taxonomy kind and a
// state, because a destructive verb whose failures are untested is a verb
// whose failures are discovered by a user.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestForgetHonoursACanceledContext proves the pipeline stops before it
// removes anything when the caller has already given up.
func TestForgetHonoursACanceledContext(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindProject, "alpha", "the alpha body\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := f.pipe.Forget(ctx, id, "")
	wantKind(t, err, cascade.KindCanceled)
	if _, rerr := f.store.Read(context.Background(), memory.KindProject, "alpha"); rerr != nil {
		t.Fatalf("a canceled forget removed the record anyway: %v", rerr)
	}
}

// TestPipelineWithNoSinkStillRetires covers the documented no-bus
// configuration: the record goes, and the outcome still reports where the
// note ended up rather than claiming a delivery.
func TestPipelineWithNoSinkStillRetires(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindProject, "alpha", "the alpha body\n")
	pipe := NewPipeline(f.base, f.store, f.clock, nil).WithIndex(f.job)

	out, err := pipe.Forget(context.Background(), id, "asked to")
	if err != nil {
		t.Fatalf("Forget with no sink: %v", err)
	}
	if !out.Forgotten || !out.EventEmitted {
		t.Fatalf("outcome = %+v, want the retirement done and the discard sink accepted", out)
	}
	if _, rerr := f.store.Read(context.Background(), memory.KindProject, "alpha"); rerr == nil {
		t.Fatal("the record survived a forget through a pipeline with no sink")
	}
}

// TestPipelineWithNoIndexReportsNotConfigured proves the pipeline never
// reports a clean index it did not look at.
func TestPipelineWithNoIndexReportsNotConfigured(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindProject, "alpha", "the alpha body mentions pelicans\n")
	pipe := NewPipeline(f.base, f.store, f.clock, f.sink)

	out, err := pipe.Forget(context.Background(), id, "asked to")
	if err != nil {
		t.Fatalf("Forget with no index: %v", err)
	}
	for _, place := range []string{"projection rows and postings", "vector index"} {
		if got := traceFor(t, out, place).Disposition; got != memory.ForgetNotConfigured {
			t.Errorf("%s: disposition %q, want %q", place, got, memory.ForgetNotConfigured)
		}
	}
	if len(f.searchHits(t, "pelicans")) != 1 {
		t.Fatal("a pipeline with no index scrubbed one anyway")
	}
}

// TestForgetSurfacesAStoreFailure proves a store that cannot answer is an
// unavailability refusal, not a silent success.
func TestForgetSurfacesAStoreFailure(t *testing.T) {
	f := newFixture(t)
	broken := NewPipeline(f.base, failingExistsStore{err: cascade.New(cascade.KindUnavailable, "disk gone")},
		f.clock, f.sink)
	_, err := broken.Forget(context.Background(), "project/alpha", "")
	wantKind(t, err, cascade.KindUnavailable)
}

// TestForgetSurfacesAnUnwritableAccount proves the pipeline refuses when
// it cannot record its own intent, rather than removing a record it can
// give no account of.
func TestForgetSurfacesAnUnwritableAccount(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindProject, "alpha", "the alpha body\n")
	// A regular file where the accounts directory must be makes every
	// account write fail, without touching the record tree.
	blocker := filepath.Join(f.base, accountsDir)
	if err := os.WriteFile(blocker, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatalf("seeding the blocker: %v", err)
	}
	_, err := f.pipe.Forget(context.Background(), id, "")
	wantKind(t, err, cascade.KindUnavailable)
	if !errors.Is(err, memory.ErrStoreIO) {
		t.Fatalf("error %v does not wrap ErrStoreIO", err)
	}
	if _, rerr := f.store.Read(context.Background(), memory.KindProject, "alpha"); rerr != nil {
		t.Fatalf("the record was retired although its account could not be written: %v", rerr)
	}
}

// TestDecodeAccountRefusesAnAccountNamingNoEntity closes the last way an
// account file could be read as valid while saying nothing.
func TestDecodeAccountRefusesAnAccountNamingNoEntity(t *testing.T) {
	_, err := decodeAccount([]byte(`{"schema_version":1}`))
	if !errors.Is(err, ErrMalformedAccount) {
		t.Fatalf("error = %v, want ErrMalformedAccount", err)
	}
	wantKind(t, err, cascade.KindIntegrity)
}

// TestAccountEncodingIsDeterministic keeps the file byte-stable, so an
// unchanged account is not rewritten and two machines agree.
func TestAccountEncodingIsDeterministic(t *testing.T) {
	acct := newAccount("project/alpha", "asked to", testEpoch)
	first, err := encodeAccount(acct)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	for i := 0; i < 8; i++ {
		again, aerr := encodeAccount(acct)
		if aerr != nil {
			t.Fatalf("encoding: %v", aerr)
		}
		if string(again) != string(first) {
			t.Fatalf("encoding run %d differs from the first", i)
		}
	}
	back, derr := decodeAccount(first)
	if derr != nil {
		t.Fatalf("decoding what we encoded: %v", derr)
	}
	if back.EntityID != acct.EntityID || back.Reason != acct.Reason ||
		!back.RequestedAt.Equal(acct.RequestedAt) {
		t.Fatalf("round trip = %+v, want %+v", back, acct)
	}
}

// failingExistsStore refuses the liveness question itself.
type failingExistsStore struct{ err error }

func (s failingExistsStore) Exists(context.Context, memory.MemoryKind, string) (bool, error) {
	return false, s.err
}

func (s failingExistsStore) Delete(context.Context, memory.MemoryKind, string) error { return s.err }

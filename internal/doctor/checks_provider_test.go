package doctor

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeProviderHealthSource drives providerHealthCheck without a real
// registry, per Art.7.2 (no network/DB in the default unit lane).
type fakeProviderHealthSource struct {
	rows        []ProviderHealthRow
	listErr     error
	recoverOK   map[string]bool
	recoverErr  map[string]error
	recoverCall []string
}

func (f *fakeProviderHealthSource) ListProviderHealth(context.Context) ([]ProviderHealthRow, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.rows, nil
}

func (f *fakeProviderHealthSource) RecoverProviderHealth(_ context.Context, name string) (bool, error) {
	f.recoverCall = append(f.recoverCall, name)
	if err, ok := f.recoverErr[name]; ok {
		return false, err
	}
	return f.recoverOK[name], nil
}

func TestProviderHealthCheck_NilSource_ErrorsCannotDetermine(t *testing.T) {
	c := NewProviderHealthCheck(nil)
	res, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if res.Status != StatusError {
		t.Fatalf("Run: status = %v, want StatusError (cannot determine health)", res.Status)
	}
}

func TestProviderHealthCheck_ListError_ErrorsCannotDetermine(t *testing.T) {
	c := NewProviderHealthCheck(&fakeProviderHealthSource{listErr: cascade.New(cascade.KindUnavailable, "boom")})
	res, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if res.Status != StatusError {
		t.Fatalf("Run: status = %v, want StatusError", res.Status)
	}
}

func TestProviderHealthCheck_AllHealthy_OK(t *testing.T) {
	src := &fakeProviderHealthSource{rows: []ProviderHealthRow{{Name: "a", Status: "healthy"}, {Name: "b", Status: "healthy"}}}
	c := NewProviderHealthCheck(src)
	res, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if res.Status != StatusOK {
		t.Fatalf("Run: status = %v, want StatusOK", res.Status)
	}
}

func TestProviderHealthCheck_UnknownIsWarnNotOK(t *testing.T) {
	// A never-probed provider (Status "") must be a DISTINCT, visible
	// outcome from healthy -- StatusWarn, never StatusOK.
	src := &fakeProviderHealthSource{rows: []ProviderHealthRow{{Name: "a", Status: ""}}}
	c := NewProviderHealthCheck(src)
	res, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if res.Status != StatusWarn {
		t.Fatalf("Run: status = %v, want StatusWarn (cannot determine != healthy)", res.Status)
	}
	if res.Status == StatusOK {
		t.Fatal("an unverifiable provider must never report StatusOK")
	}
}

func TestProviderHealthCheck_DeadOrDegraded_Error(t *testing.T) {
	src := &fakeProviderHealthSource{rows: []ProviderHealthRow{{Name: "a", Status: "dead"}, {Name: "b", Status: "healthy"}}}
	c := NewProviderHealthCheck(src)
	res, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if res.Status != StatusError {
		t.Fatalf("Run: status = %v, want StatusError", res.Status)
	}
}

func TestProviderHealthCheck_EmptyRegistry_OK(t *testing.T) {
	c := NewProviderHealthCheck(&fakeProviderHealthSource{rows: nil})
	res, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if res.Status != StatusOK {
		t.Fatalf("Run: status = %v, want StatusOK for an empty registry", res.Status)
	}
}

func TestProviderHealthCheck_Fix_ReprobesUnhealthyOnly(t *testing.T) {
	src := &fakeProviderHealthSource{
		rows:      []ProviderHealthRow{{Name: "healthy-one", Status: "healthy"}, {Name: "dead-one", Status: "dead"}},
		recoverOK: map[string]bool{"dead-one": true},
	}
	c := NewProviderHealthCheck(src)
	res, err := c.Fix(context.Background())
	if err != nil {
		t.Fatalf("Fix: unexpected error: %v", err)
	}
	if !res.Applied {
		t.Fatal("Fix: expected Applied=true when an unhealthy provider was re-probed")
	}
	if len(src.recoverCall) != 1 || src.recoverCall[0] != "dead-one" {
		t.Fatalf("Fix: RecoverProviderHealth calls = %v, want exactly [dead-one]", src.recoverCall)
	}
}

func TestProviderHealthCheck_Fix_NeverEvictsOrDeletes(t *testing.T) {
	// R-14.35: Fix re-probes and reports; there is no delete/evict path
	// on ProviderHealthSource at all -- the interface itself makes
	// eviction unreachable from Fix, which this test documents.
	src := &fakeProviderHealthSource{rows: []ProviderHealthRow{{Name: "dead-one", Status: "dead"}}, recoverOK: map[string]bool{}}
	c := NewProviderHealthCheck(src)
	if _, err := c.Fix(context.Background()); err != nil {
		t.Fatalf("Fix: unexpected error: %v", err)
	}
	// The provider is still present in a follow-up Run (nothing removed
	// it) -- rows is unmodified by Fix, so ListProviderHealth still
	// reports it.
	res, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if res.Status != StatusError {
		t.Fatalf("Run after Fix: status = %v, want StatusError (still dead, never evicted)", res.Status)
	}
}

func TestProviderHealthCheck_Fix_AlreadyHealthy_Idempotent(t *testing.T) {
	src := &fakeProviderHealthSource{rows: []ProviderHealthRow{{Name: "a", Status: "healthy"}}}
	c := NewProviderHealthCheck(src)
	res, err := c.Fix(context.Background())
	if err != nil {
		t.Fatalf("Fix: unexpected error: %v", err)
	}
	if res.Applied {
		t.Fatal("Fix: expected Applied=false when every provider is already healthy")
	}
	if len(src.recoverCall) != 0 {
		t.Fatalf("Fix: RecoverProviderHealth should not be called for a healthy provider, got %v", src.recoverCall)
	}
}

func TestProviderHealthCheck_Fix_NilSource_NotFixable(t *testing.T) {
	c := NewProviderHealthCheck(nil)
	_, err := c.Fix(context.Background())
	if !errors.Is(err, ErrCheckNotFixable) {
		t.Fatalf("Fix: error = %v, want ErrCheckNotFixable", err)
	}
}

func TestProviderHealthCheck_Metadata(t *testing.T) {
	c := NewProviderHealthCheck(&fakeProviderHealthSource{})
	meta := c.Metadata()
	if !meta.FirstRun || !meta.Fixable {
		t.Fatalf("Metadata = %+v, want FirstRun=true Fixable=true", meta)
	}
	if c.Name() != "provider_health" {
		t.Fatalf("Name() = %q, want provider_health", c.Name())
	}
}

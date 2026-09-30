package secrets

// Purpose: probePlatform's contract, checked with fake Custody values so it
//   runs on every OS: a backend that can probe is asked, and its answer is
//   returned untouched; any other backend is judged by Available.
// Constraints: no platform backend, no keychain, no build tags.

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// probeFake is a Custody whose Available answer is fixed and which counts
// the calls made to it.
type probeFake struct {
	name           string
	available      bool
	availableCalls int
}

func (f *probeFake) Name() string { return f.name }
func (f *probeFake) Available() bool {
	f.availableCalls++
	return f.available
}
func (f *probeFake) Set(context.Context, string, []byte) error   { return errors.New("unused") }
func (f *probeFake) Get(context.Context, string) ([]byte, error) { return nil, errors.New("unused") }
func (f *probeFake) Delete(context.Context, string) error        { return errors.New("unused") }
func (f *probeFake) List(context.Context) ([]string, error)      { return nil, errors.New("unused") }

// probingFake adds the optional probe method that probePlatform prefers.
type probingFake struct {
	probeFake
	result     error
	probeCalls int
}

func (f *probingFake) probe(context.Context) error {
	f.probeCalls++
	return f.result
}

// errProbeSentinel is the test-local error a probing fake returns; no
// production value can be mistaken for it.
var errProbeSentinel = errors.New("probe sentinel")

// TestProbePlatformUsesProbeWhenAvailable: a backend that can probe is asked
// through probe and its error comes back as the very same value, even though
// its Available would say true. Available is never consulted.
func TestProbePlatformUsesProbeWhenAvailable(t *testing.T) {
	plat := &probingFake{probeFake: probeFake{name: "probing", available: true}, result: errProbeSentinel}
	if got := probePlatform(plat); got != errProbeSentinel {
		t.Fatalf("probePlatform = %v, want the probe's own sentinel", got)
	}
	if plat.probeCalls != 1 {
		t.Fatalf("probe called %d times, want 1", plat.probeCalls)
	}
	if plat.availableCalls != 0 {
		t.Fatalf("Available called %d times, want 0 when probe answers", plat.availableCalls)
	}
}

// TestProbePlatformAvailableTrue: a backend without a probe method that
// reports Available is usable, so probePlatform returns nil.
func TestProbePlatformAvailableTrue(t *testing.T) {
	plat := &probeFake{name: "plain", available: true}
	if err := probePlatform(plat); err != nil {
		t.Fatalf("probePlatform = %v, want nil for an available backend", err)
	}
	if plat.availableCalls != 1 {
		t.Fatalf("Available called %d times, want 1", plat.availableCalls)
	}
}

// TestProbePlatformAvailableFalseRefuses: an unavailable backend without a
// probe method is refused with the plain unavailable error, never the
// cleanup-failure refusal (which never falls back). ErrCustodyUnavailable is
// a constructor, so the value is checked structurally: type, Kind, exact
// message, no cause.
func TestProbePlatformAvailableFalseRefuses(t *testing.T) {
	plat := &probeFake{name: "plain", available: false}
	err := probePlatform(plat)
	if err == nil {
		t.Fatal("probePlatform = nil for an unavailable backend, want a refusal")
	}
	if err == ErrProbeCleanupFailed {
		t.Fatal("an unavailable backend must not read as a cleanup failure, which never falls back")
	}
	var ce *cascade.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %T is not a *cascade.Error", err)
	}
	if ce.Kind != cascade.KindUnavailable {
		t.Fatalf("Kind = %v, want KindUnavailable", ce.Kind)
	}
	const wantMsg = "secrets: the plain custody backend is not available on this host"
	if ce.Msg != wantMsg {
		t.Fatalf("Msg = %q, want %q", ce.Msg, wantMsg)
	}
	if u := errors.Unwrap(err); u != nil {
		t.Fatalf("refusal wraps %v, want no cause", u)
	}
	if got, want := err.Error(), "unavailable: "+wantMsg; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if plat.availableCalls != 1 {
		t.Fatalf("Available called %d times, want 1", plat.availableCalls)
	}
}

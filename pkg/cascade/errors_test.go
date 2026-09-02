package cascade_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestKindEnumerationClosed locks R-14.3's frozen 14-kind enumeration: exact
// count, exact members, exact order. Any future edit that adds, removes, or
// reorders a kind fails here first.
func TestKindEnumerationClosed(t *testing.T) {
	want := []cascade.Kind{
		cascade.KindNotFound,
		cascade.KindInvalidInput,
		cascade.KindConflict,
		cascade.KindUnavailable,
		cascade.KindTimeout,
		cascade.KindCanceled,
		cascade.KindPermissionDenied,
		cascade.KindElevationRequired,
		cascade.KindPolicyDenied,
		cascade.KindCapabilityDenied,
		cascade.KindQuotaExhausted,
		cascade.KindUnsupported,
		cascade.KindIntegrity,
		cascade.KindInternal,
	}
	got := cascade.AllKinds()
	if len(got) != 14 {
		t.Fatalf("AllKinds() has %d members, want exactly 14 (R-14.3 frozen enumeration)", len(got))
	}
	for i, k := range want {
		if got[i] != k {
			t.Errorf("AllKinds()[%d] = %v, want %v", i, got[i], k)
		}
	}
}

func TestKindStringAndValid(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range cascade.AllKinds() {
		if !k.Valid() {
			t.Errorf("Kind %v (%s) reports Valid() == false", k, k)
		}
		s := k.String()
		if s == "" || s == "invalid-kind" {
			t.Errorf("Kind %v has no stable String(), got %q", k, s)
		}
		if seen[s] {
			t.Errorf("Kind string %q is not unique", s)
		}
		seen[s] = true
	}

	var zero cascade.Kind
	if zero.Valid() {
		t.Error("the zero Kind value must not be Valid")
	}
	if zero.String() != "invalid-kind" {
		t.Errorf("zero Kind String() = %q, want %q", zero.String(), "invalid-kind")
	}
}

func TestNewAndNewf(t *testing.T) {
	err := cascade.New(cascade.KindNotFound, "widget missing")
	if err.Kind != cascade.KindNotFound {
		t.Fatalf("New: Kind = %v, want KindNotFound", err.Kind)
	}
	if got, want := err.Error(), "not-found: widget missing"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}

	errf := cascade.Newf(cascade.KindInvalidInput, "field %q: %d out of range", "count", -1)
	want := `invalid-input: field "count": -1 out of range`
	if got := errf.Error(); got != want {
		t.Errorf("Newf Error() = %q, want %q", got, want)
	}
}

func TestWrapAndWrapf(t *testing.T) {
	cause := errors.New("disk full")
	wrapped := cascade.Wrap(cascade.KindUnavailable, cause, "writing snapshot")
	if !errors.Is(wrapped, cause) {
		t.Error("Wrap: errors.Is does not find the wrapped cause")
	}
	if got, want := wrapped.Error(), "unavailable: writing snapshot: disk full"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}

	wrappedf := cascade.Wrapf(cascade.KindTimeout, cause, "after %d attempts", 3)
	if got, want := wrappedf.Error(), "timeout: after 3 attempts: disk full"; got != want {
		t.Errorf("Wrapf Error() = %q, want %q", got, want)
	}
}

func TestError_EmptyMessageFallsBackToKind(t *testing.T) {
	e := &cascade.Error{Kind: cascade.KindInternal}
	if got, want := e.Error(), "internal"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}

	cause := errors.New("boom")
	e2 := &cascade.Error{Kind: cascade.KindInternal, Err: cause}
	if got, want := e2.Error(), "internal: boom"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestErrorIs_MatchesByKind is the core wrapping/inspection contract (task
// 3): errors.Is treats any two taxonomy errors of the same Kind as
// equivalent regardless of message, and different kinds as distinct.
func TestErrorIs_MatchesByKind(t *testing.T) {
	a := cascade.New(cascade.KindConflict, "first message")
	b := cascade.New(cascade.KindConflict, "a completely different message")
	if !errors.Is(a, b) {
		t.Error("two errors of the same Kind should satisfy errors.Is regardless of message")
	}

	c := cascade.New(cascade.KindNotFound, "first message")
	if errors.Is(a, c) {
		t.Error("errors of different Kinds must not satisfy errors.Is")
	}
}

func TestErrorIs_SentinelMatching(t *testing.T) {
	err := cascade.Newf(cascade.KindNotFound, "widget %q not found", "gizmo")
	if !errors.Is(err, cascade.ErrNotFound) {
		t.Error("errors.Is(err, cascade.ErrNotFound) should be true for any KindNotFound error")
	}
	if errors.Is(err, cascade.ErrConflict) {
		t.Error("errors.Is(err, cascade.ErrConflict) should be false for a KindNotFound error")
	}

	// Every sentinel is Valid and self-consistent with its own Kind.
	for _, k := range cascade.AllKinds() {
		sentinel := sentinelFor(t, k)
		if !errors.Is(sentinel, sentinel) {
			t.Errorf("sentinel for %v does not satisfy errors.Is against itself", k)
		}
	}
}

// TestErrorIs_ThroughStdlibWrap proves an internal/ package's free-form
// wrapping (fmt.Errorf("...: %w", err)) does not erase the Kind — the whole
// point of task 3's inspection helpers.
func TestErrorIs_ThroughStdlibWrap(t *testing.T) {
	inner := cascade.New(cascade.KindQuotaExhausted, "lane exhausted")
	outer := fmt.Errorf("dispatch failed: %w", inner)

	if !errors.Is(outer, cascade.ErrQuotaExhausted) {
		t.Error("errors.Is should see through a stdlib %w wrap to the taxonomy Kind")
	}
	kind, ok := cascade.KindOf(outer)
	if !ok || kind != cascade.KindQuotaExhausted {
		t.Errorf("KindOf(outer) = (%v, %v), want (KindQuotaExhausted, true)", kind, ok)
	}
}

func TestKindOf_ErrorPaths(t *testing.T) {
	if kind, ok := cascade.KindOf(nil); ok || kind != 0 {
		t.Errorf("KindOf(nil) = (%v, %v), want (0, false)", kind, ok)
	}

	plain := errors.New("not a taxonomy error")
	if kind, ok := cascade.KindOf(plain); ok || kind != 0 {
		t.Errorf("KindOf(plain stdlib error) = (%v, %v), want (0, false)", kind, ok)
	}

	if cascade.HasKind(plain, cascade.KindInternal) {
		t.Error("HasKind must be false for a non-taxonomy error, even for KindInternal")
	}

	taxErr := cascade.New(cascade.KindPermissionDenied, "no")
	if !cascade.HasKind(taxErr, cascade.KindPermissionDenied) {
		t.Error("HasKind should be true when the kind matches")
	}
	if cascade.HasKind(taxErr, cascade.KindPolicyDenied) {
		t.Error("HasKind should be false when the kind does not match")
	}
}

func TestUnwrap(t *testing.T) {
	cause := errors.New("root cause")
	e := cascade.Wrap(cascade.KindInternal, cause, "outer")
	if got := errors.Unwrap(error(e)); got != cause {
		t.Errorf("errors.Unwrap = %v, want %v", got, cause)
	}

	noWrap := cascade.New(cascade.KindInternal, "outer only")
	if got := errors.Unwrap(error(noWrap)); got != nil {
		t.Errorf("errors.Unwrap on a non-wrapping error = %v, want nil", got)
	}
}

// sentinelFor returns this package's sentinel error for k, failing the test
// if k is not one of the 14 frozen kinds (keeps this helper honest as the
// enumeration is extended only via a T0 amendment).
func sentinelFor(t *testing.T, k cascade.Kind) error {
	t.Helper()
	switch k {
	case cascade.KindNotFound:
		return cascade.ErrNotFound
	case cascade.KindInvalidInput:
		return cascade.ErrInvalidInput
	case cascade.KindConflict:
		return cascade.ErrConflict
	case cascade.KindUnavailable:
		return cascade.ErrUnavailable
	case cascade.KindTimeout:
		return cascade.ErrTimeout
	case cascade.KindCanceled:
		return cascade.ErrCanceled
	case cascade.KindPermissionDenied:
		return cascade.ErrPermissionDenied
	case cascade.KindElevationRequired:
		return cascade.ErrElevationRequired
	case cascade.KindPolicyDenied:
		return cascade.ErrPolicyDenied
	case cascade.KindCapabilityDenied:
		return cascade.ErrCapabilityDenied
	case cascade.KindQuotaExhausted:
		return cascade.ErrQuotaExhausted
	case cascade.KindUnsupported:
		return cascade.ErrUnsupported
	case cascade.KindIntegrity:
		return cascade.ErrIntegrity
	case cascade.KindInternal:
		return cascade.ErrInternal
	default:
		t.Fatalf("sentinelFor: unhandled kind %v — AllKinds() and this switch have drifted apart", k)
		return nil
	}
}

// ExampleNew is a runnable godoc example for New, the taxonomy's primary
// entry-point constructor (Art.10.6).
func ExampleNew() {
	err := cascade.New(cascade.KindNotFound, `provider "openai" not configured`)
	fmt.Println(err)
	// Output: not-found: provider "openai" not configured
}

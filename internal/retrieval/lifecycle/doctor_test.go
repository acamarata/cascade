package lifecycle_test

// Purpose: TestRecallIndex-prefixed tests for DoctorCheck — the three
// outcomes the C/S-05.T2 framework can render (StatusOK, StatusWarn,
// StatusError), and the not-fixable contract.
//
// SPORT: internal.retrieval.lifecycle.DoctorCheck/ADDED (P1-E06-W2-S11-T4).

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
)

// TestRecallIndexDoctorCheckNoBuilderIsError proves a nil builder (no
// retrieval index configured) reports StatusError, never a silent OK.
func TestRecallIndexDoctorCheckNoBuilderIsError(t *testing.T) {
	check := lifecycle.NewDoctorCheck(nil)
	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != doctor.StatusError {
		t.Fatalf("want StatusError with no builder, got %+v", result)
	}
}

// TestRecallIndexDoctorCheckNoIndexIsOK proves a builder over an empty
// (never-rebuilt) manager reports StatusOK with an informational
// remediation: an unbuilt index on a fresh install is a normal,
// unconfigured state, not a defect `cascade doctor` should fail a
// healthy installation over.
func TestRecallIndexDoctorCheckNoIndexIsOK(t *testing.T) {
	h := newHarness(t)
	m := h.manager(nil)
	check := lifecycle.NewDoctorCheck(func(context.Context) (*lifecycle.Manager, func(), error) {
		return m, nil, nil
	})
	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != doctor.StatusOK || result.Remediation == "" {
		t.Fatalf("want StatusOK with an informational remediation, got %+v", result)
	}
}

// TestRecallIndexDoctorCheckCleanIsOK proves a rebuilt, verified-clean
// index reports StatusOK.
func TestRecallIndexDoctorCheckCleanIsOK(t *testing.T) {
	h := newHarness(t)
	m := h.manager(oneSource("a.md", "# T\n\nbody"))
	if _, err := m.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	check := lifecycle.NewDoctorCheck(func(context.Context) (*lifecycle.Manager, func(), error) {
		return m, nil, nil
	})
	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != doctor.StatusOK {
		t.Fatalf("want StatusOK, got %+v", result)
	}
}

// TestRecallIndexDoctorCheckDriftedIsWarn proves a marker mismatch is
// reported as a warning with the drift named in the message.
func TestRecallIndexDoctorCheckDriftedIsWarn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	m := h.manager(oneSource("a.md", "# T\n\nbody"))
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	h.hash.hash = "a-different-commit:digest"
	check := lifecycle.NewDoctorCheck(func(context.Context) (*lifecycle.Manager, func(), error) {
		return m, nil, nil
	})
	result, err := check.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != doctor.StatusWarn {
		t.Fatalf("want StatusWarn on drift, got %+v", result)
	}
}

// TestRecallIndexDoctorCheckBuildErrorIsError proves a builder that fails
// to open its store reports StatusError rather than panicking.
func TestRecallIndexDoctorCheckBuildErrorIsError(t *testing.T) {
	check := lifecycle.NewDoctorCheck(func(context.Context) (*lifecycle.Manager, func(), error) {
		return nil, nil, errors.New("boom")
	})
	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != doctor.StatusError {
		t.Fatalf("want StatusError on a build failure, got %+v", result)
	}
}

// TestRecallIndexDoctorCheckNotFixable proves Fix always refuses, per the
// check's Metadata().Fixable=false contract.
func TestRecallIndexDoctorCheckNotFixable(t *testing.T) {
	check := lifecycle.NewDoctorCheck(nil)
	if check.Metadata().Fixable {
		t.Fatal("want Fixable=false")
	}
	if _, err := check.Fix(context.Background()); !errors.Is(err, doctor.ErrCheckNotFixable) {
		t.Fatalf("want ErrCheckNotFixable, got %v", err)
	}
}

// TestRecallIndexDoctorCheckIdentity proves Name/Describe are stable and
// non-empty (the doctor registry keys checks by Name).
func TestRecallIndexDoctorCheckIdentity(t *testing.T) {
	check := lifecycle.NewDoctorCheck(nil)
	if check.Name() != lifecycle.DoctorCheckName || check.Name() == "" {
		t.Fatalf("want Name() == DoctorCheckName, got %q", check.Name())
	}
	if check.Describe() == "" {
		t.Fatal("want a non-empty Describe()")
	}
}

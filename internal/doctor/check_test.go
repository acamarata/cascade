package doctor

import (
	"context"
	"errors"
	"testing"
)

// fakeCheck is the shared minimal Check implementation used across this
// package's test files.
type fakeCheck struct {
	name     string
	meta     CheckMeta
	runFn    func(ctx context.Context) (CheckResult, error)
	fixFn    func(ctx context.Context) (FixResult, error)
	describe string
}

func (f *fakeCheck) Name() string        { return f.name }
func (f *fakeCheck) Describe() string    { return f.describe }
func (f *fakeCheck) Metadata() CheckMeta { return f.meta }

func (f *fakeCheck) Run(ctx context.Context) (CheckResult, error) {
	if f.runFn != nil {
		return f.runFn(ctx)
	}
	return CheckResult{Status: StatusOK}, nil
}

func (f *fakeCheck) Fix(ctx context.Context) (FixResult, error) {
	if f.fixFn != nil {
		return f.fixFn(ctx)
	}
	if !f.meta.Fixable {
		return FixResult{}, ErrCheckNotFixable
	}
	return FixResult{}, nil
}

func TestCheckResultAndFixResultAreValueTypes(t *testing.T) {
	// CheckResult/FixResult must be usable as plain value types (no
	// pointer receiver requirements, safe to copy) — the ticket
	// contract's explicit constraint.
	a := CheckResult{Status: StatusWarn, Message: "m"}
	b := a
	b.Status = StatusError
	if a.Status != StatusWarn {
		t.Fatalf("mutating the copy mutated the original: %+v", a)
	}

	fa := FixResult{Applied: true, Delta: "d"}
	fb := fa
	fb.Applied = false
	if !fa.Applied {
		t.Fatalf("mutating the copy mutated the original: %+v", fa)
	}
}

func TestErrCheckNotFixable_NonFixableCheckReturnsSentinel(t *testing.T) {
	c := &fakeCheck{name: "not-fixable", meta: CheckMeta{Fixable: false}}
	_, err := c.Fix(context.Background())
	if !errors.Is(err, ErrCheckNotFixable) {
		t.Fatalf("got err=%v, want ErrCheckNotFixable", err)
	}
}

func TestStatusValues(t *testing.T) {
	if StatusOK == StatusWarn || StatusWarn == StatusError || StatusOK == StatusError {
		t.Fatalf("Status consts must be pairwise distinct")
	}
}

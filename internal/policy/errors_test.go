package policy

import (
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestClassifyErrorsCarryPolicyDeniedKind(t *testing.T) {
	// The taxonomy is frozen at fourteen kinds (R-14.3), so neither
	// refusal can be a kind of its own. R-14.152 settled that case: the
	// refusal presents as policy-denied and keeps its identifier as a
	// stable string.
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"parse", newParseError(errors.New("boom")), CodeClassifyParseError},
		{"unknown", newUnknownError("because"), CodeClassifyUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, ok := cascade.KindOf(tc.err)
			if !ok || kind != cascade.KindPolicyDenied {
				t.Fatalf("KindOf(%v) = %v, %v; want KindPolicyDenied, true", tc.err, kind, ok)
			}
			if !errors.Is(tc.err, cascade.ErrPolicyDenied) {
				t.Error("a classifier refusal must match cascade.ErrPolicyDenied")
			}
			if !strings.Contains(tc.err.Error(), tc.code) {
				t.Errorf("message %q does not carry the stable code %q", tc.err.Error(), tc.code)
			}
		})
	}
}

func TestClassifyErrorsAreDistinguishable(t *testing.T) {
	parse := newParseError(errors.New("unterminated quote"))
	unknown := newUnknownError("no table row")

	if !errors.Is(parse, ErrClassifyParseError) {
		t.Error("a parse refusal must match ErrClassifyParseError")
	}
	if errors.Is(parse, ErrClassifyUnknown) {
		t.Error("a parse refusal must NOT match ErrClassifyUnknown; the two refusals mean different things to a user")
	}
	if !errors.Is(unknown, ErrClassifyUnknown) {
		t.Error("an unknown-form refusal must match ErrClassifyUnknown")
	}
	if errors.Is(unknown, ErrClassifyParseError) {
		t.Error("an unknown-form refusal must NOT match ErrClassifyParseError")
	}
	if errors.Is(parse, errors.New("something else")) {
		t.Error("a refusal must not match an unrelated error")
	}
}

func TestParseErrorWrapsItsCause(t *testing.T) {
	cause := errors.New("reached EOF without closing quote")
	err := newParseError(cause)
	if !errors.Is(err, cause) {
		t.Fatal("the parse refusal must keep its cause reachable, so a user can be shown where the command broke")
	}
	if err.Code != CodeClassifyParseError {
		t.Fatalf("Code = %q, want %q", err.Code, CodeClassifyParseError)
	}
}

func TestUnknownErrorNamesTheReasonAndTheRung(t *testing.T) {
	err := newUnknownError("the command \"frobnicate\" is not in the risk table")
	msg := err.Error()
	for _, want := range []string{"frobnicate", "L4", "deny", CodeClassifyUnknown} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not mention %q", msg, want)
		}
	}
}

func TestClassifyErrorNilSafety(t *testing.T) {
	var nilErr *ClassifyError
	if got := nilErr.Error(); got != CodeClassifyUnknown {
		t.Errorf("nil ClassifyError renders as %q, want %q", got, CodeClassifyUnknown)
	}
	if nilErr.Unwrap() != nil {
		t.Error("nil ClassifyError must unwrap to nil")
	}
	if nilErr.Is(ErrClassifyUnknown) {
		t.Error("a nil refusal must not match a real one")
	}
	empty := &ClassifyError{Code: CodeClassifyUnknown}
	if got := empty.Error(); got != CodeClassifyUnknown {
		t.Errorf("ClassifyError with no cause renders as %q, want %q", got, CodeClassifyUnknown)
	}
}

func TestSentinelsAreConstructedFromTheStableCodes(t *testing.T) {
	if ErrClassifyParseError.Code != CodeClassifyParseError {
		t.Error("ErrClassifyParseError carries the wrong code")
	}
	if ErrClassifyUnknown.Code != CodeClassifyUnknown {
		t.Error("ErrClassifyUnknown carries the wrong code")
	}
	if CodeClassifyParseError == CodeClassifyUnknown {
		t.Fatal("the two stable codes must differ")
	}
}

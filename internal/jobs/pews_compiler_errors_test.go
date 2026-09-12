package jobs

import (
	"errors"
	"strings"
	"testing"
)

// TestErrEmptyTicketID_IsSentinel asserts ErrEmptyTicketID is a stable
// sentinel errors.Is recognizes.
func TestErrEmptyTicketID_IsSentinel(t *testing.T) {
	wrapped := errors.Join(ErrEmptyTicketID)
	if !errors.Is(wrapped, ErrEmptyTicketID) {
		t.Error("errors.Is(wrapped, ErrEmptyTicketID) = false, want true")
	}
}

// TestErrUnclassifiedFootprint_IsSentinel mirrors the above for the
// gate-set refusal sentinel.
func TestErrUnclassifiedFootprint_IsSentinel(t *testing.T) {
	if !errors.Is(ErrUnclassifiedFootprint, ErrUnclassifiedFootprint) {
		t.Error("errors.Is(ErrUnclassifiedFootprint, ErrUnclassifiedFootprint) = false, want true")
	}
}

// TestErrUnknownModelClass_Error asserts the message names the
// offending value.
func TestErrUnknownModelClass_Error(t *testing.T) {
	err := &ErrUnknownModelClass{ModelClass: "bogus"}
	msg := err.Error()
	if !strings.Contains(msg, "bogus") {
		t.Errorf("ErrUnknownModelClass.Error() = %q, want it to name %q", msg, "bogus")
	}
}

// TestErrUnknownReviewLevel_Error covers both the cr_level and
// qa_level message branches.
func TestErrUnknownReviewLevel_Error(t *testing.T) {
	cr := &ErrUnknownReviewLevel{Field: crLevelField, Value: "CR-Z"}
	if msg := cr.Error(); !strings.Contains(msg, "cr_level") || !strings.Contains(msg, "CR-Z") {
		t.Errorf("cr_level Error() = %q, want it to name cr_level and CR-Z", msg)
	}
	qa := &ErrUnknownReviewLevel{Field: qaLevelField, Value: "QA-Z"}
	if msg := qa.Error(); !strings.Contains(msg, "qa_level") || !strings.Contains(msg, "QA-Z") {
		t.Errorf("qa_level Error() = %q, want it to name qa_level and QA-Z", msg)
	}
}

// TestErrMissingContractField_Error asserts the message names the
// missing field.
func TestErrMissingContractField_Error(t *testing.T) {
	err := &ErrMissingContractField{Field: "title"}
	if msg := err.Error(); !strings.Contains(msg, "title") {
		t.Errorf("ErrMissingContractField.Error() = %q, want it to name %q", msg, "title")
	}
}

// TestErrUnparseableContractField_Error asserts the message names the
// field, its value and the expected form.
func TestErrUnparseableContractField_Error(t *testing.T) {
	err := &ErrUnparseableContractField{Field: "weight", Value: "XXL", Reason: "one of XS, S, M, L, XL"}
	msg := err.Error()
	for _, want := range []string{"weight", "XXL", "one of XS, S, M, L, XL"} {
		if !strings.Contains(msg, want) {
			t.Errorf("ErrUnparseableContractField.Error() = %q, want it to contain %q", msg, want)
		}
	}
}

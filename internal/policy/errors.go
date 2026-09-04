// Package policy (errors.go): Purpose: the two named refusals the command
//
//	classifier returns, carried as taxonomy errors.
//
// Inputs: an underlying parse error, or a reason string naming the AST form
//
//	that could not be classified.
//
// Outputs: ErrClassifyParseError and ErrClassifyUnknown as comparison
//
//	targets for errors.Is, and *ClassifyError values carrying a detail
//	message.
//
// Constraints: the pkg/cascade taxonomy is FROZEN at fourteen kinds
//
//	(R-14.3), so "classify-parse-error" and "classify-unknown" cannot be
//	kinds. R-14.152 settled exactly this case for a different contract:
//	the refusal maps to KindPolicyDenied and the contract's identifier
//	survives as a STABLE STRING on the error. That is what Code holds.
//	Both refusals mean the same thing to a caller (the command is L4)
//	but they are distinguishable so a user can be told whether their
//	command was malformed or merely unrecognised.
//
// SPORT: internal/policy ClassifyError/ADDED (P1-E09-W2-S17-T3).
package policy

import (
	"github.com/acamarata/cascade/pkg/cascade"
)

// The stable identifier strings, per R-14.152. They appear in error
// messages and audit rows and must not change once shipped.
const (
	// CodeClassifyParseError marks a command the shell grammar could not
	// parse at all.
	CodeClassifyParseError = "classify-parse-error"
	// CodeClassifyUnknown marks a command that parsed but whose form the
	// §5.15 table does not cover, including every Windows-native form
	// (R-14.28).
	CodeClassifyUnknown = "classify-unknown"
)

// ClassifyError is a classifier refusal. Code is the stable identifier;
// the wrapped taxonomy error carries KindPolicyDenied so the rest of the
// system treats it like any other policy refusal.
type ClassifyError struct {
	// Code is CodeClassifyParseError or CodeClassifyUnknown.
	Code string
	// Cause is the taxonomy error this refusal presents as.
	Cause *cascade.Error
}

// Error renders the refusal, leading with the stable code.
func (e *ClassifyError) Error() string {
	if e == nil || e.Cause == nil {
		return CodeClassifyUnknown
	}
	return e.Cause.Error()
}

// Unwrap exposes the taxonomy error, so cascade.KindOf and
// errors.Is(err, cascade.ErrPolicyDenied) both work on a refusal.
func (e *ClassifyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Is matches on Code alone, so errors.Is(err, ErrClassifyUnknown) is true
// for every unknown-form refusal regardless of its detail message. The
// taxonomy error's own Is compares only Kind, which cannot tell the two
// refusals apart; this is what does.
func (e *ClassifyError) Is(target error) bool {
	t, ok := target.(*ClassifyError)
	if !ok {
		return false
	}
	return e != nil && t != nil && e.Code == t.Code
}

// ErrClassifyParseError is the comparison target for a refusal caused by
// input the shell grammar rejected.
var ErrClassifyParseError = &ClassifyError{
	Code:  CodeClassifyParseError,
	Cause: cascade.New(cascade.KindPolicyDenied, CodeClassifyParseError),
}

// ErrClassifyUnknown is the comparison target for a refusal caused by a
// command form the §5.15 table does not cover.
var ErrClassifyUnknown = &ClassifyError{
	Code:  CodeClassifyUnknown,
	Cause: cascade.New(cascade.KindPolicyDenied, CodeClassifyUnknown),
}

// newParseError builds the refusal for input that would not parse. The
// underlying parse error is wrapped rather than summarised, so a caller
// can show the user where their command broke.
func newParseError(cause error) *ClassifyError {
	return &ClassifyError{
		// Taken from the sentinel rather than from the constant, so the
		// value a caller compares against and the value this builds can
		// never drift apart.
		Code: ErrClassifyParseError.Code,
		Cause: cascade.Wrapf(cascade.KindPolicyDenied, cause,
			"policy: %s: command refused at %s because it could not be parsed",
			CodeClassifyParseError, L4),
	}
}

// newUnknownError builds the refusal for a form the table does not cover.
// reason names the form, so the message says what was not understood
// rather than only that something was not.
func newUnknownError(reason string) *ClassifyError {
	return &ClassifyError{
		Code: ErrClassifyUnknown.Code,
		Cause: cascade.Newf(cascade.KindPolicyDenied,
			"policy: %s: command refused at %s (%s): %s",
			CodeClassifyUnknown, L4, L4.disposition(), reason),
	}
}

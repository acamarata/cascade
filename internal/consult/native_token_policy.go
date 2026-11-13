// Package consult provides consultation policy enforcement.
package consult

import "errors"

// NativeTokenViolation records a single native-token policy violation.
type NativeTokenViolation struct {
	// Code is the machine-readable violation code.
	Code string
	// Use is the native-token use that triggered the violation.
	Use string
}

// PolicyError represents a disallowed native-token use.
type PolicyError struct {
	// Code is the machine-readable error code.
	Code string
	// Use is the native-token use that was denied.
	Use string
}

// Error returns the human-readable policy error message.
func (e *PolicyError) Error() string {
	return "D4652: native token use not allowed: " + e.Use
}

// ErrNativeTokenUseNotAllowed is the sentinel error for disallowed native-token uses.
var ErrNativeTokenUseNotAllowed = errors.New("native token use not allowed")

// Unwrap returns the underlying sentinel error.
func (e *PolicyError) Unwrap() error {
	return ErrNativeTokenUseNotAllowed
}

// ValidateNativeTokenUse checks whether the given use string is permitted.
// If allowed, it returns the supplied violations slice unchanged and a nil error.
// If not allowed, it appends a new NativeTokenViolation to the slice and returns
// a *PolicyError wrapping ErrNativeTokenUseNotAllowed.
func ValidateNativeTokenUse(use string, violations []NativeTokenViolation) ([]NativeTokenViolation, error) {
	switch use {
	case "T0 coordination", "CR/QA", "hard-ticket escalation", "planning sign-off":
		return violations, nil
	default:
		v := append(violations, NativeTokenViolation{Code: "D4652", Use: use})
		return v, &PolicyError{Code: "D4652", Use: use}
	}
}

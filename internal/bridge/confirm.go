package bridge

import (
	"context"
	"errors"
	"time"
)

// ConfirmToken represents a single-use confirmation token.
//
// Fields:
//   - Token: The opaque token string used for redemption.
//   - Action: The action being confirmed. Must be one of: gpt.purge, gpt.delete, gpt.project.sweep.
//   - Subjects: The sorted list of subject identifiers this token applies to.
//   - ExpiresAt: The exact time after which this token is no longer valid.
type ConfirmToken struct {
	Token     string
	Action    string
	Subjects  []string
	ExpiresAt time.Time
}

// ErrTokenNotFound is returned when the requested token does not exist in the store.
var ErrTokenNotFound = errors.New("token not found")

// ErrTokenExpired is returned when the token has passed its ExpiresAt time.
var ErrTokenExpired = errors.New("token expired")

// ErrSubjectsMismatch is returned when the subjects provided to Redeem do not
// exactly match the sorted subjects bound to the token at issue time.
var ErrSubjectsMismatch = errors.New("subjects mismatch")

// ErrTokenAlreadyUsed is returned when the token has already been successfully redeemed.
var ErrTokenAlreadyUsed = errors.New("token already used")

// ConfirmStore defines the contract for issuing and redeeming confirmation tokens.
//
// Implementations must sort the Subjects slice before storage or comparison.
// Valid actions are strictly limited to: gpt.purge, gpt.delete, gpt.project.sweep.
type ConfirmStore interface {
	// Issue generates and persists a new ConfirmToken for the specified action and subjects.
	//
	// Parameters:
	//   - ctx: Context for cancellation and deadlines.
	//   - action: The action to confirm (e.g., gpt.purge).
	//   - subjects: The list of subjects; will be sorted by the implementation.
	//
	// Returns:
	//   - ConfirmToken: The generated token including its expiration time.
	//   - error: Any persistence or validation error.
	Issue(ctx context.Context, action string, subjects []string) (ConfirmToken, error)

	// Redeem validates and consumes a token, ensuring action and subjects match.
	//
	// Parameters:
	//   - ctx: Context for cancellation and deadlines.
	//   - token: The raw token string to redeem.
	//   - action: The action being performed; must match the issued action.
	//   - subjects: The subjects being acted upon; must match the issued subjects.
	//
	// Returns:
	//   - error: ErrTokenNotFound, ErrTokenExpired, ErrSubjectsMismatch,
	//            ErrTokenAlreadyUsed, or other persistence errors.
	Redeem(ctx context.Context, token string, action string, subjects []string) error
}

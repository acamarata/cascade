package docket

import (
	"errors"
)

// ErrGreenSHAEmpty is returned when a green attempt is created with an empty commit SHA.
var ErrGreenSHAEmpty = errors.New("GREEN_SHA_EMPTY")

// GreenAttempt represents a record of a successful or green attempt.
type GreenAttempt struct {
	// CommitSHA is the exact commit SHA associated with the attempt.
	CommitSHA string `json:"commit_sha"`
	// Cost is the nullable cost associated with the attempt.
	Cost *float64 `json:"cost"`
}

// NewGreenAttempt creates a new GreenAttempt. It returns ErrGreenSHAEmpty if sha is empty.
func NewGreenAttempt(sha string, cost *float64) (GreenAttempt, error) {
	if sha == "" {
		return GreenAttempt{}, ErrGreenSHAEmpty
	}
	return GreenAttempt{
		CommitSHA: sha,
		Cost:      cost,
	}, nil
}

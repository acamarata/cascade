// Package conductor: merge lease types and contract interface.
// SPORT: conductor.merge-lease/ADD (P2-GND-12).

package conductor

import (
	"context"
	"errors"
	"time"
)

// MergeLease represents an active lease on a merge branch for a specific repository.
type MergeLease struct {
	// Repo is the identifier of the repository being leased.
	Repo string
	// Branch is the branch within the repository that is locked.
	Branch string
	// HolderID is the unique identifier of the current lease holder.
	HolderID string
	// ExpiresAt is the time at which this lease automatically expires.
	ExpiresAt time.Time
}

// ErrNotOwner is returned when an operation attempts to release or modify
// a lease that is currently held by a different holder.
var ErrNotOwner = errors.New("release by non-owner")

// LeaseStore defines the contract for managing merge leases.
// Implementations are responsible for the underlying persistence and
// concurrency control of the leases.
type LeaseStore interface {
	// Acquire attempts to obtain a new lease for the specified repository
	// and branch on behalf of the given holder, valid for the specified TTL.
	Acquire(ctx context.Context, repo, branch, holderID string, ttl time.Duration) (*MergeLease, error)

	// Release voluntarily gives up the lease held by holderID for the
	// specified repository and branch. It returns ErrNotOwner if the
	// caller is not the current holder.
	Release(ctx context.Context, repo, branch, holderID string) error

	// Takeover attempts to seize an existing lease for the specified
	// repository and branch on behalf of the given holder, valid for the
	// specified TTL. This is typically used when a previous holder has
	// crashed or failed to release the lease.
	Takeover(ctx context.Context, repo, branch, holderID string, ttl time.Duration) (*MergeLease, error)
}

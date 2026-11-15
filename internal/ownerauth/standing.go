package ownerauth

import (
	"errors"
	"time"
)

// DefaultDuration is the default validity period for a standing grant.
const DefaultDuration = 7 * 24 * time.Hour

// RequestKind represents the type of operation request being made.
type RequestKind int

const (
	// KindLane represents a lane operation request.
	KindLane RequestKind = iota
	// KindGrant represents a grant operation request.
	KindGrant
	// KindJobs represents a jobs operation request.
	KindJobs
)

// ErrNotOwnerSession is returned when the session is not an owner session.
var ErrNotOwnerSession = errors.New("not an owner session")

// ErrGrantRevoked is returned when the standing grant has been revoked.
var ErrGrantRevoked = errors.New("grant revoked")

// ErrGrantExpired is returned when the standing grant has expired.
var ErrGrantExpired = errors.New("grant expired")

// StandingGrant represents a standing authorization granted to an owner.
type StandingGrant struct {
	// OwnerID is the identifier of the owner who holds the grant.
	OwnerID string
	// LaneOps contains the allowed lane operations.
	LaneOps []string
	// GrantOps contains the allowed grant operations.
	GrantOps []string
	// JobsOps contains the allowed jobs operations.
	JobsOps []string
	// ExpiresAt is the time when the grant expires.
	ExpiresAt time.Time
	// Revoked indicates whether the grant has been revoked.
	Revoked bool
}

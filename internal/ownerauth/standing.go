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

// Session represents an authentication session.
type Session struct {
	// IsOwner indicates whether this session belongs to an owner.
	IsOwner bool
	// OwnerID is the identifier of the owner if this is an owner session.
	OwnerID string
}

// CreateGrant creates a new standing grant for an owner session.
// It verifies that the provided session belongs to an owner; if not, it returns
// nil and ErrNotOwnerSession. On success, it returns a StandingGrant populated
// with the given op lists, OwnerID from the session, ExpiresAt set to
// clock().Add(DefaultDuration), and Revoked set to false.
func CreateGrant(ownerSession Session, laneOps, grantOps, jobsOps []string, clock func() time.Time) (*StandingGrant, error) {
	if !ownerSession.IsOwner {
		return nil, ErrNotOwnerSession
	}

	return &StandingGrant{
		OwnerID:   ownerSession.OwnerID,
		LaneOps:   append([]string(nil), laneOps...),
		GrantOps:  append([]string(nil), grantOps...),
		JobsOps:   append([]string(nil), jobsOps...),
		ExpiresAt: clock().Add(DefaultDuration),
		Revoked:   false,
	}, nil
}

// Validate checks if the standing grant is valid for the given request kind at the specified time.
// It returns ErrGrantRevoked if the grant has been revoked, and ErrGrantExpired if the grant has expired.
// If the grant is valid, it confirms the requested kind is covered by the grant operations.
func (g *StandingGrant) Validate(kind RequestKind, now time.Time) error {
	if g.Revoked {
		return ErrGrantRevoked
	}
	if now.After(g.ExpiresAt) {
		return ErrGrantExpired
	}

	switch kind {
	case KindLane:
		if len(g.LaneOps) == 0 {
			return errors.New("lane operations not covered")
		}
	case KindGrant:
		if len(g.GrantOps) == 0 {
			return errors.New("grant operations not covered")
		}
	case KindJobs:
		if len(g.JobsOps) == 0 {
			return errors.New("jobs operations not covered")
		}
	default:
		return errors.New("unknown request kind")
	}

	return nil
}

// Revoke immediately revokes the standing grant, preventing any further renewals.
func (g *StandingGrant) Revoke() {
	g.Revoked = true
}

// Renew extends the standing grant's expiration time by the given duration.
// It returns ErrGrantRevoked if the grant has already been revoked.
func (g *StandingGrant) Renew(duration time.Duration) error {
	if g.Revoked {
		return ErrGrantRevoked
	}
	g.ExpiresAt = g.ExpiresAt.Add(duration)
	return nil
}

package broker

import "errors"

// ErrNotImplemented is returned when a routing operation is not yet implemented.
var ErrNotImplemented = errors.New("broker: operation unavailable")

// Tier represents a routing tier level for system models.
type Tier int

const (
	// TierSys0 represents the Sys0 routing tier.
	TierSys0 Tier = iota
	// TierSys1 represents the Sys1 routing tier.
	TierSys1
	// TierSys2GF represents the Sys2 GF routing tier.
	TierSys2GF
	// TierSys2GC represents the Sys2 GC routing tier.
	TierSys2GC
	// TierSys2GP represents the Sys2 GP routing tier.
	TierSys2GP
	// TierSys2CO represents the Sys2 CO routing tier.
	TierSys2CO
	// TierSys2A4 represents the Sys2 A4 routing tier.
	TierSys2A4
	// TierSys2A3 represents the Sys2 A3 routing tier.
	TierSys2A3
	// TierSys2A2 represents the Sys2 A2 routing tier.
	TierSys2A2
	// TierSys2A1 represents the Sys2 A1 routing tier.
	TierSys2A1
)

// RouteRequest represents a routing request containing the input string
// and a flag indicating whether the input is code.
type RouteRequest struct {
	// Input is the user input text or code to be routed.
	Input string
	// IsCode indicates whether the input is code.
	IsCode bool
}

// RouteResult represents the result of a routing request, including the
// determined tier and whether the request was refused.
type RouteResult struct {
	// Tier is the selected routing tier.
	Tier Tier
	// Refused indicates whether the request was refused.
	Refused bool
}

// QuotaChecker defines an interface for checking user quotas against a tier.
type QuotaChecker interface {
	// CheckQuota checks if the quota allows the given tier.
	CheckQuota(tier Tier) (bool, error)
}

// RPMChecker defines an interface for checking rate limits (requests per minute)
// against a specific key.
type RPMChecker interface {
	// CheckRPM checks if the RPM limit for the given key has not been exceeded.
	CheckRPM(key string) (bool, error)
}

// Route routes a request to an appropriate system tier based on quotas and rate limits.
// Currently, it is a stub that always returns ErrNotImplemented.
func Route(req RouteRequest, qc QuotaChecker, rc RPMChecker) (RouteResult, error) {
	return RouteResult{}, ErrNotImplemented
}

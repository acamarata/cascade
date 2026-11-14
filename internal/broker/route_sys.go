// Package broker routes work to a system tier by quota and rate headroom.
package broker

// Tier represents a routing tier level for system models.
type Tier int

const (
	// TierSys0 represents the Sys0 routing tier.
	TierSys0 Tier = iota
	// TierSys1 represents the Sys1 routing tier.
	TierSys1
	// TierSys2Rung1 is rung 1 of the Sys2 ladder, in the order it is tried.
	TierSys2Rung1
	// TierSys2Rung2 is rung 2 of the Sys2 ladder, in the order it is tried.
	TierSys2Rung2
	// TierSys2Rung3 is rung 3 of the Sys2 ladder, in the order it is tried.
	TierSys2Rung3
	// TierSys2Rung4 is rung 4 of the Sys2 ladder, in the order it is tried.
	TierSys2Rung4
	// TierSys2Rung5 is rung 5 of the Sys2 ladder, in the order it is tried.
	TierSys2Rung5
	// TierSys2Rung6 is rung 6 of the Sys2 ladder, in the order it is tried.
	TierSys2Rung6
	// TierSys2Rung7 is rung 7 of the Sys2 ladder, in the order it is tried.
	TierSys2Rung7
	// TierSys2Rung8 is rung 8 of the Sys2 ladder, in the order it is tried.
	TierSys2Rung8
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

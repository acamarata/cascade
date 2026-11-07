package rpc

import (
	"context"
	"time"
)

// FleetTopologyEnvelopeVersion is the version string of the fleet topology result envelope.
const FleetTopologyEnvelopeVersion = "1"

// LaneDTO is the credential-free wire view of one fleet lane.
type LaneDTO struct {
	ID               string    `json:"id"`
	RuntimeProfile   string    `json:"runtime_profile"`
	QuotaDomain      string    `json:"quota_domain"`
	ModelID          string    `json:"model_id"`
	Effort           string    `json:"effort"`
	LaneClass        string    `json:"lane_class"`
	InteractionClass string    `json:"interaction_class"`
	Health           string    `json:"health"`
	HealthReason     string    `json:"health_reason"`
	BaseShadowPrice  float64   `json:"base_shadow_price"`
	Ceiling          string    `json:"ceiling"`
	CeilingReason    string    `json:"ceiling_reason"`
	Retired          bool      `json:"retired"`
	DiscoveredAt     time.Time `json:"discovered_at"`
}

// BucketDTO is the credential-free wire view of one quota bucket.
type BucketDTO struct {
	Name              string    `json:"name"`
	Window            string    `json:"window"`
	Source            string    `json:"source"`
	Limit             int64     `json:"limit"`
	RemainingFraction float64   `json:"remaining_fraction"`
	Confidence        float64   `json:"confidence"`
	ResetAt           time.Time `json:"reset_at"`
	ObservedAt        time.Time `json:"observed_at"`
}

// DomainDTO is the credential-free wire view of one quota domain; Holders is the number of credentials on it.
type DomainDTO struct {
	ID          string      `json:"id"`
	Kind        string      `json:"kind"`
	BillingTier string      `json:"billing_tier"`
	Quarantined bool        `json:"quarantined"`
	Holders     int         `json:"holders"`
	Buckets     []BucketDTO `json:"buckets"`
}

// ProfileDTO is the credential-free wire view of one runtime profile.
type ProfileDTO struct {
	ID         string `json:"id"`
	Runtime    string `json:"runtime"`
	Persistent bool   `json:"persistent"`
	Local      bool   `json:"local"`
}

// LaneExplainDTO is the wire view of a lane with its profile and quota domain.
type LaneExplainDTO struct {
	Lane    LaneDTO    `json:"lane"`
	Profile ProfileDTO `json:"profile"`
	Domain  DomainDTO  `json:"domain"`
}

// FleetTopologyDeps are the injected lookups and detector behind the five fleet topology methods; all five are required.
type FleetTopologyDeps struct {
	Lanes   func(ctx context.Context, includeRetired bool) ([]LaneDTO, error)
	Lane    func(ctx context.Context, id string) (LaneExplainDTO, error)
	Domains func(ctx context.Context) ([]DomainDTO, error)
	Domain  func(ctx context.Context, id string) (DomainDTO, error)
	Scan    func(payload []byte) error
}

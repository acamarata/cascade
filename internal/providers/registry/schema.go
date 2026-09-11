// Purpose: the provider registry's data shapes (P1-E10-W3-S20-T2): the
//
//	closed DriverKind/AuthType/AccountKind/Tier/HealthStatus/CapacityBucket/
//	LaneState enumerations, the VaultKeyRef type that can never carry a
//	credential value, ProviderRecord, LaneRecord, and CostRecord.
//
// Inputs: none at this layer -- these are pure data shapes and validators.
// Outputs: none.
// Constraints: AuthRef is a VaultKeyRef (a name), never a secret value -- no
//
//	field anywhere in ProviderRecord can hold a raw credential (mirrors
//	internal/providers/intake.VaultKeyRef, declared independently here so
//	this package never imports intake -- see migration.go's CONTRACT
//	DEVIATION note for the matching domain-registration deviation). Every
//	enum's zero value is either the honest "unknown" member or outright
//	invalid -- never a permissive default. LaneState has NO valid zero
//	value: an unresolvable state must be written as LaneStateUnknown
//	explicitly, never left empty and read as available.
//
// SPORT: provider.registry/ADD (P1-E10-W3-S20-T2).

package registry

import (
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// DriverKind names the driver family a provider record resolved to.
// Mirrors intake.DriverKind's five members (08-INIT-CONFIG-SPEC.md §2).
type DriverKind string

// The five closed DriverKind members.
const (
	DriverAnthropic    DriverKind = "anthropic"
	DriverOpenAICompat DriverKind = "openai-compat"
	DriverGemini       DriverKind = "gemini"
	DriverOllama       DriverKind = "ollama"
	DriverLocalLLM     DriverKind = "localllm"
)

// Valid reports whether k is one of the five declared members.
func (k DriverKind) Valid() bool {
	switch k {
	case DriverAnthropic, DriverOpenAICompat, DriverGemini, DriverOllama, DriverLocalLLM:
		return true
	}
	return false
}

// AuthType names how a provider record's credential is held.
type AuthType string

// The two closed AuthType members.
const (
	AuthKey   AuthType = "key"
	AuthOAuth AuthType = "oauth"
)

// Valid reports whether a is AuthKey or AuthOAuth.
func (a AuthType) Valid() bool { return a == AuthKey || a == AuthOAuth }

// AccountKind is the R-14.34 provider-ownership classification.
type AccountKind string

// The three closed AccountKind members.
const (
	AccountPersonal AccountKind = "personal"
	AccountShared   AccountKind = "shared"
	AccountService  AccountKind = "service"
)

// Valid reports whether k is one of the three declared members.
func (k AccountKind) Valid() bool {
	switch k {
	case AccountPersonal, AccountShared, AccountService:
		return true
	}
	return false
}

// Tier is the 06-FORGE-SPEC.md §5 rule 16 lane-affinity vocabulary:
// advisory router input, never a vendor binding.
type Tier string

// The six closed Tier members, cheapest to strongest.
const (
	TierCheapest  Tier = "cheapest"
	TierFree      Tier = "free"
	TierCheap     Tier = "cheap"
	TierMid       Tier = "mid"
	TierStrong    Tier = "strong"
	TierStrongest Tier = "strongest"
)

// Valid reports whether t is one of the six declared members.
func (t Tier) Valid() bool {
	switch t {
	case TierCheapest, TierFree, TierCheap, TierMid, TierStrong, TierStrongest:
		return true
	}
	return false
}

// HealthStatus is the S-20.T3-managed provider health state. Unlike
// LaneState, HealthUnknown is legitimately the zero value -- the ticket's
// schema names this default explicitly ("health_status ... default
// unknown").
type HealthStatus string

// The four closed HealthStatus members. HealthUnknown is deliberately the
// zero value.
const (
	HealthUnknown  HealthStatus = ""
	HealthHealthy  HealthStatus = "healthy"
	HealthDegraded HealthStatus = "degraded"
	HealthDead     HealthStatus = "dead"
)

// Valid reports whether h is one of the four declared members (including
// the zero value HealthUnknown).
func (h HealthStatus) Valid() bool {
	switch h {
	case HealthUnknown, HealthHealthy, HealthDegraded, HealthDead:
		return true
	}
	return false
}

// CapacityBucket is the R-16.10 closed set a lane draws its quota from.
type CapacityBucket string

// The three closed CapacityBucket members.
const (
	CapacityInteractiveUsage CapacityBucket = "interactive_usage"
	CapacityAgentSDKCredit   CapacityBucket = "agent_sdk_credit"
	CapacityAPICredit        CapacityBucket = "api_credit"
)

// Valid reports whether c is one of the three declared members.
func (c CapacityBucket) Valid() bool {
	switch c {
	case CapacityInteractiveUsage, CapacityAgentSDKCredit, CapacityAPICredit:
		return true
	}
	return false
}

// LaneState is the R-16.10 closed lane-availability state. The zero value
// (empty string) is deliberately NOT a member -- Valid() rejects it, so an
// unresolvable state must be written as LaneStateUnknown explicitly. This
// is the "no permissive zero value" rule: a caller that forgets to set
// State never silently reads as available.
type LaneState string

// The five closed LaneState members.
const (
	LaneStateAvailable    LaneState = "available"
	LaneStateConstrained  LaneState = "constrained"
	LaneStateExhausted    LaneState = "exhausted"
	LaneStateAuthRequired LaneState = "auth-required"
	LaneStateUnknown      LaneState = "unknown"
)

// Valid reports whether s is one of the five declared members. The zero
// value is intentionally excluded.
func (s LaneState) Valid() bool {
	switch s {
	case LaneStateAvailable, LaneStateConstrained, LaneStateExhausted, LaneStateAuthRequired, LaneStateUnknown:
		return true
	}
	return false
}

// VaultKeyRef is a vault broker key NAME, never a credential value. A
// distinct type (not a plain string field) so a reviewer, and the
// compiler for any function taking a VaultKeyRef where the caller only
// has a string, sees the "name vs value" distinction at every call site.
type VaultKeyRef string

// String returns the ref's name. Safe to log: a VaultKeyRef never holds a
// credential value, only the vault broker's key name.
func (r VaultKeyRef) String() string { return string(r) }

// ModelCost is one model's rate-card entry: per-token input/output cost in
// micro-USD (R-14.104).
type ModelCost struct {
	InputMicroUSDPerToken  int64 `json:"input_micro_usd_per_token"`
	OutputMicroUSDPerToken int64 `json:"output_micro_usd_per_token"`
}

// CostRecord is the registry-owned rate-card snapshot ratified by R-14.104.
// A nil *CostRecord on ProviderRecord.Cost means "unknown / not fetched
// yet" -- callers must handle that nil gracefully rather than treating it
// as zero cost.
type CostRecord struct {
	Models        map[string]ModelCost `json:"models"`
	LastRefreshed time.Time            `json:"last_refreshed"`
}

// ParseCostRecord decodes data into a *CostRecord. Never panics on
// malformed input (FuzzCostRecord asserts this): a decode failure returns
// a cascade.KindIntegrity error, not a panic. Empty input returns (nil,
// nil) -- the "unknown" state, not malformed.
func ParseCostRecord(data []byte) (*CostRecord, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var rec CostRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "registry: decode cost record")
	}
	return &rec, nil
}

// EncodeCostRecord marshals rec for storage. A nil rec encodes to nil bytes
// (the nullable "unknown" column state).
func EncodeCostRecord(rec *CostRecord) ([]byte, error) {
	if rec == nil {
		return nil, nil
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "registry: encode cost record")
	}
	return b, nil
}

// ProviderRecord is one row of the providers registry: the durable,
// authoritative record S-20.T1 intake upserts and K/S-22 routing, S-20.T3
// health/eviction, and S-20.T4 usage accounting all consume.
type ProviderRecord struct {
	Name string
	// Driver is the resolved driver kind.
	Driver DriverKind
	// BaseURL is the resolved API root; empty means the driver default.
	BaseURL string
	// Auth is the resolved auth type.
	Auth AuthType
	// AuthRef is the vault-key reference the credential lives under.
	// NEVER a credential value -- see VaultKeyRef's own doc comment. No
	// field on this struct (or anywhere reachable from it) can hold a raw
	// secret: Validate has nothing to check on this point because the
	// type system already makes the violation unrepresentable.
	AuthRef VaultKeyRef
	// KnownModels is the model-enumeration result, updated on re-verify.
	KnownModels []string
	// AccountKind classifies provider ownership (R-14.34).
	AccountKind AccountKind
	// Tier is the advisory lane-affinity vocabulary member.
	Tier Tier
	// Capabilities is the R-14.88 tri-state probe result.
	Capabilities provider.Capabilities
	// CapabilitiesProbedAt is when Capabilities was last refreshed.
	CapabilitiesProbedAt time.Time
	// Cost is the R-14.104 rate-card snapshot. nil = unknown.
	Cost *CostRecord
	// HealthStatus is the S-20.T3-managed health state.
	HealthStatus HealthStatus
	// HealthCheckedAt is when HealthStatus was last set.
	HealthCheckedAt time.Time
	// DemotionCount is incremented by S-20.T3 on 429/dead-key, reset on
	// verified recovery.
	DemotionCount int
	// CreatedAt/UpdatedAt are registry-managed, injected-clock timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// LaneRecord is one named lane a provider contributes. A single-key
// provider uses its own provider_name as the lane_name.
type LaneRecord struct {
	LaneName     string
	ProviderName string
	// ModelFilter restricts the lane to a subset of KnownModels; empty
	// means "all known models".
	ModelFilter []string
	// Weight is the router admission weight; default 1.
	Weight int
	// PoolMembership is the pool name for key-pool lanes, or "" for a
	// standalone lane.
	PoolMembership string
	// PoolIndex is the round-robin dispatch counter within the pool,
	// updated atomically by AdvancePoolIndex. 0 for standalone lanes.
	PoolIndex int
	// Capacity is the R-16.10 capacity bucket this lane draws from.
	Capacity CapacityBucket
	// State is the R-16.10 lane-availability state -- see LaneState's doc
	// comment: there is no permissive zero value.
	State LaneState
	// ResetEstimate is the injected-clock estimate of when a
	// constrained/exhausted State resets.
	ResetEstimate time.Time
}

// Validate is declared on registry.go (ProviderRecord) and lanes.go
// (LaneRecord) rather than here, to keep this file under the 300-line cap.

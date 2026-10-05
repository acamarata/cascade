package learn

// Purpose: the closed vocabularies of the learned-config authority (tier,
//   denylist area, status, source kind) and LearnedConfig, the one stored
//   shape of a learned proposal, with its tier invariant.
// Inputs: strings from callers, stored rows and proposal payloads.
// Outputs: parsed enum values, or KindInvalidInput.
// Constraints: every set is closed and fails closed. There is no zero-value
//   default: "" and any unknown string refuse, and code that ranks an
//   unknown tier ranks it security. C11: learned output never writes
//   policy, security, egress, approval, destructive or minimum-verification
//   rules, so a tier is never taken from a caller, a payload or a stored
//   row; NewLearnedConfig recomputes it through ClassifyChange.
// SPORT: internal.learn.ConfigTier/ADDED, internal.learn.LearnedConfig/ADDED
//   (P1-LRN-01).

import (
	"slices"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ConfigTier is how far a learned change may go without a human: safe
// changes may auto-apply, behavioral changes apply and are reported,
// security changes are never applied (C11).
type ConfigTier string

// The three tiers, least to most restricted.
const (
	TierSafe       ConfigTier = "safe"
	TierBehavioral ConfigTier = "behavioral"
	TierSecurity   ConfigTier = "security"
)

// tierRank orders the tiers. A tier missing from this map ranks as security.
var tierRank = map[ConfigTier]int{TierSafe: 0, TierBehavioral: 1, TierSecurity: 2}

// ParseConfigTier accepts exactly "safe", "behavioral" and "security".
// Anything else, "" and other casings included, is KindInvalidInput.
func ParseConfigTier(s string) (ConfigTier, error) {
	t := ConfigTier(s)
	if _, ok := tierRank[t]; !ok {
		return "", cascade.New(cascade.KindInvalidInput, "learn: config tier must be safe, behavioral or security")
	}
	return t, nil
}

// maxTier returns the stricter of a and b; an unknown tier is security.
func maxTier(a, b ConfigTier) ConfigTier {
	ra, okA := tierRank[a]
	rb, okB := tierRank[b]
	if !okA || !okB {
		return TierSecurity
	}
	if rb > ra {
		return b
	}
	return a
}

// DenylistArea is one closed area of configuration that learned output may
// never write (C11 and the Epic Invalidations).
type DenylistArea string

// The twelve denylist areas.
const (
	AreaAuthRules                DenylistArea = "auth_rules"
	AreaSecretAccess             DenylistArea = "secret_access"
	AreaDestructivePermissions   DenylistArea = "destructive_permissions"
	AreaTrustRoots               DenylistArea = "trust_roots"
	AreaMandatorySecurityGates   DenylistArea = "mandatory_security_gates"
	AreaProviderComplianceFlags  DenylistArea = "provider_compliance_flags"
	AreaModelAuthoringCapability DenylistArea = "model_authoring_capability"
	AreaEgressRules              DenylistArea = "egress_rules"
	AreaApprovalRules            DenylistArea = "approval_rules"
	AreaMinimumVerification      DenylistArea = "minimum_verification"
	AreaBackupGuarantees         DenylistArea = "backup_guarantees"
	AreaExecutionAuthority       DenylistArea = "execution_authority"
)

// denylistAreaTable is the closed area set, in its canonical order. It is
// an array, so nothing can append to it.
var denylistAreaTable = [...]DenylistArea{
	AreaAuthRules, AreaSecretAccess, AreaDestructivePermissions, AreaTrustRoots,
	AreaMandatorySecurityGates, AreaProviderComplianceFlags, AreaModelAuthoringCapability,
	AreaEgressRules, AreaApprovalRules, AreaMinimumVerification, AreaBackupGuarantees,
	AreaExecutionAuthority,
}

// DenylistAreas returns a copy of the twelve areas in canonical order.
func DenylistAreas() []DenylistArea {
	return slices.Clone(denylistAreaTable[:])
}

// ParseDenylistArea accepts exactly one of the twelve area names.
func ParseDenylistArea(s string) (DenylistArea, error) {
	a := DenylistArea(s)
	if !slices.Contains(denylistAreaTable[:], a) {
		return "", cascade.New(cascade.KindInvalidInput, "learn: unknown denylist area")
	}
	return a, nil
}

// Status is a learned config's lifecycle state.
type Status string

// The closed status set.
const (
	StatusPending  Status = "pending"
	StatusApplied  Status = "applied"
	StatusRejected Status = "rejected"
	StatusReverted Status = "reverted"
)

// parseStatus accepts exactly one of the four statuses.
func parseStatus(s Status) (Status, error) {
	if !slices.Contains([]Status{StatusPending, StatusApplied, StatusRejected, StatusReverted}, s) {
		return "", cascade.New(cascade.KindInvalidInput, "learn: status must be pending, applied, rejected or reverted")
	}
	return s, nil
}

// SourceKind names what produced a learned proposal.
type SourceKind string

// The closed source-kind set.
const (
	SourceTelemetryOutcome  SourceKind = "telemetry_outcome"
	SourceSchedulerDecision SourceKind = "scheduler_decision"
	SourceObservation       SourceKind = "observation"
	SourceDetector          SourceKind = "detector"
)

// parseSourceKind accepts exactly one of the four source kinds.
func parseSourceKind(k SourceKind) (SourceKind, error) {
	kinds := []SourceKind{SourceTelemetryOutcome, SourceSchedulerDecision, SourceObservation, SourceDetector}
	if !slices.Contains(kinds, k) {
		return "", cascade.New(cascade.KindInvalidInput, "learn: unknown source kind")
	}
	return k, nil
}

// SourceRef points at the signal behind a proposal by opaque id.
type SourceRef struct {
	Kind SourceKind
	ID   string
}

// EvidenceRef points at one piece of evidence by opaque ids only.
type EvidenceRef struct {
	Kind   string
	ID     string
	RepoID string
	LaneID string
}

// RollbackSnapshot is the value the active version replaced.
type RollbackSnapshot struct {
	PreviousValue string
	SnapshotAt    time.Time
}

// LearnedConfig is one stored learned proposal. Tier, Areas and
// LoosensBound are always the output of ClassifyChange over Target and
// Change; NewLearnedConfig refuses any value that disagrees.
type LearnedConfig struct {
	ID                         string
	Source                     SourceRef
	Target                     TargetID
	Scope, Label               string
	Confidence                 float64
	Tier                       ConfigTier
	Areas                      []DenylistArea
	LoosensBound               bool
	Change                     Change
	Evidence                   []EvidenceRef
	Status                     Status
	Created, LastVerified      time.Time
	SuccessCount, FailureCount int
	Version                    int
	Rollback                   *RollbackSnapshot
}

// NewLearnedConfig validates c. It holds the invariant security <=> (areas
// or LoosensBound), then recomputes the classification of c.Target and
// c.Change and refuses (KindInvalidInput) any stated Tier, Areas or
// LoosensBound that disagrees. It never corrects a value: a row that claims
// the wrong tier is refused, so no caller or stored row can set a tier.
// Security rows also refuse applied status or any version (C11).
func NewLearnedConfig(c LearnedConfig) (LearnedConfig, error) {
	if _, err := ParseConfigTier(string(c.Tier)); err != nil {
		return LearnedConfig{}, err
	}
	if _, err := parseStatus(c.Status); err != nil {
		return LearnedConfig{}, err
	}
	if c.Tier == TierSecurity && (c.Status == StatusApplied || c.Version != 0) {
		return LearnedConfig{}, cascade.New(cascade.KindInvalidInput,
			"learn: a security-tier learned config cannot be applied or have versions")
	}
	restricted := len(c.Areas) > 0 || c.LoosensBound
	if (c.Tier == TierSecurity) != restricted {
		return LearnedConfig{}, cascade.New(cascade.KindInvalidInput,
			"learn: tier security must coincide with a denylist area or a loosened bound")
	}
	cls, err := ClassifyChange(string(c.Target), c.Change)
	if err != nil {
		return LearnedConfig{}, err
	}
	if cls.Target != c.Target || cls.Tier != c.Tier || cls.LoosensBound != c.LoosensBound ||
		!slices.Equal(cls.Areas, c.Areas) {
		return LearnedConfig{}, cascade.New(cascade.KindInvalidInput,
			"learn: stated tier, areas or bound flag disagree with the computed classification")
	}
	return c, nil
}

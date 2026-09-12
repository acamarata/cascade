// Purpose: the closed R-21.25 Role enum and every field enum on the five
//
//	R-21.24 entities, each with String()/Valid(), whose zero value is
//	invalid (06-FORGE-SPEC §5.16 fail-closed rule) -- an unset or
//	unrecognised value is ErrTopologyInvariant at validation time, never
//	a silent default.
//
// Inputs: none. Outputs: none.
// Constraints: no vendor or model name appears anywhere in this file
//
//	(R-21.23).
//
// SPORT: fleet/topology/enums/ADD (P1-E40-W9-S77-T1).

package topology

import (
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Role is the R-21.25 CLOSED provider-neutral role vocabulary a Lane may
// serve. The zero value is invalid.
type Role string

// The eighteen closed Role members.
const (
	RoleExecutive            Role = "executive"
	RolePlanner              Role = "planner"
	RoleArchitect            Role = "architect"
	RoleImplementationLead   Role = "implementation_lead"
	RoleCoder                Role = "coder"
	RoleFastCoder            Role = "fast_coder"
	RoleOperator             Role = "operator"
	RoleResearcher           Role = "researcher"
	RoleScout                Role = "scout"
	RoleDistiller            Role = "distiller"
	RoleLongContext          Role = "long_context"
	RoleTester               Role = "tester"
	RoleReducer              Role = "reducer"
	RoleCritic               Role = "critic"
	RoleAdversary            Role = "adversary"
	RoleRecovery             Role = "recovery"
	RoleAutonomousSubproject Role = "autonomous_subproject"
	RoleFinalAcceptance      Role = "final_acceptance"
)

// Valid reports whether r is one of the eighteen declared members.
func (r Role) Valid() bool {
	switch r {
	case RoleExecutive, RolePlanner, RoleArchitect, RoleImplementationLead, RoleCoder,
		RoleFastCoder, RoleOperator, RoleResearcher, RoleScout, RoleDistiller,
		RoleLongContext, RoleTester, RoleReducer, RoleCritic, RoleAdversary,
		RoleRecovery, RoleAutonomousSubproject, RoleFinalAcceptance:
		return true
	}
	return false
}

// String returns r's wire form.
func (r Role) String() string { return string(r) }

// BillingKind is Account.Billing's closed vocabulary.
type BillingKind string

// The four closed BillingKind members.
const (
	BillingSubscription BillingKind = "subscription"
	BillingAPI          BillingKind = "api"
	BillingFree         BillingKind = "free"
	BillingPool         BillingKind = "pool"
)

// Valid reports whether k is one of the four declared members.
func (k BillingKind) Valid() bool {
	switch k {
	case BillingSubscription, BillingAPI, BillingFree, BillingPool:
		return true
	}
	return false
}

// String returns k's wire form.
func (k BillingKind) String() string { return string(k) }

// AccountRole is Account.Role's closed vocabulary.
type AccountRole string

// The three closed AccountRole members. RoleWorkforce is R-21.34's default
// for an unclassified account, but it is never a permissive zero value --
// Reconcile assigns it explicitly.
const (
	AccountRoleExecutive  AccountRole = "executive"
	AccountRoleWorkforce  AccountRole = "workforce"
	AccountRoleSpecialist AccountRole = "specialist"
)

// Valid reports whether r is one of the three declared members.
func (r AccountRole) Valid() bool {
	switch r {
	case AccountRoleExecutive, AccountRoleWorkforce, AccountRoleSpecialist:
		return true
	}
	return false
}

// String returns r's wire form.
func (r AccountRole) String() string { return string(r) }

// CredentialHealth is Credential.Health's closed vocabulary.
type CredentialHealth string

// The two closed CredentialHealth members.
const (
	CredentialOK          CredentialHealth = "ok"
	CredentialQuarantined CredentialHealth = "quarantined"
)

// Valid reports whether h is one of the two declared members.
func (h CredentialHealth) Valid() bool {
	switch h {
	case CredentialOK, CredentialQuarantined:
		return true
	}
	return false
}

// String returns h's wire form.
func (h CredentialHealth) String() string { return string(h) }

// QuotaDomainKind is QuotaDomain.Kind's closed vocabulary.
type QuotaDomainKind string

// The three closed QuotaDomainKind members.
const (
	QuotaDomainAPIProject         QuotaDomainKind = "api_project"
	QuotaDomainSubscriptionWindow QuotaDomainKind = "subscription_window"
	QuotaDomainSharedPool         QuotaDomainKind = "shared_pool"
)

// Valid reports whether k is one of the three declared members.
func (k QuotaDomainKind) Valid() bool {
	switch k {
	case QuotaDomainAPIProject, QuotaDomainSubscriptionWindow, QuotaDomainSharedPool:
		return true
	}
	return false
}

// String returns k's wire form.
func (k QuotaDomainKind) String() string { return string(k) }

// BillingTier is QuotaDomain.BillingTier's closed vocabulary. `discover`
// is the R-21.24 not-yet-known value -- explicit, not the zero value.
type BillingTier string

// The three closed BillingTier members.
const (
	BillingTierFree     BillingTier = "free"
	BillingTierPaid     BillingTier = "paid"
	BillingTierDiscover BillingTier = "discover"
)

// Valid reports whether t is one of the three declared members.
func (t BillingTier) Valid() bool {
	switch t {
	case BillingTierFree, BillingTierPaid, BillingTierDiscover:
		return true
	}
	return false
}

// String returns t's wire form.
func (t BillingTier) String() string { return string(t) }

// RuntimeKind is RuntimeProfile.Runtime's closed vocabulary.
type RuntimeKind string

// The six closed RuntimeKind members.
const (
	RuntimeClaudeCLI   RuntimeKind = "claude-cli"
	RuntimeCodex       RuntimeKind = "codex"
	RuntimeAntigravity RuntimeKind = "antigravity"
	RuntimeOpenCode    RuntimeKind = "opencode"
	RuntimeAPI         RuntimeKind = "api"
	RuntimeOllama      RuntimeKind = "ollama"
)

// Valid reports whether k is one of the six declared members.
func (k RuntimeKind) Valid() bool {
	switch k {
	case RuntimeClaudeCLI, RuntimeCodex, RuntimeAntigravity, RuntimeOpenCode, RuntimeAPI, RuntimeOllama:
		return true
	}
	return false
}

// String returns k's wire form.
func (k RuntimeKind) String() string { return string(k) }

// Effort is Lane.Effort's closed vocabulary. EffortNA is the explicit
// "not applicable" member for a runtime with no effort dial.
type Effort string

// The six closed Effort members.
const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
	EffortMax    Effort = "max"
	EffortNA     Effort = "n/a"
)

// Valid reports whether e is one of the six declared members.
func (e Effort) Valid() bool {
	switch e {
	case EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax, EffortNA:
		return true
	}
	return false
}

// String returns e's wire form.
func (e Effort) String() string { return string(e) }

// InteractionClass is Lane.InteractionClass's closed vocabulary.
type InteractionClass string

// The two closed InteractionClass members.
const (
	InteractionInteractive InteractionClass = "interactive"
	InteractionBatch       InteractionClass = "batch"
)

// Valid reports whether c is one of the two declared members.
func (c InteractionClass) Valid() bool {
	switch c {
	case InteractionInteractive, InteractionBatch:
		return true
	}
	return false
}

// String returns c's wire form.
func (c InteractionClass) String() string { return string(c) }

// LaneHealth is Lane.Health's closed vocabulary (R-16.10).
type LaneHealth string

// The six closed LaneHealth members.
const (
	LaneHealthAvailable    LaneHealth = "available"
	LaneHealthConstrained  LaneHealth = "constrained"
	LaneHealthExhausted    LaneHealth = "exhausted"
	LaneHealthAuthRequired LaneHealth = "auth-required"
	LaneHealthQuarantined  LaneHealth = "quarantined"
	LaneHealthUnknown      LaneHealth = "unknown"
)

// Valid reports whether h is one of the six declared members.
func (h LaneHealth) Valid() bool {
	switch h {
	case LaneHealthAvailable, LaneHealthConstrained, LaneHealthExhausted,
		LaneHealthAuthRequired, LaneHealthQuarantined, LaneHealthUnknown:
		return true
	}
	return false
}

// String returns h's wire form.
func (h LaneHealth) String() string { return string(h) }

// encodeRoles marshals roles as a JSON string array, for storage in
// config_lane.roles (store.go/reconcile.go).
func encodeRoles(roles []Role) (string, error) {
	strs := make([]string, len(roles))
	for i, r := range roles {
		strs[i] = string(r)
	}
	b, err := json.Marshal(strs)
	if err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "topology: encode roles")
	}
	return string(b), nil
}

// decodeRoles is encodeRoles's inverse.
func decodeRoles(data string) ([]Role, error) {
	var strs []string
	if err := json.Unmarshal([]byte(data), &strs); err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "topology: decode roles")
	}
	roles := make([]Role, len(strs))
	for i, s := range strs {
		roles[i] = Role(s)
	}
	return roles, nil
}

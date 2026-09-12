// Package topology holds the fleet-topology entities ratified by R-21.24
// (Account, Credential, QuotaDomain, RuntimeProfile, Lane), their closed
// enums, invariants, lane-class price table, derived lane identity, the
// immutable offering snapshot, and the CRUD/reconcile storage layer over
// the EXISTING `config` cascade.db domain (R-21.22 keeps the domain list
// closed, so this package extends J/S-20.T2's provider registry rather
// than registering a twelfth domain). No vendor or model name is parsed
// anywhere in this package (R-21.23); a credential value never reaches a
// column, only a vault-key reference (R-16.44).
//
// This file: the five entities and their identifier types.
//
// SPORT: fleet/topology/types/ADD (P1-E40-W9-S77-T1).
package topology

import "time"

// AccountID identifies one Account row. The empty value is invalid.
type AccountID string

// Valid reports whether id is non-empty.
func (id AccountID) Valid() bool { return id != "" }

// CredentialID identifies one Credential row. The empty value is invalid.
type CredentialID string

// Valid reports whether id is non-empty.
func (id CredentialID) Valid() bool { return id != "" }

// QuotaDomainID identifies one QuotaDomain row. The empty value is invalid.
type QuotaDomainID string

// Valid reports whether id is non-empty.
func (id QuotaDomainID) Valid() bool { return id != "" }

// DomainID is QuotaDomainID's package-local alias, used on every field and
// cross-package signature that names a quota-domain reference (R-21.24's
// own "QuotaDomainID (alias DomainID)" wording). This is NOT
// internal/storage.DomainID -- that type names one of the twelve closed
// cascade.db storage domains; this one names a row of this package's own
// QuotaDomain entity, which itself lives inside the `config` storage
// domain. The two never appear in the same expression, so the shared name
// causes no ambiguity across package boundaries.
type DomainID = QuotaDomainID

// RuntimeProfileID identifies one RuntimeProfile row. The empty value is
// invalid.
type RuntimeProfileID string

// Valid reports whether id is non-empty.
func (id RuntimeProfileID) Valid() bool { return id != "" }

// LaneID identifies one Lane row. Never generated fresh by a caller --
// see lane_identity.go's LaneID function, which derives it deterministically
// so every writer upserts by the same key. The empty value is invalid.
type LaneID string

// Valid reports whether id is non-empty.
func (id LaneID) Valid() bool { return id != "" }

// VaultKeyRef is a vault broker key NAME, never a credential value.
// Declared independently of internal/providers/registry.VaultKeyRef so
// this package never imports that one (mirrors registry's own "declared
// independently" precedent against internal/providers/intake).
type VaultKeyRef string

// String returns the ref's name. Safe to log: a VaultKeyRef never holds a
// credential value, only the vault broker's key name.
func (r VaultKeyRef) String() string { return string(r) }

// BillingInfo is Account's billing shape.
type BillingInfo struct {
	Kind BillingKind
	// UserReportedMonthlyMicros is personal-config-only, in micro-USD; it
	// is never derived from observed usage and is not tracked by
	// Reconcile (always 0 on a reconciled row).
	UserReportedMonthlyMicros int64
}

// Account is the R-21.24 account entity: one row per provider account.
type Account struct {
	ID       AccountID
	Provider string
	Billing  BillingInfo
	Role     AccountRole
}

// Credential is the R-21.24 credential entity. SecretRef is a vault key
// reference only -- no field here or anywhere reachable from it may ever
// hold a raw secret value (R-16.44, R-21.22).
type Credential struct {
	ID                CredentialID
	AccountRef        AccountID
	QuotaDomainRef    DomainID
	SecretRef         VaultKeyRef
	RuntimeProfileRef RuntimeProfileID
	Health            CredentialHealth
	QuarantineReason  string
	QuarantinedUntil  time.Time
}

// QuotaDomain is the R-21.24 quota-domain entity.
type QuotaDomain struct {
	ID          DomainID
	AccountRef  AccountID
	Kind        QuotaDomainKind
	BillingTier BillingTier
	Quarantined bool
	// Dimensions holds this domain's observed buckets, keyed by dimension
	// name from the closed per-Kind set (dimensions.go's ValidateDimensions).
	Dimensions map[string]Bucket
	// Batch is this domain's two R-21.116 gauge limits (dimensions.go).
	Batch BatchLimits
	// IndependenceProven is R-21.95's fail-closed default: only an
	// api_project domain with this explicitly set true (proven, from the
	// provider's documented per-project semantics) gets a distinct
	// limit_scope.go ResolveScope; every other case -- including an
	// api_project domain that leaves this false -- conservatively shares
	// the account-level scope, so an unproven independence claim can
	// never split a real provider limit.
	IndependenceProven bool
}

// Endpoint is RuntimeProfile's optional network endpoint.
type Endpoint struct {
	Host string
	Port int
}

// RuntimeProfile is the R-21.24 runtime-profile entity.
type RuntimeProfile struct {
	ID         RuntimeProfileID
	Runtime    RuntimeKind
	ConfigHome string
	Persistent bool
	Endpoint   Endpoint
	EnvVar     string
}

// Lane is the R-21.24 lane entity. CredentialRef is nil for a subscription
// runtime (R-21.107's nil-credential allowlist). ModelIdentity is
// R-21.72's provider-declared identity and OfferingSnapshot is R-21.101's
// immutable, versioned capability snapshot -- both populated only by
// Reconcile / a future providers/<vendor> discovery pass, never parsed in
// this package.
type Lane struct {
	ID                LaneID
	RuntimeProfileRef RuntimeProfileID
	QuotaDomainRef    DomainID
	// CredentialRef is nil for a subscription runtime lane (R-21.107).
	CredentialRef    *CredentialID
	ModelID          string
	Effort           Effort
	Roles            []Role
	LaneClass        LaneClass
	InteractionClass InteractionClass
	BaseShadowPrice  float64
	Health           LaneHealth
	ModelIdentity    ModelIdentity
	OfferingSnapshot OfferingSnapshot
	DiscoveredAt     time.Time
	// RetiredAt is nil for a live lane. RetireLane sets it; a row is
	// never deleted (store.go).
	RetiredAt *time.Time
}

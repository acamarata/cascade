// Purpose (this file): ResourceRequest, plus the request-scoped enums this
// ticket owns (QualityEnum, DeadlineEnum, DataClass) and the raise-only
// DataClass floor validator (R-21.143). Reused, not redeclared: Tier
// (priors.go, R-16.71), conductor.TaskClass (R-16.79), provider.
// SensitivityTier (06 SPEC.5.16, zero value already resolves to
// restricted), jobs.RiskClass (R-16.37 §Jobs).
//
// Inputs: caller-supplied field values plus a derived DataClass floor
// (R-21.143, computed upstream -- ValidateResourceRequest only enforces
// the raise-only relationship once given both sides).
// Outputs: a normalized ResourceRequest or a typed pkg/cascade error.
// Constraints: no permissive zero-value on any enum (06-FORGE-SPEC.md
// §5.15); unset/unknown resolves to the most-restrictive member for
// Sensitivity/RiskClass/DataClass; QualityEnum/DeadlineEnum/TierEnum carry
// no default and must be set explicitly.
//
// SPORT: fleet.capacity.request (ADD, P1-E31-W6-S63-T2).

package capacity

import (
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// SessionScopeID identifies the caller's session scope.
type SessionScopeID string

// TierEnum is Tier (priors.go) under the contract's requested name: a
// type alias, not a new type, so there is one tier vocabulary (R-16.71)
// -- see snapshot.go's State/BucketKind for this package's own precedent.
type TierEnum = Tier

// textEnum/textDecodeErr/marshalEnumText/unmarshalEnumText give every
// string enum below (and Tier, priors.go -- a method may live in any file
// of its own package) one shared TextMarshaler/TextUnmarshaler, honored by
// both encoding/json and go-toml/v2 for a scalar field.
type textEnum interface{ ~string }

func textDecodeErr(kind, raw string) error {
	return cascade.Newf(cascade.KindInvalidInput, "capacity: unknown %s %q", kind, raw)
}

func marshalEnumText[T textEnum](v T, valid bool, kind string) ([]byte, error) {
	if !valid {
		return nil, textDecodeErr(kind, string(v))
	}
	return []byte(v), nil
}

func unmarshalEnumText[T textEnum](b []byte, valid func(T) bool, kind string) (T, error) {
	v := T(b)
	if !valid(v) {
		return v, textDecodeErr(kind, string(b))
	}
	return v, nil
}

// MarshalText implements encoding.TextMarshaler for Tier.
func (t Tier) MarshalText() ([]byte, error) { return marshalEnumText(t, t.Valid(), "tier") }

// UnmarshalText implements encoding.TextUnmarshaler for Tier.
func (t *Tier) UnmarshalText(b []byte) error {
	v, err := unmarshalEnumText(b, Tier.Valid, "tier")
	if err == nil {
		*t = v
	}
	return err
}

// QualityEnum is the closed min_quality vocabulary. No permissive zero
// value: the empty string is not a member, so a caller must explicitly
// set MinQuality.
type QualityEnum string

// The three closed QualityEnum members.
const (
	QualityStandard QualityEnum = "standard"
	QualityHigh     QualityEnum = "high"
	QualityMax      QualityEnum = "max"
)

// Valid reports whether q is one of the three declared members.
func (q QualityEnum) Valid() bool {
	switch q {
	case QualityStandard, QualityHigh, QualityMax:
		return true
	}
	return false
}

// MarshalText implements encoding.TextMarshaler for QualityEnum.
func (q QualityEnum) MarshalText() ([]byte, error) {
	return marshalEnumText(q, q.Valid(), "min_quality")
}

// UnmarshalText implements encoding.TextUnmarshaler for QualityEnum.
func (q *QualityEnum) UnmarshalText(b []byte) error {
	v, err := unmarshalEnumText(b, QualityEnum.Valid, "min_quality")
	if err == nil {
		*q = v
	}
	return err
}

// DeadlineEnum is the closed deadline vocabulary. No permissive zero
// value.
type DeadlineEnum string

// The two closed DeadlineEnum members.
const (
	DeadlineInteractive DeadlineEnum = "interactive"
	DeadlineBatch       DeadlineEnum = "batch"
)

// Valid reports whether d is one of the two declared members.
func (d DeadlineEnum) Valid() bool {
	switch d {
	case DeadlineInteractive, DeadlineBatch:
		return true
	}
	return false
}

// MarshalText implements encoding.TextMarshaler for DeadlineEnum.
func (d DeadlineEnum) MarshalText() ([]byte, error) { return marshalEnumText(d, d.Valid(), "deadline") }

// UnmarshalText implements encoding.TextUnmarshaler for DeadlineEnum.
func (d *DeadlineEnum) UnmarshalText(b []byte) error {
	v, err := unmarshalEnumText(b, DeadlineEnum.Valid, "deadline")
	if err == nil {
		*d = v
	}
	return err
}

// DataClass is this package's closed data-sensitivity floor (R-21.94,
// R-21.143). It mirrors internal/jobs.DataClass's four-value ordering BY
// VALUE, not by import, matching that file's own precedent (and
// internal/evidence/dataclass.go's) for the identical situation: a
// package-local copy with identical names/ordering keeps every consumer
// interoperable at the wire without adding a fifth cross-package edge for
// four string constants.
type DataClass string

// The closed DataClass vocabulary, ordered least to most sensitive,
// matching internal/jobs.DataClass and internal/policy.DataClass exactly.
const (
	DataClassPublic       DataClass = "public"
	DataClassInternal     DataClass = "internal"
	DataClassConfidential DataClass = "confidential"
	DataClassSecret       DataClass = "secret"
)

// dataClassRank orders the four values for the raise-only comparison
// below. An invalid value never reaches this map -- callers resolve it to
// DataClassSecret first.
var dataClassRank = map[DataClass]int{
	DataClassPublic:       0,
	DataClassInternal:     1,
	DataClassConfidential: 2,
	DataClassSecret:       3,
}

// Valid reports whether d is one of the four closed values.
func (d DataClass) Valid() bool {
	_, ok := dataClassRank[d]
	return ok
}

// MarshalText implements encoding.TextMarshaler for DataClass.
func (d DataClass) MarshalText() ([]byte, error) { return marshalEnumText(d, d.Valid(), "data_class") }

// UnmarshalText implements encoding.TextUnmarshaler for DataClass.
func (d *DataClass) UnmarshalText(b []byte) error {
	v, err := unmarshalEnumText(b, DataClass.Valid, "data_class")
	if err == nil {
		*d = v
	}
	return err
}

// ValidateDataClassFloor enforces R-21.143's raise-only rule: presented
// must be a valid DataClass at or above floor's rank. A presented value
// below the floor is refused with a typed policy-denied error -- it is
// never silently downgraded or silently accepted. floor itself is
// resolved by the caller (ValidateResourceRequest resolves an unset floor
// to DataClassSecret, the most restrictive member, before calling this).
func ValidateDataClassFloor(presented, floor DataClass) (DataClass, error) {
	if !presented.Valid() {
		return "", textDecodeErr("data_class", string(presented))
	}
	if !floor.Valid() {
		floor = DataClassSecret
	}
	if dataClassRank[presented] < dataClassRank[floor] {
		return "", cascade.Newf(cascade.KindPolicyDenied,
			"capacity: data_class %q is below the derived floor %q: downgrade refused (R-21.143)", presented, floor)
	}
	return presented, nil
}

// ResourceRequest is the tier-policy engine's primary input (06-FORGE-SPEC
// §Epic AE DECIDED; R-16.11/R-16.37).
type ResourceRequest struct {
	Intent        string
	Scope         SessionScopeID
	TaskClass     conductor.TaskClass
	MinQuality    QualityEnum
	Mutation      bool
	Sensitivity   provider.SensitivityTier
	RiskClass     jobs.RiskClass
	Deadline      DeadlineEnum
	PreferredTier TierEnum
	ReserveTier0  bool
	DataClass     DataClass
}

// conductorTaskClasses is every §5.16 task-class member ResourceRequest.
// TaskClass may legitimately name -- a superset of priorTaskClasses (this
// package's own priors.go), since a request naming "segment" or "chat"
// (the two rows priors carries no cell for) is still a well-formed
// request; CapabilityScorer.Score's own contract returns 0.0 (the
// most-restrictive value) for a class it has no data for, rather than
// this package refusing the request outright. conductor.TaskClass has no
// exported Valid() method (R-16.79's owner), so this local membership
// check enumerates the same nine names declared there without adding a
// second scored table.
var conductorTaskClasses = map[conductor.TaskClass]bool{
	conductor.TaskClassClassify:  true,
	conductor.TaskClassSegment:   true,
	conductor.TaskClassSummarize: true,
	conductor.TaskClassExtract:   true,
	conductor.TaskClassChat:      true,
	conductor.TaskClassCode:      true,
	conductor.TaskClassReason:    true,
	conductor.TaskClassReview:    true,
	conductor.TaskClassArbitrate: true,
}

// validRiskClass reports whether r is one of jobs.RiskClass's four closed
// members. jobs.RiskClass exposes no Valid() method, so this enumerates
// its constants directly rather than adding one to jobs on this ticket's
// behalf.
func validRiskClass(r jobs.RiskClass) bool {
	switch r {
	case jobs.RiskClassLow, jobs.RiskClassNormal, jobs.RiskClassHigh, jobs.RiskClassCritical:
		return true
	}
	return false
}

// ValidateResourceRequest normalizes req against its fail-closed defaults
// and returns a typed error for anything that cannot be resolved safely.
// derivedFloor is the R-21.143 provenance-derived DataClass floor (an
// unset/invalid floor resolves to DataClassSecret, the most restrictive
// member, per §5.15).
func ValidateResourceRequest(req ResourceRequest, derivedFloor DataClass) (ResourceRequest, error) {
	out := req
	if out.Intent == "" {
		return ResourceRequest{}, cascade.Newf(cascade.KindInvalidInput, "capacity: resource request intent must not be empty")
	}
	if !conductorTaskClasses[out.TaskClass] {
		return ResourceRequest{}, cascade.Newf(cascade.KindInvalidInput, "capacity: unknown task_class %q", out.TaskClass)
	}
	if !out.MinQuality.Valid() {
		return ResourceRequest{}, cascade.Newf(cascade.KindInvalidInput, "capacity: min_quality must be explicitly one of standard, high, max, got %q", out.MinQuality)
	}
	if !out.Deadline.Valid() {
		return ResourceRequest{}, cascade.Newf(cascade.KindInvalidInput, "capacity: deadline must be explicitly one of interactive, batch, got %q", out.Deadline)
	}
	if out.PreferredTier != "" && !out.PreferredTier.Valid() {
		return ResourceRequest{}, cascade.Newf(cascade.KindInvalidInput, "capacity: unknown preferred_tier %q", out.PreferredTier)
	}
	if !out.Sensitivity.Valid() {
		return ResourceRequest{}, cascade.Newf(cascade.KindInvalidInput, "capacity: sensitivity tier %v out of range", out.Sensitivity)
	}
	if out.RiskClass == "" {
		out.RiskClass = jobs.RiskClassCritical
	} else if !validRiskClass(out.RiskClass) {
		return ResourceRequest{}, cascade.Newf(cascade.KindInvalidInput, "capacity: unknown risk_class %q", out.RiskClass)
	}
	floor := derivedFloor
	if !floor.Valid() {
		floor = DataClassSecret
	}
	if out.DataClass == "" {
		out.DataClass = floor
		return out, nil
	}
	raised, err := ValidateDataClassFloor(out.DataClass, floor)
	if err != nil {
		return ResourceRequest{}, err
	}
	out.DataClass = raised
	return out, nil
}

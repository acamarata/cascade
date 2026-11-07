// Purpose: the lane-rank vocabulary (contract:lane-rank). These are the
// types the ranker, its predicates, its utility terms and its explain
// printer share; this file declares them only, with no ranking logic.
//
// Inputs: topology lane identity, health, class and data-class ceiling;
// this package's Mode, Tables, Weights and ShadowPriceResult; the
// canonical provider.SensitivityTier.
//
// Outputs: Candidate, PremiumPermit, EnvelopeFn, LearnedPairsFn,
// RankRequest, Verdict, Term, Ranked, Ranking, Predicate, UtilityTerm.
//
// Constraints: names, field names, field types and field order are fixed
// by the contract and checked by TestRankTypesExactShape; later rank
// tickets build on them, so any change here is a contract change.

package economics

import (
	"math/rand"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/provider"
)

// Candidate is one lane as the ranker sees it: identity, price, capability
// and every eligibility signal, read once per rank call.
type Candidate struct {
	// LaneID is the topology lane id; ties in Order break on it.
	LaneID topology.LaneID
	// RegistryLane is the lane's registry name.
	RegistryLane string
	// DomainID is the quota domain the lane draws from.
	DomainID topology.DomainID
	// Provider is the provider id that serves the lane.
	Provider string
	// Identity is the canonical model identity behind the lane.
	Identity topology.ModelIdentity
	// LaneClass is the lane's class.
	LaneClass topology.LaneClass
	// BasePrice is the lane's base price before scarcity.
	BasePrice float64
	// SP is the lane's shadow price.
	SP ShadowPriceResult
	// Roles are the roles the lane may fill.
	Roles []topology.Role
	// TaskClasses are the task classes the lane accepts; nil = all task classes.
	TaskClasses []string
	// Modalities and Tools are what the lane supports.
	Modalities, Tools []string
	// Ceiling is the lane's data-class ceiling.
	Ceiling topology.Ceiling
	// CeilingErr is the ceiling read error; non-nil makes the lane ineligible.
	CeilingErr error
	// Health is the lane's health state.
	Health topology.LaneHealth
	// TransientDemoted is true while the lane is demoted for a transient fault.
	TransientDemoted bool
	// Reservable is nil when a reservation can be taken, else why not.
	Reservable error
	// AtOrBelowReserve and BelowAbsoluteFloor are the shadow-price signals.
	AtOrBelowReserve, BelowAbsoluteFloor bool
	// WorkspacePermitted and ModelAvailable gate the workspace and the model.
	WorkspacePermitted, ModelAvailable bool
	// ExpectedQuality and PredictedFailure are the learned quality estimates.
	ExpectedQuality, PredictedFailure float64
	// LatencyP50 is the lane's median latency.
	LatencyP50 time.Duration
	// EWMA429 is the lane's smoothed rate-limit rate.
	EWMA429 float64
}

// PremiumPermit bounds premium-lane use for one request.
type PremiumPermit struct {
	// AllowedRoles are the roles the permit covers.
	AllowedRoles []topology.Role
	// MaxCalls is the permit's call budget; CallsUsed is what is spent.
	MaxCalls, CallsUsed int
}

// EnvelopeFn reports whether the caller's envelope permits a candidate,
// with the reason when it does not.
type EnvelopeFn func(c Candidate) (permitted bool, reason string)

// LearnedPairsFn returns the learned correlation penalty between a producer
// and a candidate for a task class, and whether one is known.
type LearnedPairsFn func(producer topology.ModelIdentity, producerProvider string, candidate topology.ModelIdentity, candidateProvider string, taskClass string, verify bool) (penalty float64, ok bool)

// RankRequest is one rank call's input. It carries Sensitivity, never a
// field named Tier.
type RankRequest struct {
	// Mode is the scheduler mode.
	Mode Mode
	// TaskClass is the request's task class.
	TaskClass string
	// Modalities and Tools are what the request needs.
	Modalities, Tools []string
	// Sensitivity is the request's data class.
	Sensitivity provider.SensitivityTier
	// Roles are the roles the request wants filled.
	Roles []topology.Role
	// Producer is the producing model, nil when there is none.
	Producer *topology.ModelIdentity
	// ProducerSession is the producing session id.
	ProducerSession string
	// IndependenceWeight overrides the independence weight when non-nil.
	IndependenceWeight *float64
	// AllowPremium and AllowReserve open premium and reserve lanes.
	AllowPremium, AllowReserve bool
	// Premium is the premium permit, nil when there is none.
	Premium *PremiumPermit
	// Envelope is the caller's envelope check.
	Envelope EnvelopeFn
	// LearnedPairs is the learned correlation source.
	LearnedPairs LearnedPairsFn
	// Tables are the economics tables in force.
	Tables Tables
	// Jitter is the tie-break randomness source; it is required by Rank.
	Jitter rand.Source
}

// Verdict is one predicate's result for one candidate.
type Verdict struct {
	// Predicate is the predicate name.
	Predicate string
	// Applicable is false when the predicate does not apply; Pass is its result.
	Applicable, Pass bool
	// Reason says why the predicate failed or did not apply.
	Reason string
}

// Term is one utility term's result for one candidate.
type Term struct {
	// Name is the term name.
	Name string
	// Weight, Value and Contribution are the term's weight, raw value and
	// weighted share of the utility.
	Weight, Value, Contribution float64
}

// Ranked is one candidate with its verdicts, terms and utility.
type Ranked struct {
	// Candidate is the ranked lane.
	Candidate Candidate
	// Eligible is true when every applicable verdict passed.
	Eligible bool
	// Verdicts are nine in order, one per predicate.
	Verdicts []Verdict
	// Terms are six, one per utility term.
	Terms []Term
	// Correlation is the correlation penalty; Utility is the final utility.
	Correlation, Utility float64
}

// Ranking is a rank call's output.
type Ranking struct {
	// Order is eligible by utility desc, SP asc, lane_id asc, then
	// ineligible by lane_id.
	Order []Ranked
	// RoleFallback is true when no lane matched the requested roles and the
	// ranking fell back.
	RoleFallback bool
}

// Predicate is one named eligibility check.
type Predicate struct {
	// Name is the predicate name.
	Name string
	// Eval returns the predicate's verdict for a candidate.
	Eval func(RankRequest, Candidate) Verdict
}

// UtilityTerm is one named utility term.
type UtilityTerm struct {
	// Name is the term name.
	Name string
	// Eval returns the term for a candidate under the given weights.
	Eval func(RankRequest, Candidate, Weights) Term
}

// Purpose: the nine ordered R-21.33 eligibility gates and premium permit.
// Inputs: immutable request and candidate snapshots, including injected envelope.
// Outputs: reasoned verdicts; no admission or reservation side effects.
// Constraints: pure evaluation, closed enums, topology owns data-class policy.

package economics

import (
	"math"
	"slices"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrEnvelopeExceeded reports a premium request outside its explicit permit.
var ErrEnvelopeExceeded = cascade.New(cascade.KindPolicyDenied, "economics: envelope exceeded")

// EligibilityPredicates returns fresh rows in R-21.33's written order.
func EligibilityPredicates() []Predicate {
	return []Predicate{
		{"model-available", func(_ RankRequest, c Candidate) Verdict {
			return rankVerdict("model-available", c.ModelAvailable, "model unavailable")
		}},
		{"task-class", taskClassVerdict},
		{"modality-tools", func(r RankRequest, c Candidate) Verdict {
			return rankVerdict("modality-tools", containsRequired(c.Modalities, r.Modalities) && containsRequired(c.Tools, r.Tools), "required modality or tool unavailable")
		}},
		{"data-class", dataClassVerdict},
		{"workspace-permission", func(_ RankRequest, c Candidate) Verdict {
			return rankVerdict("workspace-permission", c.WorkspacePermitted, "workspace permission denied")
		}},
		{"lane-health", func(_ RankRequest, c Candidate) Verdict {
			healthy := c.Health == topology.LaneHealthAvailable || c.Health == topology.LaneHealthConstrained
			return rankVerdict("lane-health", healthy && !c.TransientDemoted, "lane unhealthy")
		}},
		{"quota-reservable", func(_ RankRequest, c Candidate) Verdict {
			if c.Reservable != nil {
				return rankVerdict("quota-reservable", false, c.Reservable.Error())
			}
			return rankVerdict("quota-reservable", true, "")
		}},
		{"delegation-envelope", envelopeVerdict},
		{"mode-permits", modePermitsVerdict},
	}
}

func rankVerdict(name string, pass bool, reason string) Verdict {
	if pass {
		reason = ""
	}
	return Verdict{Predicate: name, Applicable: true, Pass: pass, Reason: reason}
}

func containsRequired(have, need []string) bool {
	for _, item := range need {
		if item == "" || !slices.Contains(have, item) {
			return false
		}
	}
	return true
}

func taskClassVerdict(r RankRequest, c Candidate) Verdict {
	known := false
	for class := range tokensOutDefaults {
		known = known || string(class) == r.TaskClass
	}
	pass := known && c.LaneClass.Valid() && (c.TaskClasses == nil || slices.Contains(c.TaskClasses, r.TaskClass))
	if classes := topology.EligibleTaskClasses(c.LaneClass); classes != nil {
		pass = pass && slices.Contains(classes, topology.TaskClass(r.TaskClass))
	}
	return rankVerdict("task-class", pass, "task class not admitted")
}

func dataClassVerdict(r RankRequest, c Candidate) Verdict {
	if c.CeilingErr != nil {
		kind, ok := cascade.KindOf(c.CeilingErr)
		reason := "ceiling unavailable: unknown"
		if ok {
			reason = "ceiling unavailable: " + kind.String()
		}
		return rankVerdict("data-class", false, reason)
	}
	return rankVerdict("data-class", topology.TierAdmits(r.Sensitivity, c.Ceiling), "data class exceeds lane ceiling")
}

func envelopeVerdict(r RankRequest, c Candidate) Verdict {
	if r.Envelope == nil {
		return Verdict{Predicate: "delegation-envelope", Pass: true, Reason: "no delegation envelope"}
	}
	pass, reason := r.Envelope(c)
	if !pass && reason == "" {
		reason = "delegation envelope refused"
	}
	return rankVerdict("delegation-envelope", pass, reason)
}

func modePermitsVerdict(r RankRequest, c Candidate) Verdict {
	reason := ""
	switch {
	case !r.Mode.Valid():
		reason = "unknown scheduler mode"
	case !c.LaneClass.Valid() || math.IsNaN(c.BasePrice) || math.IsInf(c.BasePrice, 0) || c.BasePrice < 0:
		reason = "invalid lane price or class"
	case c.BelowAbsoluteFloor && !r.AllowReserve:
		reason = "below absolute reserve floor"
	case c.AtOrBelowReserve && r.Mode != ModeIncident && !r.AllowReserve:
		reason = "at or below hard reserve"
	case r.Mode == ModeCrunch && c.BasePrice >= 5 && !r.AllowPremium:
		reason = "crunch bars premium lanes"
	}
	return rankVerdict("mode-permits", reason == "", reason)
}

// PremiumPermitted checks the explicit role and remaining-call permit for
// premium lanes (base price >= 5), irrespective of mode or task class.
// Non-premium lanes do not need a permit. A permit is never consumed here.
func PremiumPermitted(r RankRequest, c Candidate) Verdict {
	if c.LaneClass.Valid() && c.BasePrice >= 0 && c.BasePrice < 5 {
		return Verdict{Predicate: "premium", Pass: true, Reason: "non-premium lane"}
	}
	pass := r.AllowPremium && r.Premium != nil && len(r.Roles) > 0 && c.LaneClass.Valid() && c.BasePrice >= 5 && !math.IsNaN(c.BasePrice) && !math.IsInf(c.BasePrice, 0)
	if pass {
		p := r.Premium
		pass = p.CallsUsed >= 0 && p.MaxCalls > p.CallsUsed
		for _, role := range r.Roles {
			pass = pass && role.Valid() && slices.Contains(p.AllowedRoles, role)
		}
	}
	return rankVerdict("premium", pass, ErrEnvelopeExceeded.Error())
}

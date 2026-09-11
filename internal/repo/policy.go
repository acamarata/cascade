package repo

// Purpose: the R-21.180 policy projection ceiling. Accepted facts are
//   exposed as path-scoped, INERT data records (fact + scope +
//   provenance) for AG/S-68.T5's rules generator; proposed, rejected and
//   superseded facts are structurally excluded. The projection emits
//   ONLY the seven closed-enum fields as data -- it never writes a
//   policy field governing gates, risk classes, capabilities, egress
//   classes or autonomy, never emits executable content, and never
//   widens scope or permissions. policyFieldDenylist below is the
//   explicit denylist; ProjectPolicy applies it to every candidate
//   record before returning it, so a hostile proposal that talks about
//   waived gates can be a Fact string but can never become a denylisted
//   FIELD.
// Inputs: a slice of InferredFact (typically LedgerStore.List's output
//   for one repository).
// Outputs: PolicyRecord values for accepted facts only; a typed error
//   for a denylisted field name reaching the projection.
// Constraints: fail-closed -- the denylist check runs even though this
//   file's own PolicyRecord type structurally has no field for gates/
//   risk/capabilities/egress/autonomy, because R-21.180's own
//   requirement is that a HOSTILE PROPOSAL naming one of those fields
//   (in its Subject or Fact text) can never reach policy in ANY state,
//   not merely that this type has no slot for it.
// SPORT: repo/inferred-fact-ledger/ADD (P1-E33-W7-S67-T2).

import (
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// PolicyRecord is one accepted fact projected as inert, path-scoped data
// for the rules generator. It carries no executable content and no
// capability/gate/risk/egress/autonomy field.
type PolicyRecord struct {
	Subject    FactSubject `json:"subject"`
	Fact       string      `json:"fact"`
	Scope      string      `json:"scope"`
	Source     string      `json:"source"`
	Confidence float64     `json:"confidence"`
	Version    int         `json:"version"`
}

// policyFieldDenylist is the closed set of field-name-shaped strings
// R-21.180's ceiling forbids a projected record from ever carrying,
// whether they appear as a Subject value or as free text inside Fact.
// This is deliberately broader than FactSubject's own closed enum (which
// already excludes them structurally): the denylist exists so a hostile
// proposal's FACT TEXT asserting "gates are waived for this path" is
// caught too, not only a hostile Subject value.
var policyFieldDenylist = []string{
	"gate", "gates", "risk_class", "risk-class", "capability", "capabilities",
	"egress_class", "egress-class", "autonomy", "waive", "waived", "bypass",
	"permission", "permissions", "elevat",
}

// containsDenylistedField reports whether text mentions any denylisted
// field-shaped term, case-insensitively.
func containsDenylistedField(text string) (string, bool) {
	lower := strings.ToLower(text)
	for _, term := range policyFieldDenylist {
		if strings.Contains(lower, term) {
			return term, true
		}
	}
	return "", false
}

// ProjectPolicy filters facts to accepted-only and renders each as a
// PolicyRecord scoped to scope. Proposed, rejected, and superseded facts
// are structurally excluded -- never returned, not even in a filtered-
// out list a caller could accidentally use. A fact whose Fact text
// mentions a denylisted field is refused outright (never silently
// dropped and never silently passed through), so a caller sees the
// refusal rather than concluding "no facts to project" when one was
// actually blocked.
func ProjectPolicy(facts []InferredFact, scope string) ([]PolicyRecord, error) {
	if scope == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "repo: ProjectPolicy requires a non-empty scope")
	}
	var out []PolicyRecord
	for _, f := range facts {
		if f.State != FactAccepted {
			continue
		}
		if err := f.Validate(); err != nil {
			return nil, err
		}
		if term, hit := containsDenylistedField(f.Fact); hit {
			return nil, cascade.Newf(cascade.KindPolicyDenied,
				"repo: fact %s mentions denylisted policy field %q and cannot be projected", f.ID, term)
		}
		if term, hit := containsDenylistedField(string(f.Subject)); hit {
			return nil, cascade.Newf(cascade.KindPolicyDenied,
				"repo: fact %s subject mentions denylisted policy field %q and cannot be projected", f.ID, term)
		}
		out = append(out, PolicyRecord{
			Subject:    f.Subject,
			Fact:       f.Fact,
			Scope:      scope,
			Source:     f.Source,
			Confidence: f.Confidence,
			Version:    f.Version,
		})
	}
	return out, nil
}

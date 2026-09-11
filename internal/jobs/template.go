package jobs

// Purpose: JobTemplate, the six-kind TemplateRegistry (implement, review,
//
//	adversarial, qa, ci, integrate) By() resolves against, and the
//	TemplateContext seam a caller uses to carry per-invocation data
//	(ticket id, footprint, pass-through fields) through Resolve's fixed
//	single-argument signature (this ticket's own HOW-1 interface).
//
// Inputs: a kind string (By), or a context.Context carrying a
//
//	TemplateContext (Resolve, template_kinds.go).
//
// Outputs: a JobTemplate (By), or a DagNode (Resolve).
//
// Constraints: Resolve(ctx) takes no second parameter (task 1's own
//
//	interface line), so per-ticket data (id, footprint, the
//	PassThroughFields) travels via TemplateContext on ctx rather than a
//	widened method signature -- WithTemplateContext/templateContextFrom
//	below. A nil ctx or a ctx with no TemplateContext attached is a
//	typed refusal, never a zero-value DagNode. Register is exported and
//	additive (AH/S-69.T2 extends this same registry with CR/QA/
//	adversarial-reviewer templates per this ticket's own Seam note); it
//	overwrites a kind already present rather than erroring, so a later
//	ticket may re-register a kind this one seeded without needing a
//	remove-first step.
//
// SPORT: jobs/job-templates/ADD (P1-E29-W6-S60-T2).

import (
	"context"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// JobTemplate resolves a typed job-template kind into a DagNode. Every
// implementation in template_kinds.go is a real, complete Resolve --
// no stub, no placeholder (Art.1).
type JobTemplate interface {
	Resolve(ctx context.Context) (DagNode, error)
}

// ErrUnknownTemplateKind is returned by By for any kind outside the six
// registered below. It is a sentinel *cascade.Error (KindNotFound), so
// callers can match it with errors.Is.
var ErrUnknownTemplateKind = cascade.New(cascade.KindNotFound, "jobs: unknown template kind")

// templateRegistry is the package-level kind -> JobTemplate map.
// Guarded by mu because AH/S-69.T2 registers additional kinds from its
// own init(), and Go does not order init() across files by any contract
// this package should rely on.
type templateRegistry struct {
	mu    sync.RWMutex
	kinds map[string]JobTemplate
}

// TemplateRegistry is the package-level registry every template kind
// (this ticket's six, and AH/S-69.T2's later additions) registers into.
var TemplateRegistry = &templateRegistry{kinds: map[string]JobTemplate{}}

// Register adds or replaces the JobTemplate for kind. Exported and
// additive: a second Register for the same kind replaces the template
// value (last write wins) rather than refusing, so a later ticket may
// swap in a richer implementation for a kind this ticket seeded.
func (r *templateRegistry) Register(kind string, tmpl JobTemplate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kinds[kind] = tmpl
}

// By resolves kind to its registered JobTemplate. An unrecognised kind
// returns ErrUnknownTemplateKind -- never a panic, never a (nil, nil)
// return (this ticket's own acceptance criteria).
func (r *templateRegistry) By(kind string) (JobTemplate, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tmpl, ok := r.kinds[kind]
	if !ok {
		return nil, ErrUnknownTemplateKind
	}
	return tmpl, nil
}

// templateContextKey is the unexported context key TemplateContext is
// stored under, so no other package can collide with or read it by
// accident.
type templateContextKey struct{}

// TemplateContext carries the per-invocation data a template's Resolve
// needs: the node's id, its declared footprint (the ticket footprint
// globs for ImplementTemplate, the target subtree glob for
// IntegrateTemplate, unused by the four read-only kinds), its
// depends_on edges, and the five DECIDED pass-through fields
// (planinput.go's PassThroughFields) the caller supplies verbatim.
type TemplateContext struct {
	ID        string
	Footprint []string
	DependsOn []string

	PassThroughFields
}

// WithTemplateContext returns a copy of ctx carrying tc, for a caller to
// pass into JobTemplate.Resolve.
func WithTemplateContext(ctx context.Context, tc TemplateContext) context.Context {
	return context.WithValue(ctx, templateContextKey{}, tc)
}

// templateContextFrom reads the TemplateContext attached to ctx, if any.
func templateContextFrom(ctx context.Context) (TemplateContext, bool) {
	tc, ok := ctx.Value(templateContextKey{}).(TemplateContext)
	return tc, ok
}

// requireTemplateContext is the shared guard every template_kinds.go
// Resolve calls first: a nil ctx and a ctx with no attached
// TemplateContext are both typed KindInvalidInput refusals, never a
// zero-value DagNode returned as if it were real.
func requireTemplateContext(ctx context.Context) (TemplateContext, error) {
	if ctx == nil {
		return TemplateContext{}, cascade.New(cascade.KindInvalidInput,
			"jobs: template resolve requires a non-nil context")
	}
	tc, ok := templateContextFrom(ctx)
	if !ok {
		return TemplateContext{}, cascade.New(cascade.KindInvalidInput,
			"jobs: template resolve requires a TemplateContext on ctx")
	}
	if tc.ID == "" {
		return TemplateContext{}, cascade.New(cascade.KindInvalidInput,
			"jobs: template context has no id")
	}
	return tc, nil
}

// FootprintClassifier classifies a footprint into a RiskClass -- the
// AC/S-59.T4 classifier dependency this ticket's own HOW-2 names,
// injected via constructor rather than called as a package-level
// function directly, so a template's own tests can substitute a
// synthetic classifier without constructing real probe-root files.
type FootprintClassifier func(footprint []string) RiskClass

// defaultClassifier is the production FootprintClassifier: AC/S-59.T4's
// classifyFootprint (risk.go, same package) over the W6 single-
// repository value. probeRoot is empty: a template resolves before a
// session scope's probe root is available, and an empty root simply
// skips content probes (risk.go's own "absent root skips content
// probes" rule) rather than inventing a new behavior here.
func defaultClassifier(footprint []string) RiskClass {
	return classifyFootprint(footprint, singleRepository(), "")
}

// mergeCapabilities returns base with extra appended, each entry kept
// at most once and base's own order preserved first. Shared by
// template_kinds.go's review/adversarial capability hints so neither
// duplicates a tag the caller already supplied.
func mergeCapabilities(base []string, extra ...string) []string {
	seen := make(map[string]bool, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, c := range base {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	for _, c := range extra {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

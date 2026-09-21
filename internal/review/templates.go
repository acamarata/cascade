// Purpose: the three immutable CR-A/CR-B/CR-C conductor task templates
//   (P1-E25-W5-S52-T4): the fixed task_class/requirements/sensitivity/
//   rubric quadruple each review level dispatches with. "conductor.
//   TaskTemplate" does not exist anywhere in this tree (verified against
//   internal/conductor and pkg/provider before writing this file) -- the
//   ticket text names a producer that was never built, so TaskTemplate
//   below is this package's OWN contract, not an implementation of a
//   nonexistent one (31-CONTINUOUS-BUILD-GUIDE.md: "PLANNED APIs/test
//   names in guidance must be created by their named producer before a
//   consumer assumes them"). It is real: buildModelRequest (provider.go)
//   is TaskTemplate's one production consumer.
// Inputs: the real internal/conductor/task_classes.go taxonomy, read at
//   construction: TaskClassReview for CR-A/CR-B, TaskClassArbitrate for
//   CR-C (06-FORGE-SPEC.md §5.16). Reasoning, context, structured AND
//   sensitivity are taken from the class's own row rather than restated as
//   literals here; the single documented deviation is CR-A's lighter
//   reasoning/context pair, which the ticket names explicitly.
// Outputs: TemplateFor(level) resolves a provider.ReviewCRLevel to its
//   Template; Snapshot() gives templates_test.go a stable, marshalable
//   golden shape to pin against silent drift.
// Constraints: every Template value is built once, in this file, and never
//   mutated after construction -- NewCRATemplate/NewCRBTemplate/
//   NewCRCTemplate each return a fresh value, so a caller that mutates its
//   own copy can never corrupt what the next call returns.
// SPORT: internal/review.templates/ADD (P1-E25-W5-S52-T4).

package review

import (
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TaskTemplate is this package's own per-CR-level conductor dispatch
// template contract: everything buildModelRequest (provider.go) needs to
// turn a BlindRequest into a provider.ModelRequest.
type TaskTemplate interface {
	// TaskClass names the §5.16 task class this level dispatches under.
	TaskClass() conductor.TaskClass
	// Requirements is the requirements triple this level's dispatch sets.
	Requirements() provider.Requirements
	// Sensitivity is the sensitivity tier this level dispatches at: the
	// task class's own SensitivityDefault from the real §5.16 table, never
	// a value below it (CR fix D1). The router, reading the thread's
	// privacy mode off the caller's ctx, is what refuses a lane.
	Sensitivity() provider.SensitivityTier
	// Rubric is the level's fixed review-instruction text, prepended to
	// every dispatch this level makes.
	Rubric() string
}

// Template is TaskTemplate's one concrete, immutable implementation.
// Exported (rather than a package-private struct) so TemplateSnapshot's
// golden test can construct one directly without a constructor detour.
type Template struct {
	level       provider.ReviewCRLevel
	taskClass   conductor.TaskClass
	reasoning   string
	contextK    int
	structured  bool
	sensitivity provider.SensitivityTier
	rubric      string
}

// TaskClass implements TaskTemplate.
func (t Template) TaskClass() conductor.TaskClass { return t.taskClass }

// Requirements implements TaskTemplate. Context is reported in raw tokens
// (contextK * 1000), matching provider.Requirements.Context's own token
// unit -- never the abbreviated "32k" shorthand the ticket prose uses.
func (t Template) Requirements() provider.Requirements {
	return provider.Requirements{
		Reasoning:  t.reasoning,
		Context:    t.contextK * 1000,
		Structured: t.structured,
	}
}

// Sensitivity implements TaskTemplate.
func (t Template) Sensitivity() provider.SensitivityTier { return t.sensitivity }

// Rubric implements TaskTemplate.
func (t Template) Rubric() string { return t.rubric }

// TemplateSnapshot is Template's JSON-marshalable golden shape.
// provider.SensitivityTier has no MarshalJSON (pkg/provider/client.go's own
// header comment records why: the real wire door decodes it as a string
// name), so the snapshot carries String() rather than the numeric tier,
// the same substitution modelExecuteWireParams makes.
type TemplateSnapshot struct {
	Level       string `json:"level"`
	TaskClass   string `json:"task_class"`
	Reasoning   string `json:"reasoning"`
	ContextK    int    `json:"context_k"`
	Structured  bool   `json:"structured"`
	Sensitivity string `json:"sensitivity"`
	Rubric      string `json:"rubric"`
}

// Snapshot returns t's golden, marshalable shape.
func (t Template) Snapshot() TemplateSnapshot {
	return TemplateSnapshot{
		Level:       string(t.level),
		TaskClass:   string(t.taskClass),
		Reasoning:   t.reasoning,
		ContextK:    t.contextK,
		Structured:  t.structured,
		Sensitivity: t.sensitivity.String(),
		Rubric:      t.rubric,
	}
}

// crARubric/crBRubric/crCRubric are each level's fixed review-instruction
// text, per the ticket's own §WHAT description of that level's job.
const (
	crARubric = "CR-A lightweight review: inspect the artifact for per-file or per-hunk defects only. " +
		"Emit structured findings; do not evaluate architecture."
	crBRubric = "CR-B peer review: inspect the full artifact and its context. Emit structured findings " +
		"ranked by severity, each with a verdict."
	crCProposeRubric = "CR-C adversarial review, PROPOSE pass: produce a candidate architecture/security " +
		"verdict over the artifact with supporting findings."
	crCChallengeRubric = "CR-C adversarial review, CHALLENGE pass: attack the PROPOSAL verbatim below. " +
		"Emit a final verdict, a dissent (disagreement with the proposal, if any), and structured findings."
)

// taskClassRow resolves one §5.16 row from the REAL table
// (conductor.TaskClasses()), so no requirement or sensitivity value below is
// a literal restated here. A class absent from the table (impossible while
// task_classes.go's own init holds, but not assumed) fails closed to the
// strictest tier rather than defaulting to a permissive one.
func taskClassRow(class conductor.TaskClass) (conductor.TaskClassRow, bool) {
	for _, row := range conductor.TaskClasses() {
		if row.Class == string(class) {
			return row, true
		}
	}
	return conductor.TaskClassRow{Class: string(class), Reasoning: "max", CtxK: 200,
		Structured: true, SensitivityDefault: provider.SensitivityLocalOnly}, false
}

// newTemplateFromRow builds a Template whose requirements AND sensitivity
// come from the class's own §5.16 row. The sensitivity tier is therefore the
// class default (restricted for review and arbitrate alike) and is never set
// below it -- the CR fix D1 rule: the earlier draft dispatched CR-A/CR-B at
// `internal`, an explicit downgrade of the review row's own default on every
// review dispatch.
func newTemplateFromRow(level provider.ReviewCRLevel, class conductor.TaskClass, rubric string) Template {
	row, _ := taskClassRow(class)
	return Template{
		level: level, taskClass: class,
		reasoning: row.Reasoning, contextK: row.CtxK, structured: row.Structured,
		sensitivity: row.SensitivityDefault, rubric: rubric,
	}
}

// NewCRATemplate builds the CR-A template: task_class=review, sensitivity
// from the review row, and {reasoning: medium, context: 32k} -- the ONE
// documented deviation from that row, named by the ticket's own §WHAT text
// ("CR-A (lightweight): requirements = {reasoning: medium, context: 32k}...
// Cheap-lane affinity"). The deviation is downward on cost only; the
// sensitivity tier is the row's and is never lightened.
func NewCRATemplate() Template {
	t := newTemplateFromRow(provider.ReviewCRLevelA, conductor.TaskClassReview, crARubric)
	t.reasoning, t.contextK = "medium", 32
	return t
}

// NewCRBTemplate builds the CR-B template: task_class=review, with
// reasoning, context, structured and sensitivity all taken from the review
// row (high / 200k / true / restricted).
func NewCRBTemplate() Template {
	return newTemplateFromRow(provider.ReviewCRLevelB, conductor.TaskClassReview, crBRubric)
}

// NewCRCTemplate builds the CR-C PROPOSE-pass template: task_class=
// arbitrate, with everything from the arbitrate row (max / 200k / true /
// restricted). Restricted is "restricted-capable" in §5.16's own wording --
// the router, not this template, resolves the permitted lane (K/S-22.T3).
func NewCRCTemplate() Template {
	return newTemplateFromRow(provider.ReviewCRLevelC, conductor.TaskClassArbitrate, crCProposeRubric)
}

// TemplateFor resolves level to its Template, or a typed error for an
// invalid level. Every provider.ReviewCRLevel member is listed explicitly
// (exhaustive lint, default-signifies-exhaustive: false).
func TemplateFor(level provider.ReviewCRLevel) (Template, error) {
	switch level {
	case provider.ReviewCRLevelA:
		return NewCRATemplate(), nil
	case provider.ReviewCRLevelB:
		return NewCRBTemplate(), nil
	case provider.ReviewCRLevelC:
		return NewCRCTemplate(), nil
	default:
		return Template{}, cascade.Newf(cascade.KindInvalidInput, "internal/review: invalid review level %q", level)
	}
}

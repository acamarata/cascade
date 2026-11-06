package generate

// Purpose: one generation pass over a repository: register, build the
//   policy view, render the four legs through one Writer and record the
//   manifest.
// Inputs: a repository root and RunDeps (clock, attention sink, register,
//   fact source, workflows probe).
// Outputs: a Result naming every file written, adopted or refused, in leg
//   order (rules, agents/skills, workflows, scoped harness), and the
//   workflows deferral when that leg did not render.
// Constraints: every Rendered path must sit in GeneratedLocations before it
//   is applied. The manifest is written at most once, and only when it
//   differs from the prior one apart from generated_at, so a rerun over an
//   unchanged tree writes nothing.
// SPORT: context-engine/generate-run (ADD, P1-GEN-11).

import (
	"bytes"
	"context"

	"github.com/acamarata/cascade/internal/context/policy"
	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// kernelTokenBudget is the most tokens the execution kernel may take.
const kernelTokenBudget = 120

// RunDeps is everything Run needs from outside the package. Every field is
// required.
type RunDeps struct {
	// Clock stamps the manifest's generated_at.
	Clock runtime.Clock
	// Attention receives every refusal the Writer makes.
	Attention AttentionSink
	// Register scans root and returns its inventory.
	Register func(ctx context.Context, root string) (repo.Inventory, error)
	// Facts answers the accepted facts for the policy view.
	Facts FactSource
	// Probe negotiates harness workflows support.
	Probe WorkflowsSupportProbe
}

// Result is what one Run did, in leg order.
type Result struct {
	// Written and Adopted are the paths Apply wrote or adopted.
	Written, Adopted []string
	// Refused is every Outcome that carries a refusal reason.
	Refused []Outcome
	// Deferral is the workflows leg's deferral, nil when it rendered.
	Deferral *Deferral
}

// Run generates every managed file under repoRoot and records the
// manifest. A nil RunDeps field or an empty repoRoot is KindInvalidInput
// naming it; an over-budget kernel, a rendered path outside the generated
// locations or a manifest the location lint rejects is KindIntegrity.
func Run(ctx context.Context, repoRoot string, d RunDeps) (Result, error) {
	if err := validateRun(repoRoot, d); err != nil {
		return Result{}, err
	}
	if n := policy.TokenCount(policy.KernelText); n > kernelTokenBudget {
		return Result{}, cascade.Newf(cascade.KindIntegrity, "generate: policy kernel is %d tokens, over the %d budget", n, kernelTokenBudget)
	}
	prior, hadPrior, err := ReadManifest(repoRoot)
	if err != nil {
		return Result{}, err
	}
	inv, err := d.Register(ctx, repoRoot)
	if err != nil {
		return Result{}, err
	}
	v, err := BuildView(ctx, inv, repoRoot, d.Facts)
	if err != nil {
		return Result{}, err
	}
	w, err := NewWriter(repoRoot, d.Attention, prior, hadPrior)
	if err != nil {
		return Result{}, err
	}
	var res Result
	if err := runLegs(ctx, w, v, d.Probe, &res); err != nil {
		return res, err
	}
	var deferrals []Deferral
	if res.Deferral != nil {
		deferrals = []Deferral{*res.Deferral}
	}
	return res, publishManifest(repoRoot, w.Manifest(d.Clock.Now(), deferrals), prior, hadPrior)
}

// validateRun names the first missing input.
func validateRun(repoRoot string, d RunDeps) error {
	missingField := ""
	switch {
	case repoRoot == "":
		return cascade.New(cascade.KindInvalidInput, "generate: empty repoRoot")
	case d.Clock == nil:
		missingField = "Clock"
	case d.Attention == nil:
		missingField = "Attention"
	case d.Register == nil:
		missingField = "Register"
	case d.Facts == nil:
		missingField = "Facts"
	case d.Probe == nil:
		missingField = "Probe"
	default:
		return nil
	}
	return cascade.Newf(cascade.KindInvalidInput, "generate: nil RunDeps.%s", missingField)
}

// runLegs renders and applies the four legs in order.
func runLegs(ctx context.Context, w *Writer, v View, p WorkflowsSupportProbe, res *Result) error {
	rules, err := RenderRules(v)
	if err != nil {
		return err
	}
	if err := applyLeg(ctx, w, rules, res); err != nil {
		return err
	}
	if err := applyAgentsSkills(ctx, w, v, res); err != nil {
		return err
	}
	leg, err := RenderWorkflows(ctx, v, p)
	if err != nil {
		return err
	}
	res.Deferral = leg.Deferral
	if err := applyLeg(ctx, w, leg.Files, res); err != nil {
		return err
	}
	scoped, err := RenderScopedHarness(v)
	if err != nil {
		return err
	}
	return applyLeg(ctx, w, scoped, res)
}

// applyAgentsSkills renders the agents and skills and writes them through
// WriteAgentsSkills.
func applyAgentsSkills(ctx context.Context, w *Writer, v View, res *Result) error {
	rs, err := RenderAgentsSkills(v)
	if err != nil {
		return err
	}
	if err := checkLocations(rs); err != nil {
		return err
	}
	outs, err := WriteAgentsSkills(ctx, w, rs)
	for _, o := range outs {
		res.record(o)
	}
	return err
}

// applyLeg applies each Rendered in order after the location check.
func applyLeg(ctx context.Context, w *Writer, rs []Rendered, res *Result) error {
	if err := checkLocations(rs); err != nil {
		return err
	}
	for _, r := range rs {
		o, err := w.Apply(ctx, r)
		if err != nil {
			return err
		}
		res.record(o)
	}
	return nil
}

// checkLocations refuses any rendered path outside GeneratedLocations.
func checkLocations(rs []Rendered) error {
	for _, r := range rs {
		if !inGeneratedLocations(r.Path) {
			return cascade.Newf(cascade.KindIntegrity, "generate: rendered %s is outside the generated locations", r.Path)
		}
	}
	return nil
}

// record files one Outcome under Written, Adopted or Refused.
func (res *Result) record(o Outcome) {
	switch {
	case o.Reason != "":
		res.Refused = append(res.Refused, o)
	case o.Written:
		res.Written = append(res.Written, o.Path)
	case o.Adopted:
		res.Adopted = append(res.Adopted, o.Path)
	}
}

// publishManifest lints next and writes it when it differs from prior
// apart from generated_at.
func publishManifest(repoRoot string, next, prior Manifest, hadPrior bool) error {
	if items := LintLocations(next); len(items) > 0 {
		return cascade.Newf(cascade.KindIntegrity, "generate: manifest entry %s is %s", items[0].Path, items[0].Condition)
	}
	if hadPrior {
		same, err := sameManifest(next, prior)
		if err != nil || same {
			return err
		}
	}
	return WriteManifest(repoRoot, next)
}

// sameManifest compares the canonical bytes of a and b with generated_at
// ignored.
func sameManifest(a, b Manifest) (bool, error) {
	a.GeneratedAt, b.GeneratedAt = "", ""
	ab, err := encodeManifest(a)
	if err != nil {
		return false, err
	}
	bb, err := encodeManifest(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(ab, bb), nil
}

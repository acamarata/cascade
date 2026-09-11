// Purpose: `cascade policy risk explain <path...>` (19 §Epic AH S-69.T1,
//
//	R-16.70(b)) — classifies the R-21.182 union footprint through
//	AC/S-59.T4's own classifier and reports the resolved risk class, the
//	AH/S-69.T1 tightening-only effective gate set, and each gate's
//	provenance (table default vs `[policy.risk_gates]` overlay).
//
// Inputs: --pre/--post/--symbol-reach path lists plus bare positional
//
//	paths (which count as both pre- and post-image), and the locally
//	loaded config.toml's [policy.risk_gates] overlay.
//
// Outputs: process output through internal/output.Writer, all derived
//
//	from explainRiskFootprint, the one function that makes the
//	decision.
//
// Constraints: this verb runs entirely locally (no daemon dial): risk
//
//	classification is a pure computation over a footprint and a config
//	overlay, unlike every other `policy` verb (which evaluates through
//	the daemon's one Engine) — there is no engine state this verb
//	needs.
//
//	NO JSON-RPC MIRROR SHIPS (contract deviation, disclosed in this
//	ticket's journal): the ticket contract asks for a `policy.risk_explain`
//	mirror registered in internal/policy/rpc.go, but internal/policy
//	cannot import internal/jobs (internal/jobs -> internal/conductor ->
//	internal/hooks/egress -> internal/secrets -> internal/policy, an
//	import cycle), so the handler that would call
//	jobs.ClassifyFootprint/EffectiveGateSet cannot live where
//	internal/policy's MethodHandlers builds its map. R-21.207's own verb
//	registry (internal/policy/verbs.go) is a CLOSED set whose own test
//	(TestMethodHandlersCoverEveryRegisteredVerb) asserts the handler map
//	and the registry name the SAME set — registering the verb there with
//	no handler in that map fails that invariant, and merging an
//	unregistered handler into the daemon's method map from cmd/cascade
//	would skip the Authorize middleware every other RPC verb passes
//	through, which is a real authorization gap on a public daemon
//	surface, not a shortcut worth taking to tick a box. The CLI below is
//	real, complete and useful on its own; the RPC mirror is left
//	unshipped rather than shipped unauthenticated or shipped dead.
//
// SPORT: cli/policy-risk-explain/ADD (P1-E34-W7-S69-T1).
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// policyRiskDeps carries the inputs newPolicyRiskExplainCmd needs to
// load config.toml locally, mirroring statusDeps's injection pattern
// (Art.7.1: never touch $HOME/the environment directly from a RunE).
type policyRiskDeps struct {
	Paths   runtime.PathProvider
	Getenv  runtime.Getenv
	Environ func() []string
}

// productionPolicyRiskDeps is the real environment.
func productionPolicyRiskDeps() policyRiskDeps {
	return policyRiskDeps{Paths: lazyPaths{}, Getenv: os.Getenv, Environ: os.Environ}
}

// newPolicyRiskCmd builds the `risk` subtree mounted under the existing
// `policy` noun (policy.go's newPolicyCmd).
func newPolicyRiskCmd() *cobra.Command {
	risk := &cobra.Command{
		Use:   "risk",
		Short: "Inspect the risk-class gate policy",
	}
	risk.AddCommand(newPolicyRiskExplainCmd(productionPolicyRiskDeps()))
	return risk
}

// newPolicyRiskExplainCmd builds `policy risk explain <path...>`.
func newPolicyRiskExplainCmd(deps policyRiskDeps) *cobra.Command {
	var pre, post, symbolReach []string
	cmd := &cobra.Command{
		Use:   "explain [--pre <path...>] [--post <path...>] [--symbol-reach <path...>] <path...>",
		Short: "Report the risk class and effective gate set for a change footprint",
		Long: "Classify the R-21.182 union footprint (pre-image, post-image and symbol-\n" +
			"reachability paths) through the AC/S-59.T4 classifier, and report the\n" +
			"resolved risk class, the AH/S-69.T1 effective gate set, and each gate's\n" +
			"provenance: the table default, or the [policy.risk_gates] overlay.",
		Example: "  cascade policy risk explain internal/secrets/scrub.go\n" +
			"  cascade policy risk explain --pre old/path.go --post new/path.go",
		Args: usageArgs(cobra.MinimumNArgs(0)),
		RunE: func(cmd *cobra.Command, args []string) error {
			footprint := unionFootprintPaths(args, pre, post, symbolReach)
			if len(footprint) == 0 {
				return cascade.New(cascade.KindInvalidInput,
					"policy risk explain: at least one path is required (positional, --pre, --post or --symbol-reach)")
			}
			overlay, err := loadRiskGateOverlay(cmd.Context(), deps)
			if err != nil {
				return err
			}
			result, err := explainRiskFootprint(footprint, overlay)
			if err != nil {
				return err
			}
			return approvalOutputWriter(cmd).Result(policyRiskExplainView(result))
		},
	}
	cmd.Flags().StringArrayVar(&pre, "pre", nil, "a pre-image path, repeatable")
	cmd.Flags().StringArrayVar(&post, "post", nil, "a post-image path, repeatable")
	cmd.Flags().StringArrayVar(&symbolReach, "symbol-reach", nil, "a symbol-reachability path, repeatable")
	return cmd
}

// unionFootprintPaths implements the R-21.182 union: bare positional
// paths count as BOTH pre-image and post-image, so they need no special
// treatment beyond joining every list; --pre/--post/--symbol-reach add
// their own. De-duplicated, order-stable on first occurrence.
func unionFootprintPaths(positional, pre, post, symbolReach []string) []string {
	seen := make(map[string]bool, len(positional)+len(pre)+len(post)+len(symbolReach))
	out := make([]string, 0, len(positional)+len(pre)+len(post)+len(symbolReach))
	for _, group := range [][]string{positional, pre, post, symbolReach} {
		for _, p := range group {
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// loadRiskGateOverlay loads config.toml locally and decodes
// [policy.risk_gates] into a typed jobs.RiskGateOverlay. A missing
// config.toml or an absent [policy] section is not an error here (the
// empty overlay); a malformed one is.
func loadRiskGateOverlay(ctx context.Context, deps policyRiskDeps) (jobs.RiskGateOverlay, error) {
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{
		Path:    deps.Paths.ConfigPath(),
		Getenv:  deps.Getenv,
		Environ: deps.Environ,
	})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "policy risk explain: load config.toml")
	}
	parsed, err := policy.ParseConfig(cfg.Extra)
	if err != nil {
		return nil, err
	}
	return jobs.BuildRiskGateOverlay(parsed.RiskGates)
}

// gateProvenance names one effective gate and where it came from.
type gateProvenance struct {
	Gate       string `json:"gate"`
	Provenance string `json:"provenance"`
}

// policyRiskExplainResult is explainRiskFootprint's output -- the SAME
// value both the CLI RunE above and the RPC mirror
// (daemon_unix_policy.go) render.
type policyRiskExplainResult struct {
	RiskClass  string           `json:"risk_class"`
	Paths      []string         `json:"paths"`
	Gates      []gateProvenance `json:"gates"`
	Floor      bool             `json:"floor"`
	FloorPaths []string         `json:"floor_paths,omitempty"`
}

// explainRiskFootprint is the ONE decision path `policy risk explain`
// and its RPC mirror both call: classify footprint through
// jobs.ClassifyFootprint (AC/S-59.T4's classifier, unchanged), resolve
// the AH/S-69.T1 effective gate set over ov, and report each gate's
// provenance plus the R-21.182 Critical floor's matching paths when it
// resolved the class. probeRoot is empty: this surface has no
// SessionScope to read content probes against, matching S-60.T2's
// templates' own "a template resolves before a session scope's probe
// root is available" posture -- a path-rule/floor match still applies,
// only content-marker probes do not.
func explainRiskFootprint(footprint []string, ov jobs.RiskGateOverlay) (policyRiskExplainResult, error) {
	class := jobs.ClassifyFootprint(footprint, "")
	effective, err := jobs.EffectiveGateSet(class, ov)
	if err != nil {
		return policyRiskExplainResult{}, err
	}
	tableDefault, err := jobs.GateSetForRiskClass(class)
	if err != nil {
		return policyRiskExplainResult{}, err
	}
	fromTable := make(map[string]bool, len(tableDefault))
	for _, g := range tableDefault {
		fromTable[string(g)] = true
	}

	gates := make([]gateProvenance, 0, len(effective))
	for _, g := range effective {
		provenance := "overlay"
		if fromTable[string(g)] {
			provenance = "table"
		}
		gates = append(gates, gateProvenance{Gate: string(g), Provenance: provenance})
	}

	var floorPaths []string
	for _, p := range footprint {
		if _, ok := jobs.CriticalFloorMatch(p); ok {
			floorPaths = append(floorPaths, p)
		}
	}
	sort.Strings(floorPaths)

	return policyRiskExplainResult{
		RiskClass:  string(class),
		Paths:      append([]string{}, footprint...),
		Gates:      gates,
		Floor:      len(floorPaths) > 0,
		FloorPaths: floorPaths,
	}, nil
}

// policyRiskExplainView renders the result as a table.
type policyRiskExplainView policyRiskExplainResult

// String renders the class, the floor line (when it applied) and the
// gate table with its provenance column.
func (v policyRiskExplainView) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "risk_class\t%s\n", v.RiskClass)
	if v.Floor {
		_, _ = fmt.Fprintf(tw, "floor\tcritical (%s)\n", strings.Join(v.FloorPaths, ", "))
	}
	_, _ = fmt.Fprintf(tw, "\nGATE\tPROVENANCE\n")
	for _, g := range v.Gates {
		_, _ = fmt.Fprintf(tw, "%s\t%s\n", g.Gate, g.Provenance)
	}
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

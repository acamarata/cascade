package main

import (
	"sort"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: --require's parsing, across its two namespaces — the closed
//
//	three-key LANE vocabulary and the open, machine-advertised NODE
//	vocabulary behind a `node.` prefix.
//
// Inputs: --require's k=v pairs.
// Outputs: a validated runRequirements, or a client-side refusal.
// Constraints: split out of run.go to stay under Art.10.3's 300-line cap.
//
//	Validation is EXTENDED here, never loosened: an unrecognized lane key
//	is still a refusal, and the node namespace refuses a blank capability,
//	a non-boolean value, and `=false`.
//
// SPORT: cmd/cascade:run-requirements (ADD) — P1-E17-W4-S37-T2.

// nodeRequirePrefix is the reserved namespace for node-capability
// requirements on --require.
const nodeRequirePrefix = "node."

// buildRequirements maps --require's k=v pairs onto runRequirements.
func buildRequirements(kv map[string]string) (runRequirements, error) {
	var r runRequirements
	for k, v := range kv {
		if capability, ok := strings.CutPrefix(k, nodeRequirePrefix); ok {
			if err := r.addNodeCapability(capability, v); err != nil {
				return r, err
			}
			continue
		}
		var err error
		switch k {
		case "reasoning":
			r.Reasoning = v
		case "context":
			r.Context, err = strconv.Atoi(v)
		case "structured":
			r.Structured, err = strconv.ParseBool(v)
		default:
			return r, cascade.Newf(cascade.KindInvalidInput, "cascade run: --require key %q is not valid (valid: reasoning, context, structured, or node.<capability>)", k)
		}
		if err != nil {
			return r, cascade.Wrapf(cascade.KindInvalidInput, err, "cascade run: --require %s=%s", k, v)
		}
	}
	sort.Strings(r.NodeCapabilities)
	return r, nil
}

// addNodeCapability records one `node.<capability>=<bool>` requirement.
//
// Only true is accepted. "Require the ABSENCE of a capability" is not a
// thing placement can honour — a node that does not report a capability is
// simply one that does not have it — so `node.x=false` is refused rather
// than silently ignored, which would leave a user believing they had
// constrained placement when they had not.
func (r *runRequirements) addNodeCapability(capability, raw string) error {
	if strings.TrimSpace(capability) == "" {
		return cascade.New(cascade.KindInvalidInput,
			"cascade run: --require node. needs a capability name after the prefix")
	}
	required, err := strconv.ParseBool(raw)
	if err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err,
			"cascade run: --require %s%s=%s (want true)", nodeRequirePrefix, capability, raw)
	}
	if !required {
		return cascade.Newf(cascade.KindInvalidInput,
			"cascade run: --require %s%s=false is not meaningful; placement cannot require a capability to be absent",
			nodeRequirePrefix, capability)
	}
	r.NodeCapabilities = append(r.NodeCapabilities, capability)
	return nil
}

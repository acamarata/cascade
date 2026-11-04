package main

// Purpose: admit hosted allow and workflow through the config baseline.
// Inputs: [ci.hosted]. Outputs: canonical family values and loosening paths.
// Constraints: false by default; workflow is a bare filename; no egress setup.
// SPORT: cmd/cascade guarded ci.hosted (P1-NODE-09).

import (
	"bytes"
	"encoding/json"
	"maps"
	"regexp"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const defaultHostedWorkflow = "cascade-ci.yml"

var hostedWorkflowFilename = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

type ciHostedValue struct {
	Allow    bool   `json:"allow"`
	Workflow string `json:"workflow"`
}

func init() {
	runtime.RegisterGuardedFamily(runtime.GuardedFamily{
		Section: "ci.hosted", Keys: []string{"ci.hosted.allow", "ci.hosted.workflow"},
		Extract: extractCIHostedFamily, Compare: compareCIHostedFamily, Apply: applyCIHostedFamily,
	})
}

func validHostedWorkflow(name string) bool {
	return name != "." && name != ".." && hostedWorkflowFilename.MatchString(name)
}

func extractCIHostedFamily(extra map[string]interface{}) (json.RawMessage, error) {
	v := ciHostedValue{Workflow: defaultHostedWorkflow}
	parent := extra
	for _, key := range []string{"ci", "hosted"} {
		raw, exists := parent[key]
		if !exists {
			return nil, nil
		}
		var ok bool
		parent, ok = raw.(map[string]interface{})
		if !ok {
			return nil, cascade.New(cascade.KindInvalidInput, "ci.hosted must be a table")
		}
	}
	for key, raw := range parent {
		var ok bool
		switch key {
		case "allow":
			v.Allow, ok = raw.(bool)
		case "workflow":
			v.Workflow, ok = raw.(string)
		default:
			return nil, cascade.New(cascade.KindInvalidInput, "ci.hosted: unknown key")
		}
		if !ok {
			return nil, cascade.Newf(cascade.KindInvalidInput, "ci.hosted.%s has an invalid type", key)
		}
	}
	if !validHostedWorkflow(v.Workflow) {
		return nil, cascade.New(cascade.KindInvalidInput, "ci.hosted.workflow must be a bare file name")
	}
	if !v.Allow && v.Workflow == defaultHostedWorkflow {
		return nil, nil
	}
	return json.Marshal(v)
}

func decodeCIHostedFamily(raw json.RawMessage) (ciHostedValue, error) {
	v := ciHostedValue{Workflow: defaultHostedWorkflow}
	if len(raw) != 0 {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&v); err != nil {
			return v, cascade.Wrap(cascade.KindInvalidInput, err, "ci.hosted: invalid family value")
		}
	}
	if !validHostedWorkflow(v.Workflow) {
		return v, cascade.New(cascade.KindInvalidInput, "ci.hosted.workflow must be a bare file name")
	}
	return v, nil
}

func compareCIHostedFamily(base, next json.RawMessage) ([]runtime.LooseningPath, error) {
	b, err := decodeCIHostedFamily(base)
	if err != nil {
		return nil, err
	}
	n, err := decodeCIHostedFamily(next)
	if err != nil {
		return nil, err
	}
	var paths []runtime.LooseningPath
	if !b.Allow && n.Allow {
		paths = append(paths, runtime.LooseningPath{Family: "ci.hosted", Key: "ci.hosted.allow",
			Current: b.Allow, Proposed: n.Allow, Reason: "enables hosted execution"})
	}
	if b.Workflow != n.Workflow {
		paths = append(paths, runtime.LooseningPath{Family: "ci.hosted", Key: "ci.hosted.workflow",
			Current: b.Workflow, Proposed: n.Workflow, Reason: "changes the dispatched workflow"})
	}
	return paths, nil
}

func applyCIHostedFamily(extra map[string]interface{}, raw json.RawMessage) {
	v, err := decodeCIHostedFamily(raw)
	if err != nil {
		v = ciHostedValue{Workflow: defaultHostedWorkflow}
	}
	ci, _ := extra["ci"].(map[string]interface{})
	ci = maps.Clone(ci)
	if ci == nil {
		ci = map[string]interface{}{}
	}
	ci["hosted"] = map[string]interface{}{"allow": v.Allow, "workflow": v.Workflow}
	extra["ci"] = ci
}

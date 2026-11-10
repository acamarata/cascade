// Purpose: the agent_lanes doctor check - each [agents.lanes.<driver>]
// lane with its harness, posture, credential class and entitlement.
// Inputs: the effective config.toml through loadDoctorConfig.
// Outputs: a read-only doctor check; StatusWarn when a lane lacks a
// programmatic entitlement or names an unregistered harness.
// Constraints: never prints a credential value, a key_env name or an
// auth_source path; reads no environment, vault or auth file.
// SPORT: cli.doctor.agents/ADD (P1-AGT-08).
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/runtime"
)

// agentLanesCheck reports the configured agent lanes.
type agentLanesCheck struct{ paths runtime.PathProvider }

// newAgentLanesDoctorCheck builds the agent_lanes check over paths.
func newAgentLanesDoctorCheck(paths runtime.PathProvider) doctor.Check {
	return agentLanesCheck{paths: paths}
}

// Name identifies the agent lanes check.
func (agentLanesCheck) Name() string { return "agent_lanes" }

// Describe states what the check measures.
func (agentLanesCheck) Describe() string {
	return "Report each configured agent lane's harness, posture and credential class"
}

// Metadata marks the check as diagnostic only.
func (agentLanesCheck) Metadata() doctor.CheckMeta { return doctor.CheckMeta{} }

// Fix refuses changes because lane posture requires operator judgment.
func (agentLanesCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}

// Run lists every lane; a config that will not parse is a StatusError.
func (c agentLanesCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	cfg, err := loadDoctorConfig(ctx, c.paths)
	if err != nil {
		return doctor.CheckResult{Status: doctor.StatusError, Message: "config could not be loaded"}, nil
	}
	lanes, err := runtime.ParseAgentsConfig(cfg.Extra)
	if err != nil {
		return doctor.CheckResult{Status: doctor.StatusError, Message: err.Error()}, nil
	}
	if len(lanes.Lanes) == 0 {
		return doctor.CheckResult{Status: doctor.StatusOK, Message: "no agent lanes configured"}, nil
	}
	bindings := agents.Bindings()
	auth, err := agents.ParseDriverAuth(cfg.Extra, bindings)
	if err != nil {
		return doctor.CheckResult{Status: doctor.StatusError, Message: err.Error()}, nil
	}
	return agentLanesResult(lanes.Lanes, auth, bindings), nil
}

// agentLanesResult renders one line per lane in lane order; only the
// credential class is shown, never a key source or path.
func agentLanesResult(lanes []runtime.AgentLaneConfig, auth map[agents.HarnessID]agents.DriverAuth, bindings []agents.Binding) doctor.CheckResult {
	res := doctor.CheckResult{Status: doctor.StatusOK}
	parts := make([]string, 0, len(lanes))
	for _, lane := range lanes {
		harness, class := lane.Driver, "unset"
		registered := false
		for _, b := range bindings {
			if string(b.Harness) == lane.Driver {
				registered = true
				break
			}
		}
		if !registered {
			harness = "unregistered"
		}
		if a, ok := auth[agents.HarnessID(lane.Driver)]; ok {
			class = a.Class
		}
		line := fmt.Sprintf("%s%s harness=%s enabled=%t posture=%s class=%s",
			agents.LanePrefix, lane.Driver, harness, lane.Enabled, lane.Posture, class)
		if !lane.ProgrammaticEntitlement() {
			line += " (warning: ProgrammaticEntitlement false)"
			res.Status = doctor.StatusWarn
		}
		if !registered {
			line += " (warning: harness not registered)"
			res.Status = doctor.StatusWarn
		}
		parts = append(parts, line)
	}
	res.Message = strings.Join(parts, "; ")
	return res
}

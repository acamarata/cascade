// Purpose: the coordinated `cascade node upgrade [--all]` rollout (07
//   §node) and its "node.upgrade" RPC handler — an elevated verb (06
//   §5.14, already listed in internal/rpc's canonical elevationTable,
//   always:true) that drives Provision (provision.go) against one or
//   every enrolled node, reporting per-node outcomes.
// Inputs: an UpgradeRequest (a single node id, or All), the enrolled
//   RecordStore, and ProvisionDeps.
// Outputs: one UpgradeOutcome per targeted node — never a single
//   all-or-nothing result: a node unreachable mid-rollout is reported and
//   the rollout continues to the remaining nodes (§5.9's per-node delta
//   reporting).
// Constraints: A NODE IS NEVER LEFT RUNNING AN UNVERIFIED BINARY —
//   RunUpgrade delegates every per-node decision to Provision, which
//   fails closed on signature/checksum failure and only ever installs
//   via an atomic rename (see provision.go). A node with no configured
//   Route (travel.go) is reported Skipped with a reason, never treated
//   as a silent success. RegisterUpgradeHandler mounts the RPC method;
//   elevation enforcement itself is internal/rpc's shared
//   ElevationMiddleware consulting elevationTable — this file does not
//   re-implement that gate (S-36.T1's enroll.go sets this precedent).
// SPORT: internal/nodes RunUpgrade/ADDED, RegisterUpgradeHandler/ADDED
//   (P1-E17-W4-S36-T5).

package nodes

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// UpgradeRequest selects the rollout's targets: either NodeID alone, or
// All (every enrolled, non-drained node with a configured Route).
type UpgradeRequest struct {
	NodeID string `json:"node_id,omitempty"`
	All    bool   `json:"all,omitempty"`
}

// UpgradeOutcome reports one node's rollout result. Exactly one of
// Installed/Skipped/Err is meaningful per outcome.
type UpgradeOutcome struct {
	NodeID          string `json:"node_id"`
	Installed       bool   `json:"installed"`
	Skipped         bool   `json:"skipped"`
	PreviousVersion string `json:"previous_version,omitempty"`
	NewVersion      string `json:"new_version,omitempty"`
	// Reason explains a Skipped outcome that is not a version-match
	// convergence (e.g. "no configured ssh/VPN route").
	Reason string `json:"reason,omitempty"`
	// Err carries a failed node's refusal message. A non-empty Err means
	// Installed and Skipped are both false: the rollout could not act on
	// this node and did NOT touch its installed binary.
	Err string `json:"err,omitempty"`
	// SkewWarning reports (§D-33) that PreviousVersion, before this
	// rollout ran, would already have fallen out of this controller's
	// same-minor window — advisory, never a refusal, surfaced even on a
	// successful install so an operator sees a node that had drifted.
	SkewWarning bool `json:"skew_warning,omitempty"`
}

// UpgradeDeps carries RunUpgrade's collaborators.
type UpgradeDeps struct {
	Store     *RecordStore
	Provision ProvisionDeps
	Artifact  Artifact
}

// RunUpgrade resolves req's targets from deps.Store and runs Provision
// against each, collecting one UpgradeOutcome per node. A per-node
// failure never stops the rollout; the only error this function itself
// returns is a systemic one (the record store could not be read at all).
func RunUpgrade(ctx context.Context, req UpgradeRequest, deps UpgradeDeps) ([]UpgradeOutcome, error) {
	targets, err := resolveUpgradeTargets(req, deps.Store)
	if err != nil {
		return nil, err
	}
	outcomes := make([]UpgradeOutcome, 0, len(targets))
	for _, rec := range targets {
		outcomes = append(outcomes, upgradeOne(ctx, rec, deps))
	}
	return outcomes, nil
}

// resolveUpgradeTargets reads deps.Store and narrows to req's selection:
// a single named node (KindNotFound if absent) or every non-drained
// enrolled node (All).
func resolveUpgradeTargets(req UpgradeRequest, store *RecordStore) ([]DeviceRecord, error) {
	if !req.All {
		rec, err := store.Get(req.NodeID)
		if err != nil {
			return nil, err
		}
		return []DeviceRecord{rec}, nil
	}
	all, err := store.List()
	if err != nil {
		return nil, err
	}
	targets := make([]DeviceRecord, 0, len(all))
	for _, rec := range all {
		if !rec.Drained {
			targets = append(targets, rec)
		}
	}
	return targets, nil
}

// upgradeOne runs Provision against a single node record, converting
// every outcome (skip, install, or refusal) into an UpgradeOutcome — this
// function never returns an error itself, since one node's refusal must
// never abort the rollout for the rest.
func upgradeOne(ctx context.Context, rec DeviceRecord, deps UpgradeDeps) UpgradeOutcome {
	if !rec.Route.configured() {
		return UpgradeOutcome{NodeID: rec.NodeID, Skipped: true, Reason: "no configured ssh/VPN route"}
	}
	target := Target{NodeID: rec.NodeID, User: rec.Route.User, Addr: rec.Route.Addr}
	result, err := Provision(ctx, target, deps.Artifact, deps.Provision)
	if err != nil {
		return UpgradeOutcome{NodeID: rec.NodeID, Err: err.Error()}
	}
	warn := deps.Provision.ControllerVersion != "" && result.PreviousVersion != "" &&
		WouldFallOutOfWindow(deps.Provision.ControllerVersion, result.PreviousVersion)
	return UpgradeOutcome{
		NodeID: rec.NodeID, Installed: result.Installed, Skipped: result.Skipped,
		PreviousVersion: result.PreviousVersion, NewVersion: result.NewVersion,
		SkewWarning: warn,
	}
}

// upgradeHandlerDeps resolves an UpgradeDeps for one RPC call. Injected
// as a func rather than a fixed value so the composition root can build
// fresh ProvisionDeps/Artifact per call without this file importing the
// daemon's config surface.
type upgradeHandlerDeps func(ctx context.Context) (UpgradeDeps, error)

// RegisterUpgradeHandler mounts "node.upgrade" on registry, mirroring
// enroll.go's RegisterHandlers precedent exactly: this file registers the
// handler internal/rpc dispatches to once its shared ElevationMiddleware
// clears the call (elevationTable already lists "node.upgrade",
// always:true) — it does not re-implement elevation gating.
func RegisterUpgradeHandler(registry *rpc.Registry, resolve upgradeHandlerDeps) {
	registry.Register("node.upgrade", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req UpgradeRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, cascade.Wrap(cascade.KindInvalidInput, err, "nodes: node.upgrade: decode request")
			}
		}
		if !req.All && req.NodeID == "" {
			return nil, cascade.New(cascade.KindInvalidInput, "nodes: node.upgrade requires node_id or all=true")
		}
		deps, err := resolve(ctx)
		if err != nil {
			return nil, err
		}
		return RunUpgrade(ctx, req, deps)
	})
}

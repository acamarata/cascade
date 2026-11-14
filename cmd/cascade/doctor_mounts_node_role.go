// Purpose: report this home's controller role in `cascade doctor`.
// Inputs: the path provider; the file-backed controller binding, which
//
//	carries the designation backend of the same data directory.
//
// Outputs: the controller_role check: ok for controller or node, error
//
//	naming the RoleConflict kind and its remedy otherwise.
//
// Constraints: read-only. The role comes from nodes.RoleStatus, the
//
//	resolver the daemon uses, so doctor and the daemon never disagree;
//	no fallback ever reports controller from an absent record.
//
// SPORT: cli.doctor.controller_role/ADD.

package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
)

type controllerRoleCheck struct{ paths runtime.PathProvider }

// Name identifies the controller role check.
func (controllerRoleCheck) Name() string { return "controller_role" }

// Describe states what the check measures.
func (controllerRoleCheck) Describe() string {
	return "Resolve this home's controller role from its designation or binding"
}

// Metadata marks the check as diagnostic only.
func (controllerRoleCheck) Metadata() doctor.CheckMeta { return doctor.CheckMeta{} }

// Fix refuses: a designation is written only by `cascade init` (or the
// daemon's first-start bootstrap) and a binding only by `cascade node
// bind`, never by doctor.
func (controllerRoleCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}

// Run resolves the role. Any RoleConflict is an error whose message names
// the kind and whose remediation names the verb that settles it.
func (c controllerRoleCheck) Run(context.Context) (doctor.CheckResult, error) {
	var binding nodes.ControllerBindingBackend
	if c.paths != nil {
		binding = nodes.NewFileControllerBindingBackend(c.paths.DataDir())
	}
	role, conflict := nodes.RoleStatus(binding)
	if conflict == nil {
		return doctor.CheckResult{Status: doctor.StatusOK, Message: "role " + string(role)}, nil
	}
	if conflict.Kind == "none" && c.paths != nil && neverBound(c.paths.DataDir()) {
		return doctor.CheckResult{
			Status:      doctor.StatusWarn,
			Message:     "not designated yet: no designation, binding or node-bind tombstone exists",
			Remediation: "run `cascade init`, or start the daemon once to bootstrap designation epoch 1",
		}, nil
	}
	return doctor.CheckResult{
		Status:      doctor.StatusError,
		Message:     "controller role conflict (" + conflict.Kind + "): " + conflict.Detail,
		Remediation: roleRemedy(conflict.Kind),
	}, nil
}

// roleRemedy is the operator's next step for a conflict kind.
func roleRemedy(kind string) string {
	if kind == "binding-incomplete" {
		return "this home holds a node binding without the pinned controller key; re-run `cascade node bind` on this node"
	}
	if kind == "both" {
		return "keep one role: remove the binding and run `cascade init` on a controller, or re-run `cascade node bind` on a node"
	}
	if kind == "none" {
		return "run `cascade init` on a controller, or `cascade node bind` on a node"
	}
	return "repair the records under the data directory's nodes/ folder, then run `cascade init` on a controller or `cascade node bind` on a node"
}

// neverBound reports whether this home still meets the daemon's bootstrap
// preconditions: neither the binding file nor the node-bind tombstone
// exists. Any other answer, including an unreadable path, is false, so the
// none conflict stays an error.
func neverBound(dataDir string) bool {
	for _, name := range []string{"controller_binding.json", "bound_once"} {
		if _, err := os.Lstat(filepath.Join(dataDir, "nodes", name)); !errors.Is(err, fs.ErrNotExist) {
			return false
		}
	}
	return true
}

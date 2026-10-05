// Seeded violation for internal/build/arch_sensitivity_test.go (P1-SEC-19).
// It lives under testdata/ so the toolchain never compiles it. Do not "fix" it.
package fixture

import "github.com/acamarata/cascade/internal/nodes"

// unset leaves Sensitivity to the zero value: a gap.
var unset = nodes.Requirement{Capabilities: []string{"docker"}}

// set names its tier explicitly: no gap.
var set = nodes.Requirement{Capabilities: []string{"docker"}, Sensitivity: 2}

// shipUnset leaves the shipped work's tier to the zero value: a gap.
var shipUnset = nodes.ShipRequest{DispatchID: "d"}

// shipSet names its tier explicitly: no gap.
var shipSet = nodes.ShipRequest{DispatchID: "d", Sensitivity: 1}

// requeueUnset re-applies no Requirement, so its tier is the zero value: a gap.
var requeueUnset = nodes.RequeueRequest{DispatchID: "d"}

// requeueSet re-applies a Requirement that names its tier: no gap.
var requeueSet = nodes.RequeueRequest{DispatchID: "d", Requirement: nodes.Requirement{Sensitivity: 1}}

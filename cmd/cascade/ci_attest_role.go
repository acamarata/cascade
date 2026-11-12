package main

import (
	"database/sql"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	cruntime "github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// attestTrustForRole picks the ci attest verify trust store by role: the
// controller identity plus record store on a controller, ci.NewVouchedTrustStore
// over the node db on a node, and an error on any other value.
func attestTrustForRole(db *sql.DB, dataDir string, clock cruntime.Clock) (attest.TrustStore, error) {
	role, err := nodes.ResolveRole(nodes.NewFileControllerBindingBackend(dataDir))
	if err != nil {
		return nil, err
	}
	switch role {
	case nodes.RoleController:
		return attestTrustStore(dataDir, clock)
	case nodes.RoleNode:
		store, err := jobs.NewCiAttestTrustStore(db)
		if err != nil {
			return nil, err
		}
		return ci.NewVouchedTrustStore(store), nil
	default:
		return nil, cascade.New(cascade.KindInvalidInput, "ci attest verify: unknown node role")
	}
}

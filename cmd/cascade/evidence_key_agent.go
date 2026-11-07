// Purpose: the agent leg of newEvidenceKeyFn. An "agent:<harness>" account
// is a harness lane, not a registry provider: its key binds the lane id,
// the owning plugin, the resolved vendor executable and the credential
// class, so a lane re-bound to another plugin, binary or class no longer
// matches its old evidence, and a lane that still resolves is never read
// as gone (a prune keeps its rows).
// Inputs: the daemon's agentLanes resolver and agents.ShimDeps.
// Outputs: topology.EvidenceKey values and the frozen-kind errors below.
// Constraints: builds the driver the way probeAgent does and runs no child
// process; reads no credential value.
// SPORT: cmd/cascade billing evidence (NEW, P1-TOP-19c).

package main

import (
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// agentEvidenceKey builds the key for one harness lane id.
func agentEvidenceKey(d evidenceKeyDeps, key []byte, id string) (topology.EvidenceKey, error) {
	if d.Agents == nil {
		return topology.EvidenceKey{}, cascade.New(cascade.KindUnavailable, "evidence key: agent lanes are not composed")
	}
	b, factory, err := d.Agents.Resolve(id)
	if err != nil {
		return topology.EvidenceKey{}, cascade.Wrapf(cascade.KindNotFound, err, "evidence key: agent lane %q no longer resolves", id)
	}
	h := b.Harness
	// A probe-only site, as probeAgent builds it: no credential, no worktree.
	drv, err := factory(agents.SpawnSite{Binary: d.Shim.Binaries[h], Posture: d.Shim.Postures[h], Defaults: d.Shim.Defaults[h]})
	if err != nil {
		return topology.EvidenceKey{}, cascade.Wrapf(cascade.KindUnavailable, err, "evidence key: agent lane %q driver", id)
	}
	rep, ok := drv.(provider.ExecutableReporter)
	if !ok {
		return topology.EvidenceKey{}, cascade.Newf(cascade.KindUnavailable, "evidence key: agent lane %q driver reports no executable", id)
	}
	class := "none"
	if auth, ok := d.Shim.Auth[h]; ok {
		class = auth.Class
	}
	return topology.EvidenceKey{
		Provider:              id,
		InstanceID:            id + "@" + b.PluginID,
		CredentialFingerprint: keyedFingerprint(key, "agent", b.PluginID, rep.Executable(), class),
	}, nil
}

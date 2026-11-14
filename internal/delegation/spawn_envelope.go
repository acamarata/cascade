// Purpose: the signed spawn envelope on the spawn path. Before the driver spawn the
// stored child record is verified under the pinned controller key and projected by
// agents.ProjectEnvelope, the one conversion to provider.SpawnEnvelope; a record that
// does not verify spawns nothing. Nothing here builds or edits an envelope by hand.

package delegation

import (
	"slices"

	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/pkg/envelope"
	"github.com/acamarata/cascade/pkg/provider"
)

// spawnJob is the job spec for rec: the envelope agents.ProjectEnvelope projects after
// verifying rec under the pinned key at now (its refusal comes back unchanged), the tree
// the child runs in (the read-only worktree, else a mutation's repo root) and its scope.
func (s *Service) spawnJob(req *Request, rec envelope.Signed, policy string) (provider.AgentJobSpec, error) {
	env, err := agents.ProjectEnvelope(rec, s.deps.Pinned, s.deps.Clock.Now())
	if err != nil {
		return provider.AgentJobSpec{}, err
	}
	job := provider.AgentJobSpec{JobID: provider.AgentJobID(rec.Env.ID), ToolPolicy: policy, Envelope: env, WorktreeRoot: req.RepoRoot}
	if req.Worktree != nil && req.Worktree.Root != "" {
		job.WorktreeRoot = req.Worktree.Root
	}
	if req.Mutation {
		job.Scope.InScopePrefixes = slices.Clone(req.InScope)
	}
	return job, nil
}

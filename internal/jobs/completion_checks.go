package jobs

// Purpose: check completion evidence completeness and checkpoint consistency.
// SPORT: jobs/completion-gate/MOVE (P1-PBD-01h).

import "context"

// missingEvidence returns required EvidenceKinds lacking a passing, non-agent-claim record.
func (cp *CompletionPolicy) missingEvidence(ctx context.Context, req TransitionRequest, gateSet GateSet) ([]EvidenceKind, error) {
	var missing []EvidenceKind
	for _, kind := range evidenceKindsForGateSet(gateSet) {
		rec, err := cp.deps.Ledger.Query(ctx, req.Job.ID, kind)
		if err != nil {
			missing = append(missing, kind)
			continue
		}
		if rec.ProducerCapability == ProducerAgentClaim || rec.Outcome != OutcomePass ||
			(kind == EvidenceCIAttestation && !cp.eligibleAttestation(ctx, req, rec)) {
			missing = append(missing, kind)
		}
	}
	return missing, nil
}

// checkpointMismatch compares req.CheckpointID verbatim against the evidence
// rows' checkpoint id (R-21.144); an empty CheckpointID never mismatches.
func (cp *CompletionPolicy) checkpointMismatch(ctx context.Context, req TransitionRequest) (*string, error) {
	if req.CheckpointID == "" {
		return nil, nil
	}
	q := cp.queryEvidence
	if q == nil {
		q = cp.deps.Ledger.Query
	}
	for _, kind := range []EvidenceKind{EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview, EvidenceAdversarial, EvidenceQA} {
		rec, err := q(ctx, req.Job.ID, kind)
		if err == ErrNoEvidence {
			continue
		}
		if err != nil {
			return nil, err
		}
		if rec.CheckpointID != "" && rec.CheckpointID != req.CheckpointID {
			got := rec.CheckpointID
			return &got, nil
		}
	}
	return nil, nil
}

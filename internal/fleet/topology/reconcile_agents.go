// Purpose: reconcile agent harness lanes without credential custody or probes.
// Inputs: agent lane records, the existing Store and an injected Clock.
// Outputs: reconciled record count or a topology error; failed passes roll back.
// Constraints: reconcile-scoped writers preserve owner-managed columns.
// SPORT: fleet/topology/reconcile_agents/ADD (P1-TOP-06).

package topology

import "context"

// AgentLaneRecord describes an agent registry lane and its harness id.
type AgentLaneRecord struct {
	LaneName, Harness string
}

// ReconcileAgentLanes reconciles harness accounts, domains, profiles,
// credentials and live lanes atomically. Agent credentials have no SecretRef.
func ReconcileAgentLanes(ctx context.Context, s *Store, clock Clock, records []AgentLaneRecord) (int, error) {
	for _, rec := range records {
		switch RuntimeKind(rec.Harness) {
		case RuntimeClaudeCLI, RuntimeCodex, RuntimeOpenCode, RuntimeAntigravity:
		case RuntimeAPI, RuntimeOllama:
			return 0, newInvariantErr("agent_harness", rec.Harness, "not an agent harness")
		default:
			return 0, newInvariantErr("agent_harness", rec.Harness, "unknown harness id")
		}
		if rec.LaneName == "" {
			return 0, newInvariantErr("agent_lane", rec.LaneName, "lane name must not be empty")
		}
	}
	err := s.withTx(ctx, func(tx *Store) error {
		for _, rec := range records {
			if err := upsertAgentRows(ctx, tx, rec, clock); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(records), nil
}

func upsertAgentRows(ctx context.Context, s *Store, rec AgentLaneRecord, clock Clock) error {
	id := "agent:" + rec.Harness
	writes := []func() (int, error){
		func() (int, error) {
			return s.reconcileAccount(ctx, Account{ID: AccountID(id), Provider: "agent",
				Billing: BillingInfo{Kind: BillingFree}, Role: AccountRoleWorkforce})
		},
		func() (int, error) {
			return s.reconcileQuotaDomain(ctx, QuotaDomain{ID: DomainID(id), AccountRef: AccountID(id),
				Kind: QuotaDomainAPIProject, BillingTier: BillingTierDiscover})
		},
		func() (int, error) {
			return s.reconcileRuntimeProfile(ctx, RuntimeProfile{ID: RuntimeProfileID(id),
				Runtime: RuntimeKind(rec.Harness), Persistent: true})
		},
		func() (int, error) {
			return s.reconcileCredential(ctx, Credential{ID: CredentialID(id), AccountRef: AccountID(id),
				QuotaDomainRef: DomainID(id), RuntimeProfileRef: RuntimeProfileID(id), Health: CredentialOK})
		},
	}
	for _, write := range writes {
		if _, err := write(); err != nil {
			return err
		}
	}
	return reconcileAgentLane(ctx, s, CredentialID(id), clock)
}

func reconcileAgentLane(ctx context.Context, s *Store, id CredentialID, clock Clock) error {
	cred, err := s.GetCredential(ctx, id)
	if err != nil {
		return err
	}
	domain, err := s.GetQuotaDomain(ctx, cred.QuotaDomainRef)
	if err != nil {
		return err
	}
	health := LaneHealthAvailable
	if cred.Health != CredentialOK || domain.Quarantined || cred.QuarantinedUntil.After(clock.Now()) {
		health = LaneHealthQuarantined
	}
	lane := Lane{ID: LaneIDFor(cred.RuntimeProfileRef, domain.ID, "", "", EffortNA, InteractionInteractive),
		RuntimeProfileRef: cred.RuntimeProfileRef, QuotaDomainRef: domain.ID, CredentialRef: &id,
		Effort: EffortNA, InteractionClass: InteractionInteractive, Health: health,
		LaneClass: LaneClassUnranked, BaseShadowPrice: 1, DiscoveredAt: clock.Now()}
	_, err = s.reconcileLaneRow(ctx, lane)
	return err
}

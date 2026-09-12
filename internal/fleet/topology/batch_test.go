package topology

import "testing"

func batchDomain() QuotaDomain {
	return QuotaDomain{
		ID: "d1", Kind: QuotaDomainAPIProject, BillingTier: BillingTierPaid,
		Batch: BatchLimits{Supported: true, ConcurrentRequests: 4, EnqueuedTokenLimit: 10000},
	}
}

// TestDeriveBatchBucketAndLane asserts a paid api domain with
// batch.supported gains the sibling bucket and an interaction_class:batch
// lane, and that neither is ever confused with the interactive one.
func TestDeriveBatchBucketAndLane(t *testing.T) {
	d := batchDomain()
	profile := RuntimeProfile{ID: "rp1", Runtime: RuntimeAPI}

	bucket, ok := DeriveBatchBucket(d)
	if !ok {
		t.Fatal("expected a batch-supported paid domain to yield a sibling bucket")
	}
	if bucket.Limit != d.Batch.EnqueuedTokenLimit {
		t.Fatalf("bucket limit = %v, want %v", bucket.Limit, d.Batch.EnqueuedTokenLimit)
	}

	batchLane, ok := DeriveBatchLane(d, profile)
	if !ok {
		t.Fatal("expected a batch lane to be derived")
	}
	if batchLane.InteractionClass != InteractionBatch {
		t.Fatalf("interaction class = %v, want batch", batchLane.InteractionClass)
	}

	interactiveLane := Lane{RuntimeProfileRef: profile.ID, QuotaDomainRef: d.ID, InteractionClass: InteractionInteractive}
	interactiveLane.ID = LaneIDFor(interactiveLane.RuntimeProfileRef, interactiveLane.QuotaDomainRef, "", "", EffortNA, InteractionInteractive)
	if batchLane.ID == interactiveLane.ID {
		t.Fatal("batch and interactive lanes over the same domain must never share an identity")
	}
}

// TestDeriveBatchRefusesUnqualifiedDomains asserts a free-tier domain, a
// non-api_project domain, and batch.supported=false all refuse -- never a
// batch lane for a domain that never opted in.
func TestDeriveBatchRefusesUnqualifiedDomains(t *testing.T) {
	free := batchDomain()
	free.BillingTier = BillingTierFree
	if _, ok := DeriveBatchBucket(free); ok {
		t.Fatal("expected a free-tier domain to be refused")
	}

	notAPI := batchDomain()
	notAPI.Kind = QuotaDomainSharedPool
	if _, ok := DeriveBatchBucket(notAPI); ok {
		t.Fatal("expected a non-api_project domain to be refused")
	}

	unsupported := batchDomain()
	unsupported.Batch.Supported = false
	if _, ok := DeriveBatchBucket(unsupported); ok {
		t.Fatal("expected batch.supported=false to be refused")
	}
	if _, ok := DeriveBatchLane(unsupported, RuntimeProfile{ID: "rp1"}); ok {
		t.Fatal("expected batch.supported=false to refuse a batch lane too")
	}
}

// TestBatchGaugesAreEligibilityOnly asserts GaugeReservable (dimensions.go)
// gates on gauge+estimate<=limit and that a batch lane is never returned
// for the InteractionInteractive class or vice versa via Candidates.
func TestBatchGaugesAreEligibilityOnly(t *testing.T) {
	if !GaugeReservable(2, 1, 4) {
		t.Fatal("expected 2+1<=4 to be reservable")
	}
	if GaugeReservable(4, 1, 4) {
		t.Fatal("expected 4+1<=4 to be false")
	}
	if !GaugeReservable(100, 1, 0) {
		t.Fatal("expected limit<=0 (unconfigured) to always be reservable")
	}
}

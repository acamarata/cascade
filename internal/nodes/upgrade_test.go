package nodes

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// upgradeStoreWith seeds an in-memory RecordStore with recs, mirroring
// records_test.go's memRecordBackend precedent.
func upgradeStoreWith(recs ...DeviceRecord) *RecordStore {
	backend := newMemRecordBackend()
	m := map[string]DeviceRecord{}
	for _, r := range recs {
		m[r.NodeID] = r
	}
	backend.records = m
	return NewRecordStore(backend, fixedClock{})
}

func TestRunUpgrade_SingleNodeNoRoute(t *testing.T) {
	store := upgradeStoreWith(DeviceRecord{NodeID: "n1"}) // no Route configured
	artifact, pub := testArtifact(t, "v2.5.0")
	outcomes, err := RunUpgrade(context.Background(), UpgradeRequest{NodeID: "n1"}, UpgradeDeps{
		Store: store, Artifact: artifact,
		Provision: ProvisionDeps{PublicKey: pub, VerifyFor: noopVerifyFor},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(outcomes) != 1 || !outcomes[0].Skipped || outcomes[0].Reason == "" {
		t.Fatalf("outcomes = %+v, want one skipped-with-reason outcome", outcomes)
	}
}

func TestRunUpgrade_UnknownNodeRefused(t *testing.T) {
	store := upgradeStoreWith()
	_, err := RunUpgrade(context.Background(), UpgradeRequest{NodeID: "ghost"}, UpgradeDeps{Store: store})
	if err == nil {
		t.Fatal("expected a KindNotFound refusal for an unknown node id")
	}
	if kind, _ := cascade.KindOf(err); kind != cascade.KindNotFound {
		t.Fatalf("kind = %v, want KindNotFound", kind)
	}
}

func TestRunUpgrade_AllContinuesPastAPerNodeFailure(t *testing.T) {
	// n1 has no route (skipped); n2 has a route but an unreachable dialer
	// (failed); n3 has a route and succeeds. The rollout must report all
	// three, never abort after n1 or n2.
	n1 := DeviceRecord{NodeID: "n1"}
	n2 := testRecordWithRoute("n2", "worker", "host2:22")
	n3 := testRecordWithRoute("n3", "worker", "host3:22")
	store := upgradeStoreWith(n1, n2, n3)
	artifact, pub := testArtifact(t, "v2.5.0")

	dialers := map[string]ExecDialer{
		"n2": &fakeExecDialer{err: cascade.New(cascade.KindUnavailable, "unreachable")},
		"n3": &fakeExecDialer{session: newFakeExecSession("v2.4.0")},
	}
	routingDialer := routedDialerFunc(func(target Target) ExecDialer { return dialers[target.NodeID] })

	outcomes, err := RunUpgrade(context.Background(), UpgradeRequest{All: true}, UpgradeDeps{
		Store: store, Artifact: artifact,
		Provision: ProvisionDeps{PublicKey: pub, VerifyFor: noopVerifyFor, Dialer: routingDialer},
	})
	if err != nil {
		t.Fatalf("unexpected systemic error: %v", err)
	}
	if len(outcomes) != 3 {
		t.Fatalf("outcomes = %+v, want 3", outcomes)
	}
	byID := map[string]UpgradeOutcome{}
	for _, o := range outcomes {
		byID[o.NodeID] = o
	}
	if !byID["n1"].Skipped {
		t.Fatalf("n1 = %+v, want skipped (no route)", byID["n1"])
	}
	if byID["n2"].Err == "" {
		t.Fatalf("n2 = %+v, want a per-node error, not a systemic abort", byID["n2"])
	}
	if !byID["n3"].Installed {
		t.Fatalf("n3 = %+v, want installed", byID["n3"])
	}
}

func TestRunUpgrade_DrainedNodeExcludedFromAll(t *testing.T) {
	drained := testRecordWithRoute("n1", "worker", "host1:22")
	drained.Drained = true
	store := upgradeStoreWith(drained)
	outcomes, err := RunUpgrade(context.Background(), UpgradeRequest{All: true}, UpgradeDeps{Store: store})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(outcomes) != 0 {
		t.Fatalf("outcomes = %+v, want none: a drained node must never be rolled", outcomes)
	}
}

func TestRunUpgrade_SkewWarningSurfacedOnInstall(t *testing.T) {
	rec := testRecordWithRoute("n1", "worker", "host1:22")
	store := upgradeStoreWith(rec)
	artifact, pub := testArtifact(t, "v2.5.0")
	session := newFakeExecSession("v1.9.0") // pre-existing skew vs controller v2.5.x
	outcomes, err := RunUpgrade(context.Background(), UpgradeRequest{NodeID: "n1"}, UpgradeDeps{
		Store: store, Artifact: artifact,
		Provision: ProvisionDeps{
			PublicKey: pub, VerifyFor: noopVerifyFor, Dialer: &fakeExecDialer{session: session},
			ControllerVersion: "v2.5.0",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(outcomes) != 1 || !outcomes[0].SkewWarning {
		t.Fatalf("outcomes = %+v, want SkewWarning=true", outcomes)
	}
}

// routedDialerFunc adapts a per-target dialer lookup to ExecDialer, for
// TestRunUpgrade_AllContinuesPastAPerNodeFailure's multi-node scenario.
type routedDialerFunc func(Target) ExecDialer

func (f routedDialerFunc) Dial(ctx context.Context, target Target, verify HostKeyVerifier) (ExecSession, error) {
	return f(target).Dial(ctx, target, verify)
}

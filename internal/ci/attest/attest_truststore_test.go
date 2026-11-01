package attest

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/nodes"
)

func TestRevokedKeyRejected(t *testing.T) {
	n := NewTestNode(t)
	r := n.SampleRecord()
	sa := n.MustAttest(t, r)
	if _, err := n.Records.Revoke(n.ID.NodeID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	err := VerifySignature(context.Background(), sa, r.CommitSHA, r.TreeHash, n.Trust(), TestStart.Add(time.Hour))
	if !IsSentinel(err, ErrRevokedKey) {
		t.Fatalf("VerifySignature(revoked node key) = %v, want ErrRevokedKey", err)
	}
	if _, err := n.Trust().Lookup(context.Background(), n.ID.NodeID, nodes.KeyIDFor(n.ID.PubKey)); !IsSentinel(err, ErrRevokedKey) {
		t.Fatalf("Lookup(revoked current key) = %v, want ErrRevokedKey", err)
	}
	if DeriveClass(sa, n.Trust(), nil) != ClassUnverified {
		t.Fatal("a revoked key still derives an eligible class")
	}
}

func TestRotatedKeyRefusedAfterRotation(t *testing.T) {
	n := NewTestNode(t)
	r := n.SampleRecord()
	old := n.MustAttest(t, r)
	next, nextPriv, err := nodes.GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	reqSig, err := n.Keys.Sign(context.Background(), n.ID.NodeID, []byte(n.ID.NodeID+"\x1f"+next.PubKeyB64()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.Records.Rotate(n.ID.NodeID, next.PubKeyB64(), reqSig); err != nil {
		t.Fatalf("RecordStore.Rotate: %v", err)
	}
	if err := n.Keys.Store(context.Background(), n.ID.NodeID, nextPriv); err != nil {
		t.Fatal(err)
	}
	err = VerifySignature(context.Background(), old, r.CommitSHA, r.TreeHash, n.Trust(), TestStart.Add(time.Hour))
	if !IsSentinel(err, ErrRevokedKey) {
		t.Fatalf("VerifySignature(superseded key) = %v, want ErrRevokedKey", err)
	}
	if _, err := n.Trust().Lookup(context.Background(), n.ID.NodeID, nodes.KeyIDFor(n.ID.PubKey)); !IsSentinel(err, ErrRevokedKey) {
		t.Fatalf("Lookup(superseded key id) = %v, want ErrRevokedKey", err)
	}
	rotated := n.SampleRecord()
	rotated.KeyID = nodes.KeyIDFor(next.PubKey)
	fresh := n.MustAttest(t, rotated)
	if err := VerifySignature(context.Background(), fresh, r.CommitSHA, r.TreeHash, n.Trust(), TestStart.Add(time.Hour)); err != nil {
		t.Fatalf("VerifySignature(current key after rotation) = %v, want nil", err)
	}
	claimsOld := rotated
	claimsOld.KeyID = nodes.KeyIDFor(n.ID.PubKey)
	err = VerifySignature(context.Background(), n.MustAttest(t, claimsOld), r.CommitSHA, r.TreeHash, n.Trust(), TestStart.Add(time.Hour))
	if !IsSentinel(err, ErrRevokedKey) {
		t.Fatalf("VerifySignature(new key signing under the old key id) = %v, want ErrRevokedKey", err)
	}
}

func TestControllerTrustStoreRefusesOtherKeyID(t *testing.T) {
	ctrl := NewTestNode(t)
	worker := NewTestNode(t)
	ts := NewControllerTrustStore(ctrl.ID, worker.Trust())
	ctx := context.Background()
	pub, err := ts.Lookup(ctx, ctrl.ID.NodeID, nodes.KeyIDFor(ctrl.ID.PubKey))
	if err != nil || !pub.Equal(ctrl.ID.PubKey) {
		t.Fatalf("Lookup(controller, its key id) = %v, %v", pub, err)
	}
	if _, err := ts.Lookup(ctx, ctrl.ID.NodeID, nodes.KeyIDFor(worker.ID.PubKey)); !IsSentinel(err, ErrRevokedKey) {
		t.Fatalf("Lookup(controller, another key id) = %v, want ErrRevokedKey", err)
	}
	if pub, err := ts.Lookup(ctx, worker.ID.NodeID, nodes.KeyIDFor(worker.ID.PubKey)); err != nil || !pub.Equal(worker.ID.PubKey) {
		t.Fatalf("Lookup(worker through next) = %v, %v", pub, err)
	}
	if _, err := ts.Lookup(ctx, "unknown-node", "0123456789abcdef"); !IsSentinel(err, ErrUnknownNode) {
		t.Fatalf("Lookup(unknown node) = %v, want ErrUnknownNode", err)
	}
	if _, err := NewControllerTrustStore(ctrl.ID, nil).Lookup(ctx, worker.ID.NodeID, "x"); !IsSentinel(err, ErrUnknownNode) {
		t.Fatalf("Lookup(no next store) = %v, want ErrUnknownNode", err)
	}
	if _, err := NewRecordTrustStore(nil).Lookup(ctx, worker.ID.NodeID, "x"); !IsSentinel(err, ErrUnknownNode) {
		t.Fatalf("Lookup(nil record store) = %v, want ErrUnknownNode", err)
	}
	r := ctrl.SampleRecord()
	if err := VerifySignature(ctx, ctrl.MustAttest(t, r), r.CommitSHA, r.TreeHash, ts, TestStart.Add(time.Hour)); err != nil {
		t.Fatalf("VerifySignature(controller's own record) = %v, want nil", err)
	}
}

func TestControllerTrustStoreHonoursRecordRevocation(t *testing.T) {
	ctrl := NewTestNode(t)
	ts := NewControllerTrustStore(ctrl.ID, ctrl.Trust())
	ctx := context.Background()
	keyID := nodes.KeyIDFor(ctrl.ID.PubKey)
	if _, err := ts.Lookup(ctx, ctrl.ID.NodeID, keyID); err != nil {
		t.Fatalf("Lookup(enrolled controller) = %v", err)
	}
	next, _, err := nodes.GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	reqSig, _ := ctrl.Keys.Sign(ctx, ctrl.ID.NodeID, []byte(ctrl.ID.NodeID+"\x1f"+next.PubKeyB64()))
	if _, err := ctrl.Records.Rotate(ctrl.ID.NodeID, next.PubKeyB64(), reqSig); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Lookup(ctx, ctrl.ID.NodeID, keyID); !IsSentinel(err, ErrRevokedKey) {
		t.Fatalf("Lookup(self key superseded in the record store) = %v, want ErrRevokedKey", err)
	}
	other := NewTestNode(t)
	if _, err := other.Records.Revoke(other.ID.NodeID); err != nil {
		t.Fatal(err)
	}
	ts = NewControllerTrustStore(other.ID, other.Trust())
	if _, err := ts.Lookup(ctx, other.ID.NodeID, nodes.KeyIDFor(other.ID.PubKey)); !IsSentinel(err, ErrRevokedKey) {
		t.Fatalf("Lookup(self key revoked in the record store) = %v, want ErrRevokedKey", err)
	}
}

func TestRecordTrustStoreRefusesPairedDevice(t *testing.T) {
	n := NewTestNode(t)
	device, _, err := nodes.GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.Records.Enroll(device, nodes.TierPairedDevice); err != nil {
		t.Fatal(err)
	}
	_, err = n.Trust().Lookup(context.Background(), device.NodeID, nodes.KeyIDFor(device.PubKey))
	if !IsSentinel(err, ErrUnverifiedAttestor) {
		t.Fatalf("Lookup(paired device) = %v, want ErrUnverifiedAttestor", err)
	}
}

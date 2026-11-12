package ci

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestVouchedTrustStoreResolvesOnlyVouchedKeys checks exact pairs and refusals.
func TestVouchedTrustStoreResolvesOnlyVouchedKeys(t *testing.T) {
	r := newNodeAttestRig(t)
	ctx := context.Background()
	s, _ := jobs.NewCiAttestTrustStore(r.db)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	k := nodes.KeyIDFor(pub)
	if err := s.Vouch(ctx, "n1", k, base64.StdEncoding.EncodeToString(pub), r.clock.Now()); err != nil {
		t.Fatal(err)
	}
	ts := NewVouchedTrustStore(s)
	got, err := ts.Lookup(ctx, "n1", k)
	if err != nil || !bytes.Equal(got, pub) {
		t.Fatalf("vouched lookup: %v", err)
	}
	for _, pair := range [][2]string{{"n2", k}, {"n1", "other"}, {"", k}, {"n1", ""}} {
		_, err := ts.Lookup(ctx, pair[0], pair[1])
		wantCIKind(t, err, cascade.KindNotFound)
		if !errors.Is(err, attest.ErrUnknownNode) {
			t.Fatal(err)
		}
	}
	_, err = NewVouchedTrustStore(nil).Lookup(ctx, "n1", k)
	wantCIKind(t, err, cascade.KindNotFound)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := s.Vouch(ctx, "n1", "k2", base64.StdEncoding.EncodeToString(other), r.clock.Now()); err != nil {
		t.Fatal(err)
	}
	_, err = ts.Lookup(ctx, "n1", "k2")
	wantCIKind(t, err, cascade.KindPermissionDenied)
	if !errors.Is(err, attest.ErrRevokedKey) {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`UPDATE ci_attest_trust SET pub_key_b64='corrupt' WHERE key_id='k2'`); err != nil {
		t.Fatal(err)
	}
	_, err = ts.Lookup(ctx, "n1", "k2")
	wantCIKind(t, err, cascade.KindIntegrity)
	if err := r.db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = ts.Lookup(ctx, "n1", k)
	wantCIKind(t, err, cascade.KindUnavailable)
}

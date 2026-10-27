// Purpose: tests for ProvisionDaemonKey: generated once, create-only, a
//
//	malformed stored value refused without a write, a transient read
//	returned unchanged with nothing generated. Every leg runs for both
//	reserved names over a real temp-dir file vault.
//
// SPORT: internal/secrets ProvisionDaemonKey tests (P1-SEC-23).

package secrets

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// countingCustody counts writes and can fail reads with a fixed error.
type countingCustody struct {
	Custody
	sets    atomic.Int32
	failGet error
}

func (c *countingCustody) Set(ctx context.Context, name string, value []byte) error {
	c.sets.Add(1)
	return c.Custody.Set(ctx, name, value)
}

func (c *countingCustody) Get(ctx context.Context, name string) ([]byte, error) {
	if c.failGet != nil {
		return nil, c.failGet
	}
	return c.Custody.Get(ctx, name)
}

// newCountingBroker is a broker over a counting temp-dir file vault.
func newCountingBroker(t *testing.T) (*Broker, *countingCustody) {
	t.Helper()
	fv, err := newFileVaultCustody(Config{Service: DefaultVaultService, Dir: t.TempDir(), Passphrase: "fake-daemon-keys-pass"})
	if err != nil {
		t.Fatalf("newFileVaultCustody: %v", err)
	}
	cc := &countingCustody{Custody: fv}
	b, err := NewBroker(cc, unelevatedRefusingGate{})
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	return b.WithDaemonKeyLock(filepath.Join(t.TempDir(), "daemon-key.lock")), cc
}

var daemonKeyNames = []string{ApprovalKeyName, InstallationKeyName}

func TestApprovalKeyGeneratedOnceCreateOnly(t *testing.T) {
	ctx := context.Background()
	for _, name := range daemonKeyNames {
		b, cc := newCountingBroker(t)
		id1, pub1, created, err := ProvisionDaemonKey(ctx, b, name, rand.Reader)
		if err != nil || !created || len(pub1) != ed25519.PublicKeySize || id1 == "" {
			t.Fatalf("%s first provision = %q, %d-byte pub, created=%v, %v", name, id1, len(pub1), created, err)
		}
		stored, err := cc.Custody.Get(ctx, name)
		if err != nil {
			t.Fatalf("%s stored value: %v", name, err)
		}
		id2, pub2, created, err := ProvisionDaemonKey(ctx, b, name, rand.Reader)
		if err != nil || created || !bytes.Equal(pub1, pub2) || id1 != id2 {
			t.Fatalf("%s second provision = %q created=%v err=%v; want the same key, not created", name, id2, created, err)
		}
		if n := cc.sets.Load(); n != 1 {
			t.Fatalf("%s custody writes = %d, want exactly 1 (create-only)", name, n)
		}
		again, _ := cc.Custody.Get(ctx, name)
		if !bytes.Equal(stored, again) {
			t.Fatalf("%s stored key changed on the second provision", name)
		}
		msg := []byte("fake approval record")
		if !ed25519.Verify(pub1, msg, ed25519.Sign(ed25519.PrivateKey(stored), msg)) {
			t.Fatalf("%s returned public key does not verify the stored key's signature", name)
		}
	}
}

func TestProvisionDaemonKeyRefusesMalformedEntry(t *testing.T) {
	ctx := context.Background()
	_, good, _ := ed25519.GenerateKey(rand.Reader)
	mismatched := append(append([]byte(nil), good[:ed25519.SeedSize]...), bytes.Repeat([]byte{7}, 32)...)
	for _, name := range daemonKeyNames {
		for _, bad := range [][]byte{bytes.Repeat([]byte{1}, 31), bytes.Repeat([]byte{2}, 32), bytes.Repeat([]byte{3}, 63), mismatched} {
			b, cc := newCountingBroker(t)
			// Seeded through the file-vault custody under the broker: Broker.Set refuses the name.
			if err := cc.Custody.Set(ctx, name, bad); err != nil {
				t.Fatalf("seeding %s: %v", name, err)
			}
			id, pub, created, err := ProvisionDaemonKey(ctx, b, name, rand.Reader)
			if !cascade.HasKind(err, cascade.KindIntegrity) || pub != nil || created || id != "" {
				t.Fatalf("%s with a %d-byte value = %q %v created=%v %v; want KindIntegrity and nothing derived", name, len(bad), id, pub, created, err)
			}
			if n := cc.sets.Load(); n != 0 {
				t.Fatalf("%s with a %d-byte value: %d custody writes, want 0", name, len(bad), n)
			}
			if after, _ := cc.Custody.Get(ctx, name); !bytes.Equal(after, bad) {
				t.Fatalf("%s with a %d-byte value: the stored value changed", name, len(bad))
			}
		}
	}
}

func TestProvisionDaemonKeyTransientReadNoGenerate(t *testing.T) {
	ctx := context.Background()
	for _, name := range daemonKeyNames {
		b, cc := newCountingBroker(t)
		transient := cascade.New(cascade.KindUnavailable, "fake keychain is locked")
		cc.failGet = transient
		_, pub, created, err := ProvisionDaemonKey(ctx, b, name, rand.Reader)
		if err != transient { //nolint:errorlint // identity: the read error must come back unchanged
			t.Fatalf("%s transient read = %v, want the custody error unchanged", name, err)
		}
		if pub != nil || created || cc.sets.Load() != 0 {
			t.Fatalf("%s transient read: pub=%v created=%v writes=%d; want nothing generated", name, pub, created, cc.sets.Load())
		}
		if names, _ := cc.List(ctx); len(names) != 0 {
			t.Fatalf("%s transient read left entries %v", name, names)
		}
	}
}

func TestProvisionDaemonKeyRefusesOtherNames(t *testing.T) {
	b, cc := newCountingBroker(t)
	_, _, _, err := ProvisionDaemonKey(context.Background(), b, "github_token", rand.Reader)
	if !cascade.HasKind(err, cascade.KindInvalidInput) || cc.sets.Load() != 0 {
		t.Fatalf("provisioning an ordinary name = %v (writes %d), want KindInvalidInput and no write", err, cc.sets.Load())
	}
}

// swappingCustody reports a create but stores a different key, the state a
// lost write leaves behind.
type swappingCustody struct{ Custody }

func (s swappingCustody) createIfAbsent(ctx context.Context, name string, _ []byte) (bool, error) {
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	return true, s.Set(ctx, name, other)
}

func TestProvisionDaemonKeyVerifiesItsWriteByRereading(t *testing.T) {
	fv, err := newFileVaultCustody(Config{Service: DefaultVaultService, Dir: t.TempDir(), Passphrase: "fake-verify-pass"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewBroker(swappingCustody{Custody: fv}, nil)
	_, pub, created, err := ProvisionDaemonKey(context.Background(), b.WithDaemonKeyLock(filepath.Join(t.TempDir(), "k.lock")), ApprovalKeyName, rand.Reader)
	if !cascade.HasKind(err, cascade.KindIntegrity) || pub != nil || created {
		t.Fatalf("provision over a lost write = %v created=%v err=%v; want KindIntegrity", pub, created, err)
	}
}

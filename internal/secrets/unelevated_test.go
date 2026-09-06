// Purpose: tests for the two named non-elevated vault reads. Each is
//
//	driven against a real file-vault custody under t.TempDir(), never a
//	stand-in, so what passes here is what the shipped read path does.
//
// SPORT: SECRETS_UNELEVATED_READ: ADD (tests).

package secrets

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// unelevatedRefusingGate refuses every elevated verb, the way a release binary
// does. Every test here runs behind it: a read that still works under a
// refusing gate is a read that genuinely skips the elevated path.
type unelevatedRefusingGate struct{}

func (unelevatedRefusingGate) Authorize(context.Context, string) error {
	return cascade.New(cascade.KindElevationRequired, "secrets: refused for the test")
}

// newTestBroker builds a broker over a temp-dir file vault behind a gate
// that refuses every elevated verb.
func newUnelevatedBroker(t *testing.T) *Broker {
	t.Helper()
	// The file vault directly, never SelectCustody: on a host with an OS
	// keychain SelectCustody would prefer it, and a unit test must not
	// write into the operator's real keychain.
	custody, err := newFileVaultCustody(Config{
		Service:    DefaultVaultService,
		Dir:        t.TempDir(),
		Passphrase: "unelevated-test-pass",
	})
	if err != nil {
		t.Fatalf("newFileVaultCustody: %v", err)
	}
	broker, err := NewBroker(custody, unelevatedRefusingGate{})
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	return broker
}

func TestApprovalKeyReaderReadsWithoutElevation(t *testing.T) {
	broker := newUnelevatedBroker(t)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	if _, err := broker.Set(context.Background(), ApprovalKeyName, private, SetUpdate); err != nil {
		t.Fatalf("storing the approval key: %v", err)
	}
	// The elevated verb is refused, which is the state a release binary
	// is permanently in.
	if _, err := broker.Get(context.Background(), ApprovalKeyName); err == nil {
		t.Fatal("the elevated Get was not refused; this test proves nothing")
	}
	reader, err := NewApprovalKeyReader(broker)
	if err != nil {
		t.Fatalf("NewApprovalKeyReader: %v", err)
	}
	id, got, err := reader.ApprovalKey(context.Background())
	if err != nil {
		t.Fatalf("ApprovalKey: %v", err)
	}
	if !bytes.Equal(got, private) {
		t.Fatal("the reader returned a different key than the one stored")
	}
	if id != approvalKeyID(public) {
		t.Fatalf("key id = %q, want the digest of the stored key's public half", id)
	}
	if id == "" {
		t.Fatal("the key id is empty")
	}
}

func TestApprovalKeyReaderRefusesAMalformedKey(t *testing.T) {
	broker := newUnelevatedBroker(t)
	if _, err := broker.Set(context.Background(), ApprovalKeyName, []byte("too short"), SetUpdate); err != nil {
		t.Fatalf("storing the malformed key: %v", err)
	}
	reader, err := NewApprovalKeyReader(broker)
	if err != nil {
		t.Fatalf("NewApprovalKeyReader: %v", err)
	}
	_, _, err = reader.ApprovalKey(context.Background())
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("ApprovalKey over a malformed key = %v, want an integrity refusal", err)
	}
}

func TestApprovalKeyReaderRefusesAMissingKey(t *testing.T) {
	reader, err := NewApprovalKeyReader(newUnelevatedBroker(t))
	if err != nil {
		t.Fatalf("NewApprovalKeyReader: %v", err)
	}
	if _, _, err := reader.ApprovalKey(context.Background()); err == nil {
		t.Fatal("ApprovalKey returned a key from an empty vault")
	}
}

func TestUnelevatedReadersRefuseANilBroker(t *testing.T) {
	if _, err := NewApprovalKeyReader(nil); err == nil {
		t.Fatal("an approval key reader was built over no broker")
	}
	if _, err := NewEgressVault(nil); err == nil {
		t.Fatal("an egress vault was built over no broker")
	}
	var reader *ApprovalKeyReader
	if _, _, err := reader.ApprovalKey(context.Background()); err == nil {
		t.Fatal("a nil approval key reader answered a read")
	}
	var vault *EgressVault
	if _, err := vault.List(context.Background()); err == nil {
		t.Fatal("a nil egress vault answered a list")
	}
	if _, err := vault.Get(context.Background(), "ANY"); err == nil {
		t.Fatal("a nil egress vault answered a read")
	}
}

func TestEgressVaultReadsWithoutElevation(t *testing.T) {
	broker := newUnelevatedBroker(t)
	const value = "correct-horse-battery-staple"
	if _, err := broker.Set(context.Background(), "TEAM_PASSPHRASE", []byte(value), SetUpdate); err != nil {
		t.Fatalf("storing the value: %v", err)
	}
	vault, err := NewEgressVault(broker)
	if err != nil {
		t.Fatalf("NewEgressVault: %v", err)
	}
	names, err := vault.List(context.Background())
	if err != nil || len(names) != 1 || names[0] != "TEAM_PASSPHRASE" {
		t.Fatalf("List = (%v, %v)", names, err)
	}
	got, err := vault.Get(context.Background(), "TEAM_PASSPHRASE")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != value {
		t.Fatalf("Get returned %q", got)
	}
	if _, err := vault.Get(context.Background(), "ABSENT_NAME"); err == nil {
		t.Fatal("the egress vault invented a value for a name it does not hold")
	}
}

package secrets

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the broker's granted read path — what it opens,
//   what it refuses, and what it records.
// SPORT: internal/secrets grant-read tests (ADD) — P1-E10-W4-S87-T1.

// grantAuditSink records what the broker told it.
type grantAuditSink struct{ uses []string }

func (s *grantAuditSink) GrantUsed(_ context.Context, grantID, keyRef string) {
	s.uses = append(s.uses, grantID+" "+keyRef)
}

// grantedBroker builds a broker with NO elevation gate (the headless
// shape) over a temp-dir file vault holding one secret.
func grantedBroker(t *testing.T) (*Broker, *Grants, *grantClock) {
	t.Helper()
	dir := t.TempDir()
	custody, err := SelectCustody(Config{
		Service: "cascade-grant-test", Dir: dir, Passphrase: "grant-test-pass",
		Runner: func(context.Context, string, ...string) ([]byte, error) {
			return nil, cascade.New(cascade.KindUnavailable, "no platform keychain in this test")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broker, err := NewBroker(custody, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Set(context.Background(), "PROVIDER_A_KEY", []byte("the-value"), SetUpdate); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileGrantStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	clock := &grantClock{now: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	grants, err := NewGrants(store, clock)
	if err != nil {
		t.Fatal(err)
	}
	return broker, grants, clock
}

// TestAGrantOpensTheReadAnElevationWouldRefuse is the whole point: this
// broker has no gate, so Get refuses, and GetGranted succeeds.
func TestAGrantOpensTheReadAnElevationWouldRefuse(t *testing.T) {
	ctx := context.Background()
	broker, grants, _ := grantedBroker(t)

	if _, err := broker.Get(ctx, "PROVIDER_A_KEY"); err == nil {
		t.Fatal("a gateless broker served an elevated Get")
	}
	if _, err := grants.Issue(ctx, "PROVIDER_A_KEY", time.Hour); err != nil {
		t.Fatal(err)
	}
	value, err := broker.GetGranted(ctx, "PROVIDER_A_KEY", grants)
	if err != nil {
		t.Fatalf("GetGranted: %v", err)
	}
	if string(value) != "the-value" {
		t.Errorf("value = %q", value)
	}
}

// TestGetIsStillRefusedWithAGrantPresent is the property that keeps this
// from being an exemption: a grant never makes the ELEVATED verb succeed.
func TestGetIsStillRefusedWithAGrantPresent(t *testing.T) {
	ctx := context.Background()
	broker, grants, _ := grantedBroker(t)
	if _, err := grants.Issue(ctx, "PROVIDER_A_KEY", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Get(ctx, "PROVIDER_A_KEY"); err == nil {
		t.Fatal("a standing grant made the elevated Get succeed; it must only open GetGranted")
	}
}

// TestAnUngrantedReadNamesTheKeyAndTheRemedy holds the fail-closed message.
func TestAnUngrantedReadNamesTheKeyAndTheRemedy(t *testing.T) {
	ctx := context.Background()
	broker, grants, _ := grantedBroker(t)

	_, err := broker.GetGranted(ctx, "PROVIDER_A_KEY", grants)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	for _, want := range []string{"PROVIDER_A_KEY", "vault grant"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
	if _, err := broker.GetGranted(ctx, "PROVIDER_A_KEY", nil); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("with no register at all: err = %v, want KindUnavailable", err)
	}
}

// TestEveryGrantedReadIsAudited is property 6, and the audit carries no
// value.
func TestEveryGrantedReadIsAudited(t *testing.T) {
	ctx := context.Background()
	broker, grants, _ := grantedBroker(t)
	grant, err := grants.Issue(ctx, "PROVIDER_A_KEY", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sink := &grantAuditSink{}
	audited := broker.WithGrantAudit(sink)

	for i := 0; i < 2; i++ {
		if _, err := audited.GetGranted(ctx, "PROVIDER_A_KEY", grants); err != nil {
			t.Fatal(err)
		}
	}
	if len(sink.uses) != 2 {
		t.Fatalf("audited %d reads, want one record per read", len(sink.uses))
	}
	for _, record := range sink.uses {
		if !strings.Contains(record, grant.ID) || !strings.Contains(record, "PROVIDER_A_KEY") {
			t.Errorf("record = %q, want the grant id and the key name", record)
		}
		if strings.Contains(record, "the-value") {
			t.Errorf("an audit record carries the secret value: %q", record)
		}
	}
}

// TestARevokedGrantStopsTheDaemonOnTheNextRead is the end-to-end of
// property 5 through the broker.
func TestARevokedGrantStopsTheDaemonOnTheNextRead(t *testing.T) {
	ctx := context.Background()
	broker, grants, _ := grantedBroker(t)
	grant, err := grants.Issue(ctx, "PROVIDER_A_KEY", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.GetGranted(ctx, "PROVIDER_A_KEY", grants); err != nil {
		t.Fatal(err)
	}
	if err := grants.Revoke(ctx, grant.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.GetGranted(ctx, "PROVIDER_A_KEY", grants); err == nil {
		t.Fatal("the read still succeeded after the grant was revoked")
	}
}

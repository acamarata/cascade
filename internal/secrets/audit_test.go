// Purpose: tests for the read-only audit report. Every one runs against a
//
//	real file-vault custody and a real quarantine ledger under
//	t.TempDir(), and every one asserts no value reaches the output.
//
// SPORT: SECRETS_AUDIT_REPORT: ADD (tests).

package secrets

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// newAuditDeps builds AuditDeps over real components.
func newAuditDeps(t *testing.T) AuditDeps {
	t.Helper()
	quarantine, err := NewQuarantineStore(t.TempDir(), fixedClock{at: doctorTestNow()})
	if err != nil {
		t.Fatalf("NewQuarantineStore: %v", err)
	}
	return AuditDeps{Broker: newUnelevatedBroker(t), Quarantine: quarantine, Now: doctorTestNow}
}

func TestAuditReportRefusesIncompleteDeps(t *testing.T) {
	full := newAuditDeps(t)
	for _, mutate := range []func(AuditDeps) AuditDeps{
		func(d AuditDeps) AuditDeps { d.Broker = nil; return d },
		func(d AuditDeps) AuditDeps { d.Quarantine = nil; return d },
		func(d AuditDeps) AuditDeps { d.Now = nil; return d },
	} {
		if _, err := BuildAuditReport(context.Background(), mutate(full)); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("an incomplete AuditDeps produced a report: %v", err)
		}
	}
}

func TestAuditReportClassifiesEveryEntry(t *testing.T) {
	deps := newAuditDeps(t)
	const value = "correct-horse-battery-staple"
	seed := map[string]string{
		"TEAM_PASSPHRASE":             value,
		"oauth.acme.primary.record":   "",
		"oauth.acme.primary.access.1": "token-bytes-here",
	}
	seed["oauth.acme.primary.record"] = `{"provider":"acme","account":"primary","exp":"2026-03-04T20:00:00Z"}`
	for name, v := range seed {
		if _, err := deps.Broker.Set(context.Background(), name, []byte(v), SetUpdate); err != nil {
			t.Fatalf("seeding %s: %v", name, err)
		}
	}
	report, err := BuildAuditReport(context.Background(), deps)
	if err != nil {
		t.Fatalf("BuildAuditReport: %v", err)
	}
	kinds := map[string]string{}
	for _, e := range report.Entries {
		kinds[e.Name] = e.Kind
	}
	if kinds["TEAM_PASSPHRASE"] != "secret" ||
		kinds["oauth.acme.primary.record"] != "oauth-record" ||
		kinds["oauth.acme.primary.access.1"] != "oauth-token" {
		t.Fatalf("entry kinds = %v", kinds)
	}
	if len(report.Grants) != 1 || report.Grants[0].Account != "primary" {
		t.Fatalf("grants = %+v", report.Grants)
	}
	if !report.Grants[0].ExpiringSoon || report.Grants[0].Expired {
		t.Fatalf("a grant expiring in under 24h graded as %+v", report.Grants[0])
	}
	if report.PerEntryMetadataAvailable {
		t.Fatal("the report claims per-entry metadata this build cannot source")
	}
	if strings.Contains(report.String(), value) || strings.Contains(report.String(), "token-bytes-here") {
		t.Fatalf("the rendered report carries a stored value:\n%s", report.String())
	}
}

func TestAuditReportGradesEveryGrantState(t *testing.T) {
	now := doctorTestNow()
	for _, tc := range []struct {
		name         string
		exp          string
		expired      bool
		expiringSoon bool
		render       string
	}{
		{"expired", now.Add(-time.Hour).Format(time.RFC3339), true, false, "EXPIRED"},
		{"soon", now.Add(time.Hour).Format(time.RFC3339), false, true, "expires soon"},
		{"valid", now.Add(90 * time.Hour).Format(time.RFC3339), false, false, "valid until"},
		{"none", "0001-01-01T00:00:00Z", false, false, "no declared expiry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := newAuditDeps(t)
			record := `{"provider":"acme","account":"a","exp":"` + tc.exp + `"}`
			if _, err := deps.Broker.Set(context.Background(), "oauth.acme.a.record", []byte(record), SetUpdate); err != nil {
				t.Fatalf("seeding: %v", err)
			}
			report, err := BuildAuditReport(context.Background(), deps)
			if err != nil {
				t.Fatalf("BuildAuditReport: %v", err)
			}
			got := report.Grants[0]
			if got.Expired != tc.expired || got.ExpiringSoon != tc.expiringSoon {
				t.Fatalf("grant = %+v, want expired=%v soon=%v", got, tc.expired, tc.expiringSoon)
			}
			if !strings.Contains(report.String(), tc.render) {
				t.Fatalf("the rendered report does not say %q:\n%s", tc.render, report.String())
			}
		})
	}
}

func TestAuditReportFailsOnAnUnreadableGrant(t *testing.T) {
	deps := newAuditDeps(t)
	if _, err := deps.Broker.Set(context.Background(), "oauth.acme.broken.record", []byte("{not json"), SetUpdate); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if _, err := BuildAuditReport(context.Background(), deps); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("BuildAuditReport over an undecodable record = %v, want an integrity refusal; "+
			"an audit that dropped the grant would understate what the vault holds", err)
	}
}

func TestAuditReportCountsTheQuarantineQueue(t *testing.T) {
	deps := newAuditDeps(t)
	if _, err := deps.Quarantine.Put(DetectionHit{
		Class: ClassAPIKey, Pattern: "test", Offset: 0, Len: 8, Confidence: 0.9, SuggestedName: "A_FINDING",
	}, "audit-test", nil); err != nil {
		t.Fatalf("seeding the queue: %v", err)
	}
	report, err := BuildAuditReport(context.Background(), deps)
	if err != nil {
		t.Fatalf("BuildAuditReport: %v", err)
	}
	if report.QuarantineDepth != 1 {
		t.Fatalf("quarantine depth = %d, want 1", report.QuarantineDepth)
	}
	if report.Backend == "" || !strings.Contains(report.String(), "backend:") {
		t.Fatalf("the report does not name the custody backend:\n%s", report.String())
	}
	if report.GeneratedAt.IsZero() {
		t.Fatal("the report carries no generation timestamp")
	}
}

func TestAuditReportIsIdempotent(t *testing.T) {
	deps := newAuditDeps(t)
	for _, name := range []string{"B_KEY", "A_KEY"} {
		if _, err := deps.Broker.Set(context.Background(), name, []byte("value-bytes"), SetUpdate); err != nil {
			t.Fatalf("seeding: %v", err)
		}
	}
	first, err := BuildAuditReport(context.Background(), deps)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := BuildAuditReport(context.Background(), deps)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.String() != second.String() {
		t.Fatalf("two reports differ:\n%s\n---\n%s", first.String(), second.String())
	}
	if first.Entries[0].Name != "A_KEY" {
		t.Fatalf("entries are not sorted: %+v", first.Entries)
	}
}

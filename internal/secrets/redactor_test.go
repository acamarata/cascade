package secrets

// Purpose: the redactor seam's behaviour, and the end-to-end proof that a
//   secret in an audit event's free-text fields does not reach the stored
//   row. The end-to-end case drives the REAL internal/audit.Log with the
//   REAL redactor over a real SQLite store, and asserts on the persisted
//   bytes rather than on the event it handed in.
// Constraints: Art.7.1 (store under t.TempDir), frozen clock.
// SPORT: AUDIT_REDACTION: ADD (tests).

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/runtime"
	sqlite "github.com/acamarata/cascade/providers/sqlite"
)

// shapedSecret has credential shape, so the detector half of the
// substitution pass recognises it with no vault bound.
// The literal is SPLIT so the source carries no contiguous match:
// GitHub push protection blocks a push containing an AWS key ID
// shape, and this fixture is synthetic but correctly shaped, which
// is the whole point of it (R-14.202). The runtime value is
// unchanged, so the detector still sees a credential.
const shapedSecret = "AKIA" + "7YQ2XPLM4RZV6WTB"

func newTestRedactor(t *testing.T) *Redactor {
	t.Helper()
	detector, err := NewDetector(DefaultRegistry(), DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	redactor, err := NewRedactor(detector)
	if err != nil {
		t.Fatalf("NewRedactor: %v", err)
	}
	return redactor
}

func TestRedactorSubstitutesShapedCredentials(t *testing.T) {
	out, err := newTestRedactor(t).Redact(context.Background(), []byte("key "+shapedSecret+" end"))
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if bytes.Contains(out, []byte(shapedSecret)) {
		t.Fatalf("the redactor left the raw value: %q", out)
	}
}

func TestRedactorEdgeCases(t *testing.T) {
	redactor := newTestRedactor(t)
	out, err := redactor.Redact(context.Background(), nil)
	if err != nil || len(out) != 0 {
		t.Fatalf("empty content must pass through: (%q, %v)", out, err)
	}
	if _, err := NewRedactor(nil); err == nil {
		t.Fatal("a redactor with no detector must be refused")
	}
	var zero *Redactor
	if _, err := zero.Redact(context.Background(), []byte("x")); err == nil {
		t.Fatal("a nil redactor must refuse rather than pass content through")
	}
	if _, err := redactor.Redact(context.Background(), []byte{0xff, 0xfe}); err == nil {
		t.Fatal("content that is not valid UTF-8 must be refused")
	}
}

// TestAuditAppendRedactsThroughTheRealLog is the R-21.235 end-to-end
// case: a secret in `action` and in `explain` must not reach the stored
// record. The assertion reads the row back through the log's own reader.
func TestAuditAppendRedactsThroughTheRealLog(t *testing.T) {
	driver, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "cascade.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })

	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0).UTC())
	log := audit.NewWithRedactor(driver, clock, nil, newTestRedactor(t))

	explain, merr := json.Marshal(map[string]string{"reason": "rotated " + shapedSecret})
	if merr != nil {
		t.Fatal(merr)
	}
	rec, aerr := log.Append(context.Background(), audit.Event{
		Kind: audit.KindVaultAccess, Actor: "tester",
		Action: "vault.rotate " + shapedSecret, Explain: explain,
	})
	if aerr != nil {
		t.Fatalf("Append: %v", aerr)
	}

	stored, xerr := log.Explain(context.Background(), rec.ID)
	if xerr != nil {
		t.Fatalf("Explain: %v", xerr)
	}
	if bytes.Contains([]byte(stored.Record.Action), []byte(shapedSecret)) {
		t.Fatalf("the stored action carries the raw secret: %q", stored.Record.Action)
	}
	if bytes.Contains(stored.Explain, []byte(shapedSecret)) {
		t.Fatalf("the stored explain carries the raw secret: %s", stored.Explain)
	}
}

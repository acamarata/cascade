// Purpose: the credential gate uses the tree's own detector: one canary per
//
//	default-registry class (and per vendor key prefix) is refused in every
//	outcome and finding string input, leaves the stored rows unchanged and
//	never appears in an error or a column. Canaries are assembled at run
//	time by concatenation, never written as one literal.
//
// SPORT: learn/credential/ADD (P1-CAP-02).
package learn

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// registryCanaries returns one credential-shaped value per default
// registry class and per vendor API-key prefix, built at run time.
func registryCanaries() map[string]string {
	body := strings.Repeat("Q7", 16)
	return map[string]string{
		"pem-block":       "-----BEGIN " + "RSA PRIVATE KEY-----\nMIIB" + body + "\n-----END " + "RSA PRIVATE KEY-----",
		"pem-header":      "-----BEGIN " + "PRIVATE KEY-----",
		"api-sk":          "sk" + "-" + body,
		"api-ghp":         "gh" + "p_" + body,
		"api-github-pat":  "github" + "_pat_" + body,
		"api-slack":       "xo" + "xb-" + "1234567890-" + body,
		"api-google-ya29": "ya" + "29." + body,
		"api-google-aiza": "AI" + "za" + body,
		"api-aws":         "AK" + "IA" + "ABCDEFGHIJKLMNOP",
		"api-gitlab":      "gl" + "pat-" + body,
		"api-npm":         "np" + "m_" + body,
		"api-shopify":     "sh" + "pat_" + body,
		"jwt":             "ey" + "JhbGciOiJI" + "." + "eyJzdWIiOiIx" + "." + "c2lnbmF0dXJl",
		"conn-string":     "postgres" + "://admin:" + "hunter2x" + "@db.internal/app",
		"bearer":          "Bea" + "rer " + body,
		"base64-json":     base64.StdEncoding.EncodeToString([]byte(`{"api_` + `token":"` + body + `"}`)),
	}
}

// TestCredentialGateCoversEveryRegistryClass: every canary is flagged by the
// gate itself (so the rows below are not passing on shape alone), and an
// opaque hex id, an ordinary label and an entropy-only hit (a credential
// word beside an opaque run, no registry pattern) are not.
func TestCredentialGateCoversEveryRegistryClass(t *testing.T) {
	for class, canary := range registryCanaries() {
		if !credentialShaped(canary) {
			t.Errorf("class %s: the gate does not flag its canary", class)
		}
	}
	entropyOnly := "key " + "k8Hq2ZpX9vRt4LmW7nYcQ"
	for _, benign := range []string{"0123456789abcdef0123456789abcdef", "tier-1", "job-ok-after", "", entropyOnly} {
		if credentialShaped(benign) {
			t.Errorf("benign value %q flagged as a credential", benign)
		}
	}
}

// outcomeStringFields lists every string-kinded TelemetryOutcome field.
func outcomeStringFields() []string {
	var out []string
	typ := reflect.TypeOf(TelemetryOutcome{})
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Type.Kind() == reflect.String {
			out = append(out, typ.Field(i).Name)
		}
	}
	return out
}

// TestCredentialCanaryEveryRegistryClass: each registry-class canary in each
// outcome string field and each finding string field is refused, the stored
// rows stay exactly the one seeded outcome, and the canary appears in no
// error and no text column.
func TestCredentialCanaryEveryRegistryClass(t *testing.T) {
	fields := outcomeStringFields()
	if len(fields) != 11 {
		t.Fatalf("outcome string fields = %d (%v), want 11", len(fields), fields)
	}
	for class, canary := range registryCanaries() {
		db := newTestOutcomeDB(t)
		ctx := context.Background()
		w := NewSQLiteOutcomeWriter(db, newTestClock())
		if err := w.Record(ctx, baseOutcome("job-cred-seed")); err != nil {
			t.Fatalf("seed outcome: %v", err)
		}
		for _, field := range fields {
			err := w.Record(ctx, withField(field, canary))
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Errorf("class %s in %s: Record did not refuse with KindInvalidInput", class, field)
			} else if strings.Contains(errChainText(err), canary) {
				t.Errorf("class %s in %s: the error chain carries the canary", class, field)
			}
		}
		fw := NewSQLiteFindingWriter(db)
		base := Finding{JobID: "job-cred-seed", Family: FamilyReview, Category: CategoryStyle, Severity: SeverityLow, Count: 1}
		for _, field := range []string{"JobID", "Family", "Category", "Severity"} {
			f := base
			reflect.ValueOf(&f).Elem().FieldByName(field).SetString(canary)
			err := fw.WriteFinding(ctx, f)
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Errorf("class %s in finding %s: WriteFinding did not refuse with KindInvalidInput", class, field)
			} else if strings.Contains(errChainText(err), canary) {
				t.Errorf("class %s in finding %s: the error chain carries the canary", class, field)
			}
		}
		if n := telemetryRowCount(t, db); n != 1 {
			t.Errorf("class %s: rows = %d, want only the seeded outcome", class, n)
		}
		assertCanaryInNoColumn(t, db, class, canary)
	}
	assertMaskedCanaryRowsRefused(t)
}

// maskedCanaryRows returns credential values whose registry hit an earlier,
// overlapping entropy run would win in Detector.Scan's overlap resolution:
// each fits its column's shape and carries a base64 credential blob behind a
// credential-named prefix. Built at run time by concatenation.
func maskedCanaryRows() []struct{ field, canary, value string } {
	blob := `{"api_` + `token":"` + strings.Repeat("Q7", 8) + `"}`
	url := base64.RawURLEncoding.EncodeToString([]byte(blob))
	std := base64.StdEncoding.EncodeToString([]byte(blob))
	return []struct{ field, canary, value string }{
		{"JobID", url, "token" + ":x." + url},
		{"ScopeRef", std, "token" + "=-+" + std},
	}
}

// assertMaskedCanaryRowsRefused: each masked row is flagged by the gate
// itself, refused by Record, leaves only the seeded outcome, and the canary
// is in no error and no column.
func assertMaskedCanaryRowsRefused(t *testing.T) {
	t.Helper()
	for _, row := range maskedCanaryRows() {
		if !credentialShaped(row.value) {
			t.Errorf("masked %s row: the gate does not flag it", row.field)
		}
		db := newTestOutcomeDB(t)
		ctx := context.Background()
		w := NewSQLiteOutcomeWriter(db, newTestClock())
		if err := w.Record(ctx, baseOutcome("job-cred-seed")); err != nil {
			t.Fatalf("seed outcome: %v", err)
		}
		err := w.Record(ctx, withField(row.field, row.value))
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("masked %s row: Record did not refuse with KindInvalidInput", row.field)
		} else if strings.Contains(errChainText(err), row.canary) {
			t.Errorf("masked %s row: the error chain carries the canary", row.field)
		}
		if n := telemetryRowCount(t, db); n != 1 {
			t.Errorf("masked %s row: rows = %d, want only the seeded outcome", row.field, n)
		}
		assertCanaryInNoColumn(t, db, "masked "+row.field, row.canary)
	}
}

// TestCredentialGateAcceptsOpaqueIDs: opaque ids (hex digests in both
// cases, a SHA-1 and a 16-hex id, a UUID in both cases, job- and scope-
// digest ids, 64-character job- and scope- hex ids, a mixed alphanumeric
// run, base64 of non-JSON text) are not flagged, and each stores in JobID
// and in every identifying column.
func TestCredentialGateAcceptsOpaqueIDs(t *testing.T) {
	sum := sha256.Sum256([]byte("opaque"))
	hex64 := hex.EncodeToString(sum[:])
	ids := []string{
		hex64, strings.ToUpper(hex64), hex64[:40], hex64[:16],
		"123e4567-e89b-12d3-a456-426614174000", "job-" + digestID("x"),
		"scope-" + digestID("y"), "a1B2c3D4e5F6g7H8i9J0k1L2m3N4o5P6",
		"123E4567-E89B-12D3-A456-426614174000", "job-" + hex64[:60], "scope-" + hex64[:58],
		base64.StdEncoding.EncodeToString([]byte(strings.Repeat("f", 33))),
	}
	fields := []string{"JobID", "RepoID", "NodeID", "TaskClass", "RiskClass",
		"LaneTier", "RetrievalStrategy", "Component", "ScopeRef"}
	for _, id := range ids {
		if credentialShaped(id) {
			t.Errorf("opaque id %q flagged as a credential", id)
		}
		for _, field := range fields {
			db := newTestOutcomeDB(t)
			o := withField(field, id)
			if err := NewSQLiteOutcomeWriter(db, newTestClock()).Record(context.Background(), o); err != nil {
				t.Errorf("opaque id %q in %s refused: %v", id, field, err)
			}
		}
	}
}

// assertCanaryInNoColumn fails when canary occurs anywhere inside any text
// column of either telemetry table.
func assertCanaryInNoColumn(t *testing.T, db *sql.DB, class, canary string) {
	t.Helper()
	for _, table := range []string{tableTelemetryOutcomes, tableTelemetryFinding} {
		for _, col := range textColumns(t, db, table) {
			var n int
			q := `SELECT COUNT(*) FROM ` + table + ` WHERE instr(` + col + `, ?) > 0`
			if err := db.QueryRow(q, canary).Scan(&n); err != nil {
				t.Fatalf("scan %s.%s: %v", table, col, err)
			}
			if n != 0 {
				t.Errorf("class %s: canary found in %s.%s", class, table, col)
			}
		}
	}
}

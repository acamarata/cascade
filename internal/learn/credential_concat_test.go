// Purpose: a credential-JSON base64 canary glued to id characters (a
//
//	"job-" style prefix of 1..8 characters, a "-x" style suffix of 1..3,
//	or both) is still flagged, because the run class swallows those
//	characters and only a window at the canary's own offset decodes
//	(P1-BF-R88a). Proven on real SQLite for JobID and every identifying
//	field, plus the CR-4 855-case probe shape (every registry class and
//	three base64-JSON variants, bare / "job-"+c / c+"-x", in every outcome
//	and finding string input) with zero leaks. Canaries are assembled at
//	run time, never written as one literal.
//
// SPORT: learn/credential-concat/ADD (P1-CAP-02).
package learn

import (
	"context"
	"database/sql"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// identifyingFields is JobID plus the 8 identifying outcome columns.
var identifyingFields = []string{"JobID", "RepoID", "NodeID", "TaskClass",
	"RiskClass", "LaneTier", "RetrievalStrategy", "Component", "ScopeRef"}

// shortURLCanaries returns raw base64url credential-JSON canaries of 26,
// 43 and 48 characters (lengths 2, 3 and 0 mod 4).
func shortURLCanaries(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, blob := range []string{
		`{"k` + `ey":"` + strings.Repeat("Q7", 4) + `Q"}`,
		`{"api_` + `token":"` + strings.Repeat("Q7", 8) + `"}`,
		`{"api_` + `token":"` + strings.Repeat("Q7", 10) + `"}`,
	} {
		c := base64.RawURLEncoding.EncodeToString([]byte(blob))
		if len(c) < 24 || len(c) > 48 || !credentialShaped(c) {
			t.Fatalf("canary of %d chars: want 24..48 and flagged bare", len(c))
		}
		out = append(out, c)
	}
	return out
}

// concatForms wraps c in every prefix of length 1..8 and suffix of length
// 1..3 from two id-character families, alone and combined.
func concatForms(c string) []string {
	var out []string
	for _, fam := range []struct{ pre, suf string }{{"job-1a2b", "-x9"}, {"x_Q9+/zZ", "_a/"}} {
		for p := 1; p <= len(fam.pre); p++ {
			out = append(out, fam.pre[:p]+c)
			for s := 1; s <= len(fam.suf); s++ {
				out = append(out, fam.pre[:p]+c+fam.suf[:s])
			}
		}
		for s := 1; s <= len(fam.suf); s++ {
			out = append(out, c+fam.suf[:s])
		}
	}
	return out
}

// seededOutcomeDB opens a real db holding only the "job-cred-seed" outcome.
func seededOutcomeDB(t *testing.T) (*sql.DB, *SQLiteOutcomeWriter) {
	t.Helper()
	db := newTestOutcomeDB(t)
	w := NewSQLiteOutcomeWriter(db, newTestClock())
	if err := w.Record(context.Background(), baseOutcome("job-cred-seed")); err != nil {
		t.Fatalf("seed outcome: %v", err)
	}
	return db, w
}

// leaked reports whether err fails to refuse the value or carries canary.
func leaked(err error, canary string) bool {
	return !cascade.HasKind(err, cascade.KindInvalidInput) || strings.Contains(errChainText(err), canary)
}

// TestCredentialDetectsConcatenatedCanary: each concatenated form of each
// short canary is flagged and refused in JobID and every identifying
// field; the db keeps only the seed row and no column holds the canary.
func TestCredentialDetectsConcatenatedCanary(t *testing.T) {
	db, w := seededOutcomeDB(t)
	ctx := context.Background()
	cases := 0
	for _, c := range shortURLCanaries(t) {
		forms := concatForms(c)
		if len(forms) != 70 {
			t.Fatalf("forms = %d, want 70", len(forms))
		}
		for _, v := range forms {
			if !credentialShaped(v) {
				t.Errorf("concatenated canary (%d chars around a %d-char blob) not flagged", len(v), len(c))
			}
			for _, field := range identifyingFields {
				cases++
				if leaked(w.Record(ctx, withField(field, v)), c) {
					t.Errorf("concatenated canary in %s: not refused cleanly", field)
				}
			}
		}
		assertCanaryInNoColumn(t, db, "concat", c)
	}
	if n := telemetryRowCount(t, db); n != 1 {
		t.Errorf("rows = %d, want only the seeded outcome", n)
	}
	t.Logf("concatenated cases = %d", cases)
}

// probeCanaries is CR-4's set: every registry canary plus three
// base64-JSON variants (raw base64url, padded base64url, padded std).
func probeCanaries() map[string]string {
	out := registryCanaries()
	blob := []byte(`{"api_` + `token":"` + strings.Repeat("Q7", 8) + `"}`)
	out["b64-rawurl"] = base64.RawURLEncoding.EncodeToString(blob)
	out["b64-url-padded"] = base64.URLEncoding.EncodeToString(blob)
	out["b64-std-padded"] = base64.StdEncoding.EncodeToString(append(blob, ' ', ' '))
	return out
}

// TestCredentialProbeReportsNoLeak re-runs the CR-4 probe shape: 19
// canaries x 3 forms x (11 outcome + 4 finding fields) = 855 writes, each
// refused with KindInvalidInput and none carrying the canary; afterwards
// only the seed row exists and no column holds any canary.
func TestCredentialProbeReportsNoLeak(t *testing.T) {
	db, w := seededOutcomeDB(t)
	fw := NewSQLiteFindingWriter(db)
	ctx := context.Background()
	fields := outcomeStringFields()
	cases, leaks := 0, 0
	for class, c := range probeCanaries() {
		for _, v := range []string{c, "job-" + c, c + "-x"} {
			for _, field := range fields {
				cases++
				if leaked(w.Record(ctx, withField(field, v)), c) {
					leaks++
					t.Errorf("class %s in %s: not refused cleanly", class, field)
				}
			}
			for _, field := range []string{"JobID", "Family", "Category", "Severity"} {
				f := Finding{JobID: "job-cred-seed", Family: FamilyReview, Category: CategoryStyle, Severity: SeverityLow, Count: 1}
				reflect.ValueOf(&f).Elem().FieldByName(field).SetString(v)
				cases++
				if leaked(fw.WriteFinding(ctx, f), c) {
					leaks++
					t.Errorf("class %s in finding %s: not refused cleanly", class, field)
				}
			}
		}
		assertCanaryInNoColumn(t, db, class, c)
	}
	if cases != 855 || leaks != 0 {
		t.Errorf("probe cases = %d leaks = %d, want 855 and 0", cases, leaks)
	}
	if n := telemetryRowCount(t, db); n != 1 {
		t.Errorf("rows = %d, want only the seeded outcome", n)
	}
	t.Logf("probe cases = %d leaks = %d", cases, leaks)
}

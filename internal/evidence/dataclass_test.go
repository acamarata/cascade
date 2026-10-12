package evidence

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestDataClassStoredTextRoundTrip(t *testing.T) {
	s, _ := newStore(t)
	for _, tc := range []struct {
		word string
		tier provider.SensitivityTier
	}{
		{"public", provider.SensitivityPublic}, {"internal", provider.SensitivityInternal},
		{"confidential", provider.SensitivityRestricted}, {"secret", provider.SensitivityLocalOnly},
	} {
		t.Run(tc.word, func(t *testing.T) { storedClassRoundTrip(t, s, tc.word, tc.tier) })
	}
}

func storedClassRoundTrip(t *testing.T, s *Store, word string, tier provider.SensitivityTier) {
	t.Helper()
	ctx := context.Background()
	c := Claim{ID: word, Statement: "s", Type: ClaimObservedFact, Confidence: 0.5, ProducedBy: ProducedBy{RunID: "run"}, DataClass: new(tier)}
	e := Evidence{ID: word, ClaimID: word, Source: Source{Type: SourceFile, Path: "f.txt", Locator: Locator{Kind: LocatorBytes, Start: 0, End: 1}}, ContentHash: "h", DataClass: new(tier)}
	if err := s.PutClaim(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := s.PutEvidence(ctx, e); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"SELECT data_class FROM " + tableClaim + " WHERE claim_id=?", "SELECT data_class FROM " + tableClaimEvidence + " WHERE evidence_id=?"} {
		var raw string
		if err := s.db.QueryRow(q, word).Scan(&raw); err != nil || raw != word {
			t.Fatalf("stored = %q: %v", raw, err)
		}
	}
	got, err := s.GetClaim(ctx, word)
	if err != nil || got.DataClass == nil || *got.DataClass != tier {
		t.Fatalf("claim = %+v: %v", got, err)
	}
	ev, err := s.EvidenceByID(ctx, word)
	if err != nil || ev.DataClass == nil || *ev.DataClass != tier {
		t.Fatalf("evidence = %+v: %v", ev, err)
	}
	if err := s.PutClaim(ctx, got); err != nil {
		t.Fatal(err)
	}
	if err := s.PutEvidence(ctx, ev); err != nil {
		t.Fatal(err)
	}
	for _, record := range []any{c, e} {
		raw, err := json.Marshal(record)
		if err != nil || !strings.Contains(string(raw), `"data_class":"`+word+`"`) {
			t.Fatalf("json = %s: %v", raw, err)
		}
		switch before := record.(type) {
		case Claim:
			var after Claim
			if err := json.Unmarshal(raw, &after); err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("claim json: %v", err)
			}
		case Evidence:
			var after Evidence
			if err := json.Unmarshal(raw, &after); err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("evidence json: %v", err)
			}
		}
	}
}

func TestUnsetDataClassFailsClosed(t *testing.T) {
	s, _ := newStore(t)
	c := Claim{ID: "unset", Statement: "s", Type: ClaimObservedFact, Confidence: 0.5, ProducedBy: ProducedBy{RunID: "run"}}
	e := Evidence{ID: "unset", ClaimID: "unset", Source: Source{Type: SourceFile, Path: "f.txt", Locator: Locator{Kind: LocatorBytes, Start: 0, End: 1}}, ContentHash: "h"}
	for _, err := range []error{c.Validate(), e.Validate(), s.PutClaim(context.Background(), c), s.PutEvidence(context.Background(), e)} {
		if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "data_class") {
			t.Fatalf("unset error: %v", err)
		}
	}
	capture := NewCapturer(nil, nil, s)
	if _, _, err := capture.Capture(context.Background(), "job", "exec", nil, []byte("data")); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil capture: %v", err)
	}
	for _, table := range []string{tableClaim, tableClaimEvidence} {
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rows = %d: %v", count, err)
		}
	}
}

func TestDataClassJSONRefusals(t *testing.T) {
	for _, into := range []any{new(Claim), new(Evidence)} {
		for _, raw := range []string{`{}`, `{"data_class":""}`} {
			if err := json.Unmarshal([]byte(raw), into); err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(into)
			if err != nil || !strings.Contains(string(b), `"data_class":""`) {
				t.Fatalf("unset = %s: %v", b, err)
			}
		}
		for _, word := range []string{"local-only", "restricted", "Secret", " secret", "unknown"} {
			err := json.Unmarshal([]byte(`{"data_class":"`+word+`"}`), into)
			if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), word) {
				t.Fatalf("bad word %q: %v", word, err)
			}
		}
		if err := json.Unmarshal([]byte("{"), into); err == nil {
			t.Fatal("malformed json accepted")
		}
	}
	for _, record := range []any{Claim{DataClass: new(provider.SensitivityTier(9))}, Evidence{DataClass: new(provider.SensitivityTier(9))}} {
		if _, err := json.Marshal(record); err == nil {
			t.Fatal("invalid tier encoded")
		}
	}
}

package learn

// Purpose: ValidateNoIdentifiers over the identifying fixtures and clean
//   opaque ids, and ExportFilter's provenance stripping.
// SPORT: learn/privacy_test (P1-LRN-01).

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

type privacyFixtures struct {
	Refused []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"refused"`
	Clean []string `json:"clean"`
}

func TestValidateNoIdentifiersRejects(t *testing.T) {
	isolateHome(t)
	raw, err := os.ReadFile("testdata/privacy/identifiers.json")
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var fx privacyFixtures
	if err := json.Unmarshal(raw, &fx); err != nil || len(fx.Refused) < 7 || len(fx.Clean) == 0 {
		t.Fatalf("parse fixtures (%d refused, %d clean): %v", len(fx.Refused), len(fx.Clean), err)
	}
	for _, c := range fx.Clean {
		if err := ValidateNoIdentifiers("Label", c); err != nil {
			t.Errorf("clean value %q refused: %v", c, err)
		}
	}
	for _, r := range fx.Refused {
		err := ValidateNoIdentifiers("Scope", r.Value)
		var ce *cascade.Error
		if err == nil || !errors.As(err, &ce) || ce.Kind != cascade.KindInvalidInput {
			t.Errorf("%s: %v, want KindInvalidInput", r.Name, err)
			continue
		}
		if !strings.HasPrefix(ce.Msg, `learn: field "Scope" must not carry `) || strings.Contains(err.Error(), r.Value) {
			t.Errorf("%s: message %q must name the field and never the value", r.Name, ce.Msg)
		}
	}
	wantErr(t, ValidateNoIdentifiers("Label", "someone@host-a.invalid"), cascade.KindInvalidInput,
		`learn: field "Label" must not carry an e-mail address or user@host`)
	wantErr(t, ValidateNoIdentifiers("Label", `\\share-a\work`), cascade.KindInvalidInput,
		`learn: field "Label" must not carry a UNC path`)
	tok := "ghp_" + strings.Repeat("A1b2C3d4", 5)
	wantErr(t, ValidateNoIdentifiers("Label", tok), cascade.KindInvalidInput,
		`learn: field "Label" must not carry a credential-shaped value`)
}

func TestExportFilterStripsProvenance(t *testing.T) {
	isolateHome(t)
	at := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	c := LearnedConfig{ID: "c1", Source: SourceRef{Kind: SourceDetector, ID: "det-01"}, Label: "l",
		Areas: []DenylistArea{AreaEgressRules}, Evidence: []EvidenceRef{{Kind: "k", ID: "ev-1"}},
		Rollback: &RollbackSnapshot{PreviousValue: "300", SnapshotAt: at}}
	stripped := ExportFilter{}.Apply(c)
	if stripped.Source != (SourceRef{}) || stripped.Rollback == nil || stripped.Rollback.PreviousValue != "" ||
		!stripped.Rollback.SnapshotAt.Equal(at) || stripped.ID != "c1" || stripped.Label != "l" {
		t.Fatalf("ExportFilter{}.Apply = %+v, want Source and PreviousValue stripped, the rest kept", stripped)
	}
	if c.Source.ID != "det-01" || c.Rollback.PreviousValue != "300" {
		t.Fatal("ExportFilter modified its input")
	}
	stripped.Areas[0], stripped.Evidence[0].ID = "x", "y"
	if c.Areas[0] != AreaEgressRules || c.Evidence[0].ID != "ev-1" {
		t.Fatal("ExportFilter shares slices with its input")
	}
	kept := ExportFilter{IncludeProvenance: true}.Apply(c)
	if kept.Source != c.Source || kept.Rollback.PreviousValue != "300" || kept.Rollback == c.Rollback {
		t.Fatalf("IncludeProvenance lost provenance or aliased the snapshot: %+v", kept)
	}
	if none := (ExportFilter{}).Apply(LearnedConfig{ID: "c2"}); none.Rollback != nil {
		t.Fatal("ExportFilter invented a rollback snapshot")
	}
}

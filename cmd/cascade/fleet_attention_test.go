package main

import (
	"testing"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/supervision"
)

// TestFleetAttentionAliasHidden proves the top-level `attention` alias
// is hidden from --help while still resolving, per R-14.92 and
// 07-CLI-COMMAND-TREE's "hidden top-level aliases kept: cascade top /
// sessions / attention" note.
func TestFleetAttentionAliasHidden(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	found, _, err := root.Find([]string{"attention"})
	if err != nil {
		t.Fatalf("attention alias not found: %v", err)
	}
	if !found.Hidden {
		t.Fatal("cascade attention alias must be Hidden (07 §fleet consolidation note)")
	}
}

// TestFleetAttentionMountedUnderFleet proves `cascade fleet attention`
// exposes all three plan-named verbs.
func TestFleetAttentionMountedUnderFleet(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	for _, verb := range []string{"list", "ack", "open"} {
		if _, _, err := root.Find([]string{"fleet", "attention", verb}); err != nil {
			t.Fatalf("fleet attention %s not found: %v", verb, err)
		}
	}
}

// TestFleetAttentionAlias_IdenticalConstruction mirrors
// TestFleetJournalAlias_IdenticalConstruction's exact pattern: the hidden
// alias and the canonical subtree are built from the same constructor,
// so they can never drift.
func TestFleetAttentionAlias_IdenticalConstruction(t *testing.T) {
	deps := fleetSessionsDeps{}
	canonical := newFleetAttentionCmd(deps)
	alias := newFleetAttentionCmd(deps)
	alias.Use = "attention"
	alias.Hidden = true

	for _, verb := range []string{"list", "ack", "open"} {
		if _, _, err := canonical.Find([]string{verb}); err != nil {
			t.Fatalf("canonical attention %s not found: %v", verb, err)
		}
		if _, _, err := alias.Find([]string{verb}); err != nil {
			t.Fatalf("alias attention %s not found: %v", verb, err)
		}
	}
}

// TestAttentionRowRenderShapes exercises the render helpers directly
// (attentionRowsFrom/attentionRowFrom/attentionDetailFrom), so table and
// detail views are covered without a live daemon.
func TestAttentionRowRenderShapes(t *testing.T) {
	item := supervision.AttentionItem{
		ID:        "id-1",
		Kind:      supervision.KindStall,
		SourceRef: "sess-1",
		ScopeRef:  supervision.ScopeRef{Kind: scope.ScopeKindSession, ID: "sess-1"},
		Priority:  2,
		CreatedAt: 1000,
	}
	rows := attentionRowsFrom([]supervision.AttentionItem{item})
	if len(rows) != 1 || rows[0].ID != "id-1" {
		t.Fatalf("attentionRowsFrom = %+v, want one row for id-1", rows)
	}
	if rows.String() == "" {
		t.Error("attentionRows.String() is empty")
	}
	if attentionRowFrom(item).String() == "" {
		t.Error("attentionRowFrom(...).String() is empty")
	}
	detail := attentionDetailFrom(item)
	if detail.ID != "id-1" || detail.ScopeKind != string(scope.ScopeKindSession) {
		t.Errorf("attentionDetailFrom = %+v, want id-1/session", detail)
	}
	if detail.String() == "" {
		t.Error("attentionDetail.String() is empty")
	}

	now := int64(2000)
	ackedItem := item
	ackedItem.AckedAt = &now
	ackedDetail := attentionDetailFrom(ackedItem)
	if ackedDetail.AckedAt == nil {
		t.Error("attentionDetailFrom did not carry AckedAt through")
	}
	if ackedDetail.String() == "" {
		t.Error("acked attentionDetail.String() is empty")
	}
}

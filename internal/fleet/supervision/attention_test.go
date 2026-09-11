package supervision

import (
	"testing"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestKindValid(t *testing.T) {
	valid := []Kind{KindStall, KindElevationRefused, KindPolicyAsk, KindError}
	for _, k := range valid {
		if !k.Valid() {
			t.Errorf("Kind(%q).Valid() = false, want true", k)
		}
	}
	invalid := []Kind{"", "bogus", "STALL"}
	for _, k := range invalid {
		if k.Valid() {
			t.Errorf("Kind(%q).Valid() = true, want false", k)
		}
	}
}

func TestAttentionItemValidate(t *testing.T) {
	valid := AttentionItem{Kind: KindStall, SourceRef: "session-1", ScopeRef: ScopeRef{Kind: scope.ScopeKindSession, ID: "s1"}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() on a valid item = %v, want nil", err)
	}

	cases := []struct {
		name string
		item AttentionItem
	}{
		{"invalid kind", AttentionItem{Kind: "bogus", SourceRef: "x", ScopeRef: ScopeRef{Kind: scope.ScopeKindSession, ID: "s1"}}},
		{"empty source_ref", AttentionItem{Kind: KindStall, SourceRef: "", ScopeRef: ScopeRef{Kind: scope.ScopeKindSession, ID: "s1"}}},
		{"invalid scope kind", AttentionItem{Kind: KindStall, SourceRef: "x", ScopeRef: ScopeRef{Kind: "bogus", ID: "s1"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.item.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error")
			}
			if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
				t.Errorf("Validate() kind = %v, ok=%v, want KindInvalidInput", kind, ok)
			}
		})
	}
}

func TestAttentionItemAcked(t *testing.T) {
	unacked := AttentionItem{}
	if unacked.Acked() {
		t.Error("zero-value item Acked() = true, want false")
	}
	now := int64(1000)
	acked := AttentionItem{AckedAt: &now}
	if !acked.Acked() {
		t.Error("item with AckedAt set Acked() = false, want true")
	}
}

func TestFilterMatches(t *testing.T) {
	stall := KindStall
	item := AttentionItem{Kind: KindStall, Priority: 5}
	ackedNow := int64(1)
	ackedItem := AttentionItem{Kind: KindStall, Priority: 5, AckedAt: &ackedNow}

	cases := []struct {
		name string
		f    Filter
		item AttentionItem
		want bool
	}{
		{"default excludes acked", Filter{}, ackedItem, false},
		{"include_acked includes acked", Filter{IncludeAcked: true}, ackedItem, true},
		{"kind filter matches", Filter{KindFilter: &stall}, item, true},
		{"kind filter rejects", Filter{KindFilter: func() *Kind { k := KindError; return &k }()}, item, false},
		{"priority filter matches", Filter{Priority: func() *int { p := 5; return &p }()}, item, true},
		{"priority filter rejects", Filter{Priority: func() *int { p := 1; return &p }()}, item, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.matches(tc.item); got != tc.want {
				t.Errorf("matches() = %v, want %v", got, tc.want)
			}
		})
	}
}

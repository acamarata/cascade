package corpus

import "testing"

// TestVisibilityClass_StoredSpellings pins each class's stored spelling as
// a literal. The stored value is what the team carrier reads back, so a
// silent rename here would be a silent reclassification there.
func TestVisibilityClass_StoredSpellings(t *testing.T) {
	want := map[VisibilityClass]string{
		VisibilityPrivate:    "private",
		VisibilityScopeLocal: "scope-local",
		VisibilityShared:     "shared",
		VisibilityTeam:       "team",
	}
	for class, spelling := range want {
		if string(class) != spelling {
			t.Errorf("class stored as %q, want %q", string(class), spelling)
		}
	}
	if len(want) != 4 {
		t.Fatalf("the model defines four visibility classes, this test covers %d", len(want))
	}
}

// TestVisibilityClass_Valid covers the closed set and the fail-closed
// direction for everything outside it.
func TestVisibilityClass_Valid(t *testing.T) {
	for _, v := range []VisibilityClass{"private", "scope-local", "shared", "team"} {
		if !v.Valid() {
			t.Errorf("%q should be a valid visibility class", string(v))
		}
	}
	for _, v := range []VisibilityClass{"", "public", "Private", "scope_local", "world", "team "} {
		if v.Valid() {
			t.Errorf("%q should not be a valid visibility class", string(v))
		}
		if got := v.String(); got != "invalid" {
			t.Errorf("VisibilityClass(%q).String() = %q, want invalid", string(v), got)
		}
	}
	if got := VisibilityTeam.String(); got != "team" {
		t.Errorf("VisibilityTeam.String() = %q, want team", got)
	}
}

// TestResolveVisibility_NarrowerSideWins proves a record can never reach
// further than its corpus, and that an unreadable class on either side
// collapses to the narrowest class rather than the widest.
func TestResolveVisibility_NarrowerSideWins(t *testing.T) {
	cases := []struct {
		name        string
		record      VisibilityClass
		corpusClass VisibilityClass
		want        VisibilityClass
	}{
		{"equal", VisibilityShared, VisibilityShared, VisibilityShared},
		{"record narrower", VisibilityPrivate, VisibilityTeam, VisibilityPrivate},
		{"corpus narrower", VisibilityTeam, VisibilityPrivate, VisibilityPrivate},
		{"corpus caps at scope-local", VisibilityShared, VisibilityScopeLocal, VisibilityScopeLocal},
		{"record unset", "", VisibilityTeam, VisibilityPrivate},
		{"corpus unset", VisibilityTeam, "", VisibilityPrivate},
		{"record unknown", "world-readable", VisibilityTeam, VisibilityPrivate},
		{"corpus unknown", VisibilityTeam, "world-readable", VisibilityPrivate},
		{"both unset", "", "", VisibilityPrivate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveVisibility(tc.record, tc.corpusClass); got != tc.want {
				t.Errorf("resolveVisibility(%q, %q) = %q, want %q",
					string(tc.record), string(tc.corpusClass), string(got), string(tc.want))
			}
		})
	}
}

// TestVisibilityRank_OrdersByReach pins the ordering resolveVisibility
// compares on, including the below-private rank an unrecognized class
// gets.
func TestVisibilityRank_OrdersByReach(t *testing.T) {
	ordered := []VisibilityClass{"nonsense", VisibilityPrivate, VisibilityScopeLocal, VisibilityShared, VisibilityTeam}
	for i := 1; i < len(ordered); i++ {
		if visibilityRank(ordered[i-1]) >= visibilityRank(ordered[i]) {
			t.Errorf("%q must rank below %q", string(ordered[i-1]), string(ordered[i]))
		}
	}
}

// TestVisibilityClass_Reaches walks each class against a session whose
// chain holds an ancestor and whose edges reach one unrelated scope. It is
// the per-class reach table: private stops at the owning scope, scope-local
// stops at the chain, shared and team add declared edges and nothing else,
// and an unrecognized class reaches nothing at all.
func TestVisibilityClass_Reaches(t *testing.T) {
	m := Membership{
		Scope: "project:one",
		Chain: []ScopeRef{"user:local", "workspace:w", "project:one"},
		Edges: []Edge{{Kind: EdgeDependsOn, Target: "project:linked"}},
	}
	cases := []struct {
		class VisibilityClass
		owner ScopeRef
		want  bool
	}{
		{VisibilityPrivate, "project:one", true},
		{VisibilityPrivate, "workspace:w", false},
		{VisibilityPrivate, "project:linked", false},
		{VisibilityPrivate, "project:other", false},
		{VisibilityScopeLocal, "project:one", true},
		{VisibilityScopeLocal, "workspace:w", true},
		{VisibilityScopeLocal, "project:linked", false},
		{VisibilityScopeLocal, "project:other", false},
		{VisibilityShared, "workspace:w", true},
		{VisibilityShared, "project:linked", true},
		{VisibilityShared, "project:other", false},
		{VisibilityTeam, "project:linked", true},
		{VisibilityTeam, "project:other", false},
		{"", "project:one", false},
		{"world-readable", "project:one", false},
		{VisibilityPrivate, "", false},
		{VisibilityShared, "", false},
	}
	for _, tc := range cases {
		got := tc.class.reaches(m, tc.owner)
		if got != tc.want {
			t.Errorf("VisibilityClass(%q).reaches(owner=%q) = %v, want %v",
				string(tc.class), string(tc.owner), got, tc.want)
		}
	}
}

// TestVisibilityPrivate_DoesNotMatchAnEmptyScopeOnBothSides guards the
// specific equality trap: a membership with an empty scope and a record
// with an empty scope are equal as strings, and equality alone would
// authorize. Validity has to be part of the decision.
func TestVisibilityPrivate_DoesNotMatchAnEmptyScopeOnBothSides(t *testing.T) {
	if VisibilityPrivate.reaches(Membership{Scope: ""}, "") {
		t.Fatal("two empty scope references must not authorize each other")
	}
}

// TestPrivacyClass_StoredSpellings pins the three tiers as literals.
func TestPrivacyClass_StoredSpellings(t *testing.T) {
	want := map[PrivacyClass]string{
		PrivacyPersonal: "personal",
		PrivacyGlobal:   "global",
		PrivacyProject:  "project",
	}
	for class, spelling := range want {
		if string(class) != spelling {
			t.Errorf("privacy class stored as %q, want %q", string(class), spelling)
		}
		if !class.Valid() {
			t.Errorf("%q should be a valid privacy class", spelling)
		}
		if class.String() != spelling {
			t.Errorf("%q.String() = %q", spelling, class.String())
		}
	}
	for _, bad := range []PrivacyClass{"", "unknown", "Personal", "secret"} {
		if bad.Valid() {
			t.Errorf("%q should not be a valid privacy class", string(bad))
		}
		if got := bad.String(); got != "invalid" {
			t.Errorf("PrivacyClass(%q).String() = %q, want invalid", string(bad), got)
		}
	}
}

// TestResolvePrivacy_PersonalWins proves the leak-safe direction: personal
// on either side, or an unreadable value on either side, resolves to
// personal.
func TestResolvePrivacy_PersonalWins(t *testing.T) {
	cases := []struct {
		record      PrivacyClass
		corpusClass PrivacyClass
		want        PrivacyClass
	}{
		{PrivacyProject, PrivacyProject, PrivacyProject},
		{PrivacyPersonal, PrivacyProject, PrivacyPersonal},
		{PrivacyProject, PrivacyPersonal, PrivacyPersonal},
		{PrivacyGlobal, PrivacyProject, PrivacyGlobal},
		{PrivacyProject, PrivacyGlobal, PrivacyGlobal},
		{PrivacyGlobal, PrivacyGlobal, PrivacyGlobal},
		{"", PrivacyProject, PrivacyPersonal},
		{PrivacyProject, "", PrivacyPersonal},
		{"unknown", PrivacyProject, PrivacyPersonal},
		{PrivacyProject, "unknown", PrivacyPersonal},
	}
	for _, tc := range cases {
		if got := resolvePrivacy(tc.record, tc.corpusClass); got != tc.want {
			t.Errorf("resolvePrivacy(%q, %q) = %q, want %q",
				string(tc.record), string(tc.corpusClass), string(got), string(tc.want))
		}
	}
}

// TestPrivacyClass_Permits covers the entitlement table, including the two
// unreadable directions: unreadable content is never permitted, and an
// unreadable entitlement permits nothing.
func TestPrivacyClass_Permits(t *testing.T) {
	cases := []struct {
		entitlement PrivacyClass
		content     PrivacyClass
		want        bool
	}{
		{PrivacyPersonal, PrivacyPersonal, true},
		{PrivacyPersonal, PrivacyProject, true},
		{PrivacyPersonal, PrivacyGlobal, true},
		{PrivacyProject, PrivacyPersonal, false},
		{PrivacyGlobal, PrivacyPersonal, false},
		{PrivacyProject, PrivacyProject, true},
		{PrivacyGlobal, PrivacyProject, true},
		{"", PrivacyProject, false},
		{"unknown", PrivacyProject, false},
		{"", PrivacyPersonal, false},
		{PrivacyPersonal, "", false},
		{PrivacyPersonal, "unknown", false},
	}
	for _, tc := range cases {
		if got := tc.entitlement.permits(tc.content); got != tc.want {
			t.Errorf("PrivacyClass(%q).permits(%q) = %v, want %v",
				string(tc.entitlement), string(tc.content), got, tc.want)
		}
	}
}

// TestStore_Query_PersonalContentNeedsPersonalEntitlement proves the
// privacy flag rides the same record surface as scope membership and is
// enforced on the same call.
func TestStore_Query_PersonalContentNeedsPersonalEntitlement(t *testing.T) {
	s := seededStore(t)
	for _, entitlement := range []PrivacyClass{PrivacyProject, PrivacyGlobal, "", "unknown"} {
		got, err := s.Query(Query{Membership: ownMembership(), Entitlement: entitlement})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		for _, r := range got {
			if r.Privacy == PrivacyPersonal {
				t.Errorf("entitlement %q received personal record %s", string(entitlement), r.ID)
			}
		}
		if entitlement.Valid() && len(got) != 2 {
			t.Errorf("entitlement %q saw %d records, want the 2 non-personal ones", string(entitlement), len(got))
		}
		if !entitlement.Valid() && len(got) != 0 {
			t.Errorf("an unreadable entitlement saw %d records, want none", len(got))
		}
	}
}

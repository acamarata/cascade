package policy

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

// classDenyList is the injected I/S-17.T4 seam under test conditions: a
// fixed set of (class, action) pairs, or a failure. It is a test double
// and lives only in a _test.go file; the shipped path takes the real
// engine.
type classDenyList struct {
	denied map[string]bool
	fail   bool
}

// Denied satisfies the DenyLister half of the interface.
func (d *classDenyList) Denied(_ context.Context, action string) (bool, error) {
	return d.denied[action], nil
}

// ContainsClass is the class-keyed query CreateStandingGrant consults.
func (d *classDenyList) ContainsClass(_ context.Context, class ActionClass, action string) (bool, error) {
	if d.fail {
		return false, errors.New("the deny-list is unreadable")
	}
	return d.denied[class.String()+"|"+action], nil
}

// Add, Remove and List complete the DenyListEngine interface; the creation
// path never calls them.
func (d *classDenyList) Add(context.Context, string, ActionClass) error { return nil }
func (d *classDenyList) Remove(context.Context, string) error           { return nil }
func (d *classDenyList) List(context.Context) ([]DenyRule, error)       { return nil, nil }

// recordingGrants counts writes so a test can prove a refusal touched no
// storage. It wraps a real StoreGrants so the row that IS written goes
// through the one store.
type recordingGrants struct {
	inner  GrantStore
	writes int
}

// Grant records the call and delegates.
func (r *recordingGrants) Grant(ctx context.Context, g Grant) error {
	r.writes++
	return r.inner.Grant(ctx, g)
}

// Revoke, Check and List delegate unchanged.
func (r *recordingGrants) Revoke(ctx context.Context, s Subject, c string) error {
	return r.inner.Revoke(ctx, s, c)
}
func (r *recordingGrants) Check(ctx context.Context, req CheckRequest) (Decision, error) {
	return r.inner.Check(ctx, req)
}
func (r *recordingGrants) List(ctx context.Context, s Subject) ([]Grant, error) {
	return r.inner.List(ctx, s)
}

// standingFixture bundles the real grant store, the deny-list double and
// the frozen clock.
type standingFixture struct {
	grants *recordingGrants
	deny   *classDenyList
	clock  *testkit.FrozenClock
	deps   StandingGrantDeps
}

// standingCapabilityName is the capability the fixture registers and the
// standing grants are written against.
const standingCapabilityName = "workspace.write"

// standingBase is the instant the fixture's clock is frozen at.
var standingBase = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// newStandingFixture builds the fixture over a REAL SQLite-backed grant
// store, so a row this test writes is a row the evaluation stack could
// read back through the same API (Art.2: no in-memory double).
func newStandingFixture(t *testing.T) *standingFixture {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "cascade.db"))
	if err != nil {
		t.Fatalf("opening the real SQLite store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := NewMemoryRegistry()
	if aerr := reg.Add(context.Background(), Capability{
		Name: standingCapabilityName, Desc: "write to the workspace",
		DefaultPolicy: ClassWorkspaceMutation,
	}); aerr != nil {
		t.Fatalf("registering the capability: %v", aerr)
	}
	clock := testkit.NewFrozenClock(standingBase)
	inner, err := NewStoreGrants(db, reg, clock)
	if err != nil {
		t.Fatalf("building the real grant store: %v", err)
	}
	f := &standingFixture{
		grants: &recordingGrants{inner: inner},
		deny:   &classDenyList{denied: map[string]bool{}},
		clock:  clock,
	}
	f.deps = StandingGrantDeps{Grants: f.grants, DenyList: f.deny, Clock: f.clock}
	return f
}

// sampleStandingGrant is a well-formed grant on a bridgeable class.
func sampleStandingGrant(t *testing.T) StandingGrant {
	t.Helper()
	id, err := cascade.NewID()
	if err != nil {
		t.Fatalf("minting a grant id: %v", err)
	}
	return StandingGrant{
		GrantID:     id,
		ActionClass: ClassWorkspaceMutation,
		Action:      "workspace.write",
		Capability:  standingCapabilityName,
		Scope:       "repo:/workspace",
		Grantee:     Subject{Kind: SubjectUser, ID: "owner"},
		Exp:         standingBase.Add(time.Hour),
	}
}

// TestStandingGrantPersistsThroughGrantStore proves R-21.237: the row is
// written through the ONE GrantStore and is readable back through the same
// API, with no second store and no second reader type.
func TestStandingGrantPersistsThroughGrantStore(t *testing.T) {
	f := newStandingFixture(t)
	g := sampleStandingGrant(t)
	if err := CreateStandingGrant(context.Background(), f.deps, g); err != nil {
		t.Fatalf("CreateStandingGrant: %v", err)
	}
	rows, err := f.grants.List(context.Background(), g.Grantee)
	if err != nil {
		t.Fatalf("reading the grant back through the store: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("the store holds %d rows, want the one standing grant", len(rows))
	}
	row := rows[0]
	if row.Capability != standingCapabilityName {
		t.Errorf("the row landed on capability %q", row.Capability)
	}
	if row.EffectiveVerdict() != VerdictAllow {
		t.Errorf("the row's verdict is %v, want an explicit allow", row.EffectiveVerdict())
	}
	if row.Conditions["standing_grant_id"] != g.GrantID.String() {
		t.Errorf("the row does not carry its standing-grant id")
	}
	if row.Conditions["scope"] != g.Scope || row.Conditions["action"] != g.Action {
		t.Errorf("the row lost its scope or action: %v", row.Conditions)
	}
	if !row.ExpiresAt.Equal(g.Exp) {
		t.Errorf("the row expires at %s, want %s", row.ExpiresAt, g.Exp)
	}
}

// TestStandingGrant_DenyListRefused proves both guards refuse BEFORE any
// storage call, and that a deny-list that cannot answer refuses too. The
// elevation-class cases iterate the complete §5.14 enumeration.
func TestStandingGrant_DenyListRefused(t *testing.T) {
	ctx := context.Background()
	f := newStandingFixture(t)
	denied := sampleStandingGrant(t)
	f.deny.denied[ClassWorkspaceMutation.String()+"|workspace.write"] = true
	if err := CreateStandingGrant(ctx, f.deps, denied); !errors.Is(err, ErrDeniedClass) {
		t.Errorf("a deny-listed action returned %v, want ErrDeniedClass", err)
	}
	if f.grants.writes != 0 {
		t.Errorf("a refused standing grant reached storage %d times", f.grants.writes)
	}

	unreadable := newStandingFixture(t)
	unreadable.deny.fail = true
	if err := CreateStandingGrant(ctx, unreadable.deps, sampleStandingGrant(t)); !errors.Is(err, ErrDeniedClass) {
		t.Errorf("an unreadable deny-list returned %v, want ErrDeniedClass", err)
	}
	if unreadable.grants.writes != 0 {
		t.Error("a grant was written while the deny-list could not be consulted")
	}

	elevated := newStandingFixture(t)
	for _, verb := range specElevationVerbs {
		g := sampleStandingGrant(t)
		g.Action = verb
		if err := CreateStandingGrant(ctx, elevated.deps, g); !errors.Is(err, ErrDeniedClass) {
			t.Errorf("standing grant on the elevation-class verb %q returned %v, want ErrDeniedClass", verb, err)
		}
	}
	if elevated.grants.writes != 0 {
		t.Errorf("an elevation-class standing grant reached storage %d times", elevated.grants.writes)
	}
}

// TestStandingGrantRefusesMalformed is the fail-closed door on the input
// itself: a grant missing any field the guards need REFUSES, and a missing
// collaborator refuses rather than skipping the guard it would have run.
func TestStandingGrantRefusesMalformed(t *testing.T) {
	ctx := context.Background()
	f := newStandingFixture(t)
	valid := sampleStandingGrant(t)
	for _, tc := range []struct {
		name  string
		mut   func(*StandingGrant)
		deps  StandingGrantDeps
		wantW int
	}{
		{"no grant id", func(g *StandingGrant) { g.GrantID = "" }, f.deps, 0},
		{"malformed grant id", func(g *StandingGrant) { g.GrantID = "short" }, f.deps, 0},
		{"unset action class", func(g *StandingGrant) { g.ActionClass = 0 }, f.deps, 0},
		{"out-of-range class", func(g *StandingGrant) { g.ActionClass = ActionClass(200) }, f.deps, 0},
		{"no action", func(g *StandingGrant) { g.Action = "" }, f.deps, 0},
		{"no expiry", func(g *StandingGrant) { g.Exp = time.Time{} }, f.deps, 0},
		{"no store", func(*StandingGrant) {}, StandingGrantDeps{DenyList: f.deny, Clock: f.clock}, 0},
		{"no deny-list", func(*StandingGrant) {}, StandingGrantDeps{Grants: f.grants, Clock: f.clock}, 0},
		{"no clock", func(*StandingGrant) {}, StandingGrantDeps{Grants: f.grants, DenyList: f.deny}, 0},
	} {
		g := valid
		tc.mut(&g)
		if err := CreateStandingGrant(ctx, tc.deps, g); !errors.Is(err, ErrDeniedClass) {
			t.Errorf("%s: CreateStandingGrant = %v, want ErrDeniedClass", tc.name, err)
		}
		if f.grants.writes != tc.wantW {
			t.Fatalf("%s: %d writes reached storage", tc.name, f.grants.writes)
		}
	}
}

// TestStandingGrantRefusesUnregisteredCapability proves the one store's
// own guard still applies: a standing grant naming a capability nobody
// registered is refused by the store, not written and denied later.
func TestStandingGrantRefusesUnregisteredCapability(t *testing.T) {
	f := newStandingFixture(t)
	g := sampleStandingGrant(t)
	g.Capability = "never.registered"
	if err := CreateStandingGrant(context.Background(), f.deps, g); err == nil {
		t.Fatal("a standing grant on an unregistered capability was written")
	}
}

package nodes

import (
	"reflect"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the §D-11 credential rules — that a static key can
//   never be planned onto a dispatch, and that a per-dispatch token is
//   bound narrowly enough to be safe to hand to another machine.
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T2.

var credNow = time.Unix(1700000000, 0).UTC()

// TestAStaticKeyLaneAlwaysRelays is the rule that keeps long-lived secrets
// off other machines. A static-key lane is not refused — it relays — but no
// plan it produces may ever carry a grant to ship.
func TestAStaticKeyLaneAlwaysRelays(t *testing.T) {
	plan, err := PlanCredentials(CredentialStatic, "job-1", "node-a", "openai",
		"vault/openai", []string{"chat"}, credNow, time.Minute)
	if err != nil {
		t.Fatalf("PlanCredentials: %v", err)
	}
	if !plan.RelayThroughController {
		t.Error("a static-key lane was not marked relay-only")
	}
	if plan.Grant != nil {
		t.Fatal("a static-key lane produced a token grant to ship to a node")
	}
}

// TestAnUnknownCredentialKindRelays is the fail-closed reading: a kind this
// build does not recognize must relay rather than be assumed shippable.
func TestAnUnknownCredentialKindRelays(t *testing.T) {
	plan, err := PlanCredentials(CredentialKind("magic"), "job-1", "node-a", "openai",
		"vault/openai", []string{"chat"}, credNow, time.Minute)
	if err != nil {
		t.Fatalf("PlanCredentials: %v", err)
	}
	if !plan.RelayThroughController || plan.Grant != nil {
		t.Fatalf("an unrecognized credential kind produced %+v, want relay-only with no grant", plan)
	}
}

// TestAScopedGrantIsBoundAndShortLived pins every binding. A grant missing
// any of them would outlive, out-scope or out-travel its dispatch.
func TestAScopedGrantIsBoundAndShortLived(t *testing.T) {
	plan, err := PlanCredentials(CredentialScoped, "job-1", "node-a", "github",
		"vault/github", []string{"read", "write"}, credNow, 10*time.Minute)
	if err != nil {
		t.Fatalf("PlanCredentials: %v", err)
	}
	if plan.RelayThroughController {
		t.Error("a scoped lane was marked relay-only")
	}
	g := plan.Grant
	if g == nil {
		t.Fatal("a scoped lane produced no grant")
	}
	if g.JobID != "job-1" || g.NodeID != "node-a" || g.Audience != "github" {
		t.Errorf("grant = %+v, want it bound to the job, node and audience", *g)
	}
	if !g.ExpiresAt.Equal(credNow.Add(10 * time.Minute)) {
		t.Errorf("expiry = %v", g.ExpiresAt)
	}
}

// TestAGrantNeverCarriesSecretMaterial is the assertion that makes this
// type safe to log and journal: it names a vault key and never a value.
//
// It asserts over the FIELD SET, not over a sample secret. The earlier form
// joined five named fields and searched them for a constant that nothing
// ever put there — it could not fail, and its own comment claimed that a
// field added later would be caught, which was the opposite of true: a new
// field would not have been in the join at all. There is no code path that
// puts a secret VALUE into this struct, so the only thing worth pinning is
// that the struct's shape has not grown one.
func TestAGrantNeverCarriesSecretMaterial(t *testing.T) {
	g, err := NewTokenGrant("job-1", "node-a", "github", "vault/github",
		[]string{"read"}, credNow, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// The bindings, and nothing else. A field outside this set is not
	// necessarily a secret — but it has not been reviewed for whether it
	// is one, and this grant is written to a journal.
	bindings := map[string]bool{
		"JobID": true, "NodeID": true, "Audience": true,
		"Verbs": true, "VaultKey": true, "ExpiresAt": true,
	}
	typ := reflect.TypeOf(g)
	for i := range typ.NumField() {
		if name := typ.Field(i).Name; !bindings[name] {
			t.Errorf("TokenGrant grew field %q. This struct is journalled and logged, so a field "+
				"holding credential material would put it at rest. Add it to the reviewed set "+
				"here once you have checked it carries a reference and not a value.", name)
		}
	}
	if len(bindings) > typ.NumField() {
		t.Errorf("%d reviewed field(s) for %d actual: a binding was REMOVED, which makes the "+
			"grant broader, not narrower", len(bindings), typ.NumField())
	}
	if g.VaultKey != "vault/github" {
		t.Errorf("vault key = %q, want the reference the value is fetched by", g.VaultKey)
	}
}

// TestALifetimeIsClampedNotHonoured proves an over-long request is reduced
// rather than granted or refused: the safe failure for asking too much
// lifetime is getting less of it.
func TestALifetimeIsClampedNotHonoured(t *testing.T) {
	for _, requested := range []time.Duration{0, -time.Minute, 48 * time.Hour} {
		g, err := NewTokenGrant("job-1", "node-a", "github", "vault/github",
			[]string{"read"}, credNow, requested)
		if err != nil {
			t.Fatalf("requested %v: %v", requested, err)
		}
		if want := credNow.Add(MaxTokenLifetime); !g.ExpiresAt.Equal(want) {
			t.Errorf("requested %v: expiry = %v, want it clamped to %v", requested, g.ExpiresAt, want)
		}
	}
}

// TestAnUnboundGrantIsRefused proves every binding is required. A grant
// missing one is not a weaker grant, it is an unscoped one.
func TestAnUnboundGrantIsRefused(t *testing.T) {
	full := []any{"job-1", "node-a", "github", "vault/github"}
	for i := range full {
		args := append([]any(nil), full...)
		args[i] = "  "
		_, err := NewTokenGrant(args[0].(string), args[1].(string), args[2].(string),
			args[3].(string), []string{"read"}, credNow, time.Minute)
		if err == nil {
			t.Errorf("a grant with binding %d blank was accepted", i)
			continue
		}
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
			t.Errorf("binding %d: kind = %v (ok=%v), want KindInvalidInput", i, kind, ok)
		}
	}
	if _, err := NewTokenGrant("job-1", "node-a", "github", "vault/github",
		nil, credNow, time.Minute); err == nil {
		t.Error("a grant permitting no verbs was accepted")
	}
}

// TestAGrantPermitsOnlyWhatItNames is the enforcement half: the bindings
// have to actually gate a call, not merely be recorded on the struct.
func TestAGrantPermitsOnlyWhatItNames(t *testing.T) {
	g, err := NewTokenGrant("job-1", "node-a", "github", "vault/github",
		[]string{"read"}, credNow, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !g.Permits("node-a", "github", "read", credNow) {
		t.Fatal("the grant refused the call it was minted for")
	}
	for _, tc := range []struct{ name, node, audience, verb string }{
		{"another node", "node-b", "github", "read"},
		{"another audience", "node-a", "openai", "read"},
		{"another verb", "node-a", "github", "write"},
	} {
		if g.Permits(tc.node, tc.audience, tc.verb, credNow) {
			t.Errorf("the grant permitted %s", tc.name)
		}
	}
	if g.Permits("node-a", "github", "read", g.ExpiresAt.Add(time.Second)) {
		t.Error("the grant permitted a call after it expired")
	}
	if g.Valid(g.ExpiresAt) {
		t.Error("the grant reported itself valid at its own expiry instant")
	}
}

// TestANoCredentialLaneShipsNothing covers the remaining kind.
func TestANoCredentialLaneShipsNothing(t *testing.T) {
	plan, err := PlanCredentials(CredentialNone, "job-1", "node-a", "", "", nil, credNow, 0)
	if err != nil {
		t.Fatalf("PlanCredentials: %v", err)
	}
	if plan.Grant != nil || plan.RelayThroughController {
		t.Fatalf("a no-credential lane produced %+v", plan)
	}
}

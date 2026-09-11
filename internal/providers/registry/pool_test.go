package registry

import (
	"context"
	"strings"
	"testing"
)

// upsertPoolMember upserts a standalone provider plus its single lane,
// joined to pool with the given initial state.
func upsertPoolMember(t *testing.T, reg *Registry, providerName, pool string, state LaneState) {
	t.Helper()
	ctx := context.Background()
	rec := sampleProvider(providerName)
	if err := reg.UpsertProvider(ctx, rec); err != nil {
		t.Fatalf("UpsertProvider(%s): %v", providerName, err)
	}
	if err := reg.UpsertLane(ctx, LaneRecord{
		LaneName: providerName, ProviderName: providerName, PoolMembership: pool,
		Capacity: CapacityAPICredit, State: state,
	}); err != nil {
		t.Fatalf("UpsertLane(%s): %v", providerName, err)
	}
}

// TestKeyPoolAdvanceRoundRobin is the acceptance criterion's table-driven
// golden: with >=3 pool members, sequential AdvancePoolIndex calls cycle
// through all members and wrap.
func TestKeyPoolAdvanceRoundRobin(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	pool := "gf-pool"
	for _, name := range []string{"gf-alpha", "gf-beta", "gf-gamma"} {
		upsertPoolMember(t, reg, name, pool, LaneStateAvailable)
	}

	members, err := reg.ListPool(ctx, pool)
	if err != nil {
		t.Fatalf("ListPool: %v", err)
	}
	if len(members) != 3 {
		t.Fatalf("ListPool returned %d members, want 3", len(members))
	}
	if members[0].ProviderName != "gf-alpha" || members[1].ProviderName != "gf-beta" || members[2].ProviderName != "gf-gamma" {
		t.Fatalf("ListPool order = %+v, want sorted by provider_name", members)
	}

	var sequence []string
	for i := 0; i < 7; i++ {
		picked, err := reg.AdvancePoolIndex(ctx, pool)
		if err != nil {
			t.Fatalf("AdvancePoolIndex call %d: %v", i, err)
		}
		sequence = append(sequence, picked)
	}
	want := []string{"gf-alpha", "gf-beta", "gf-gamma", "gf-alpha", "gf-beta", "gf-gamma", "gf-alpha"}
	for i := range want {
		if sequence[i] != want[i] {
			t.Fatalf("dispatch sequence = %v, want %v", sequence, want)
		}
	}
	assertGolden(t, "pool_roundrobin.golden.json", sequence)
}

// TestAdvancePoolIndexSkipsUnhealthyMembers proves a pool never silently
// promotes an exhausted/unavailable member: only the LaneStateAvailable
// member is ever selected.
func TestAdvancePoolIndexSkipsUnhealthyMembers(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	pool := "mixed-pool"
	upsertPoolMember(t, reg, "healthy-one", pool, LaneStateAvailable)
	upsertPoolMember(t, reg, "dead-one", pool, LaneStateExhausted)
	upsertPoolMember(t, reg, "auth-broken", pool, LaneStateAuthRequired)

	for i := 0; i < 4; i++ {
		picked, err := reg.AdvancePoolIndex(ctx, pool)
		if err != nil {
			t.Fatalf("AdvancePoolIndex call %d: %v", i, err)
		}
		if picked != "healthy-one" {
			t.Fatalf("AdvancePoolIndex picked %q, want the only available member healthy-one", picked)
		}
	}
}

// TestAdvancePoolIndexExhaustedPoolIsTypedRefusal is the non-negotiable:
// a pool with zero available members returns a typed, distinguishable
// refusal, never a nil pick or an empty string.
func TestAdvancePoolIndexExhaustedPoolIsTypedRefusal(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	pool := "all-dead-pool"
	upsertPoolMember(t, reg, "dead-a", pool, LaneStateExhausted)
	upsertPoolMember(t, reg, "dead-b", pool, LaneStateAuthRequired)
	upsertPoolMember(t, reg, "dead-c", pool, LaneStateUnknown)

	picked, err := reg.AdvancePoolIndex(ctx, pool)
	if err == nil {
		t.Fatalf("AdvancePoolIndex on an all-unavailable pool should have failed, picked %q", picked)
	}
	if picked != "" {
		t.Fatalf("AdvancePoolIndex on refusal should return an empty pick, got %q", picked)
	}
	if !isPoolExhausted(err) {
		t.Fatalf("AdvancePoolIndex refusal should be ErrPoolExhausted (KindQuotaExhausted); got %v", err)
	}
}

// TestAdvancePoolIndexEmptyPoolIsTypedRefusal covers the other exhaustion
// path: a pool name with no members at all.
func TestAdvancePoolIndexEmptyPoolIsTypedRefusal(t *testing.T) {
	reg := newTestRegistry(t)
	if _, err := reg.AdvancePoolIndex(context.Background(), "never-registered-pool"); err == nil || !isPoolExhausted(err) {
		t.Fatalf("AdvancePoolIndex on an unknown pool should return ErrPoolExhausted; got %v", err)
	}
}

func isPoolExhausted(err error) bool {
	return err != nil && strings.Contains(err.Error(), "pool exhausted")
}

package consult

import (
	"errors"
	"testing"
)

// TestRouteT0PlanQuotaOKUsesHaiku55 verifies that when quota is OK and BurnAccount is true,
// RouteT0Plan returns Destination "Haiku 5.5". It also verifies that when BurnAccount is false,
// RouteT0Plan returns a zero route and ErrNoBurnAccount.
func TestRouteT0PlanQuotaOKUsesHaiku55(t *testing.T) {
	// BurnAccount=true with explicit non-Opus model
	req1 := T0Request{
		Tier:             "T0",
		Model:            "GC2",
		NativeQuotaShort: false,
		BurnAccount:      true,
	}
	route1, err1 := RouteT0Plan(req1)
	if err1 != nil {
		t.Fatalf("expected no error for BurnAccount=true, got %v", err1)
	}
	if route1.Destination != "Haiku 5.5" {
		t.Fatalf("expected Destination 'Haiku 5.5', got %q", route1.Destination)
	}
	if route1.Account != "burn" {
		t.Fatalf("expected Account 'burn', got %q", route1.Account)
	}

	// BurnAccount=false yields zero route and ErrNoBurnAccount
	req2 := T0Request{
		Tier:             "T0",
		Model:            "GC2",
		NativeQuotaShort: false,
		BurnAccount:      false,
	}
	route2, err2 := RouteT0Plan(req2)
	if !errors.Is(err2, ErrNoBurnAccount) {
		t.Fatalf("expected ErrNoBurnAccount, got %v", err2)
	}
	if route2 != (T0Route{}) {
		t.Fatalf("expected zero route, got %+v", route2)
	}
}

// TestRouteT0PlanUsesBurnAccountWhenHaiku verifies that when quota is OK and burn is available,
// RouteT0Plan returns Account "burn".
func TestRouteT0PlanUsesBurnAccountWhenHaiku(t *testing.T) {
	req := T0Request{
		Tier:             "T0",
		Model:            "Haiku 5.5",
		NativeQuotaShort: false,
		BurnAccount:      true,
	}
	route, err := RouteT0Plan(req)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if route.Destination != "Haiku 5.5" {
		t.Fatalf("expected Destination 'Haiku 5.5', got %q", route.Destination)
	}
	if route.Account != "burn" {
		t.Fatalf("expected Account 'burn', got %q", route.Account)
	}
}

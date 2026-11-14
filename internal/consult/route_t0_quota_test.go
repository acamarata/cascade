package consult

import (
	"errors"
	"testing"
)

// TestRouteT0PlanQuotaShortUsesGC2 verifies that NativeQuotaShort routes to GC2.
func TestRouteT0PlanQuotaShortUsesGC2(t *testing.T) {
	gc2 := T0Route{Destination: "GC2", Account: ""}
	tests := []struct {
		name    string
		req     T0Request
		want    T0Route
		wantErr error
	}{
		{"empty model, quota short", T0Request{Tier: "T0", Model: "", NativeQuotaShort: true}, gc2, nil},
		{"GC2 model, quota short, burn account", T0Request{Tier: "T0", Model: "GC2", NativeQuotaShort: true, BurnAccount: true}, gc2, nil},
		{"Haiku 5.5 model, quota short", T0Request{Tier: "T0", Model: "Haiku 5.5", NativeQuotaShort: true}, gc2, nil},
		{"invalid tier", T0Request{Tier: "T1", NativeQuotaShort: true}, T0Route{}, ErrInvalidT0Request},
		{"invalid model", T0Request{Tier: "T0", Model: "GPT-4", NativeQuotaShort: true}, T0Route{}, ErrInvalidT0Request},
		{"whitespace model", T0Request{Tier: "T0", Model: " GC2 ", NativeQuotaShort: true}, T0Route{}, ErrInvalidT0Request},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RouteT0Plan(tt.req)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("RouteT0Plan() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("RouteT0Plan() = %v, want %v", got, tt.want)
			}
		})
	}
}

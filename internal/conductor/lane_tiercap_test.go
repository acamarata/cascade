package conductor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestLaneTierCapAdmitsFailClosed(t *testing.T) {
	ctx := context.Background()
	tiers := []provider.SensitivityTier{
		provider.SensitivityPublic, provider.SensitivityInternal,
		provider.SensitivityRestricted, provider.SensitivityLocalOnly,
	}
	errCap := func(context.Context, LaneID, LaneType) (provider.SensitivityTier, string, error) {
		return provider.SensitivityLocalOnly, "x", errors.New("boom")
	}
	noReason := func(context.Context, LaneID, LaneType) (provider.SensitivityTier, string, error) {
		return provider.SensitivityRestricted, "", nil
	}
	publicCap := func(context.Context, LaneID, LaneType) (provider.SensitivityTier, string, error) {
		return provider.SensitivityPublic, "declared", nil
	}
	for _, req := range tiers {
		for name, c := range map[string]LaneTierCap{"nil": nil, "error": errCap, "no-reason": noReason} {
			ok, flag := admitLaneTierCap(ctx, c, "lane-a", LaneExternalAPI, req)
			if ok {
				t.Fatalf("%s cap admitted %s", name, req)
			}
			if !strings.HasPrefix(flag, "ceiling:"+req.String()+":removed:lane-a(") {
				t.Fatalf("%s cap flag = %q", name, flag)
			}
		}
		if ok, flag := admitLaneTierCap(ctx, conductorTestCap, "lane-a", LaneExternalAPI, req); !ok || flag != "" {
			t.Fatalf("test cap refused %s: %q", req, flag)
		}
	}
	if ok, _ := admitLaneTierCap(ctx, publicCap, "lane-a", LaneExternalAPI, provider.SensitivityPublic); !ok {
		t.Fatal("public cap refused public")
	}
	ok, flag := admitLaneTierCap(ctx, publicCap, "lane-a", LaneExternalAPI, provider.SensitivityInternal)
	if ok || flag != "ceiling:internal:removed:lane-a(declared)" {
		t.Fatalf("public cap over internal = %v %q", ok, flag)
	}

	for name, opts := range map[string][]RouterOption{"absent": nil, "nil": {WithLaneTierCap(nil)}} {
		r, err := NewDaemonRouter(nil, nil, &fakeEventPublisher{}, nil, nil, opts...)
		if r != nil || !errors.Is(err, ErrLaneTierCapMissing) {
			t.Fatalf("%s: NewDaemonRouter = %v, %v", name, r, err)
		}
		if k, _ := cascade.KindOf(err); k != cascade.KindInvalidInput {
			t.Fatalf("%s: kind = %v", name, k)
		}
		if !strings.Contains(err.Error(), "no LaneTierCap supplied") {
			t.Fatalf("%s: message = %q", name, err.Error())
		}
	}
}

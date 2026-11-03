package gateway

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestQuotaUnimplementedSourceRefused(t *testing.T) {
	accts, err := ParseAccounts(map[string]any{"gw": map[string]any{
		"base_url": "https://gateway.invalid", "key_env": "GATEWAY_TOKEN",
		"usage": map[string]any{"source": "headers", "window": "5h", "utilization_header": "x-used"},
	}})
	if err != nil || len(accts) != 1 {
		t.Fatalf("ParseAccounts = %v, %v", accts, err)
	}
	src := accts[0].Usage
	for name, got := range map[string]error{
		"check":    CheckUsageSource(src),
		"headers":  second(DecodeHeaders(src, map[string]string{"x-used": "10"}, time.Unix(1, 0))),
		"endpoint": second(DecodeEndpoint(src, []byte(`{}`), time.Unix(1, 0))),
	} {
		if !errors.Is(got, ErrUsageSourceUnsupported) || got.Error() != ErrUsageSourceUnsupported.Error() {
			t.Fatalf("%s: err = %v, want ErrUsageSourceUnsupported", name, got)
		}
		var ce *cascade.Error
		if !errors.As(got, &ce) || ce.Kind != cascade.KindUnsupported {
			t.Fatalf("%s: kind = %v, want KindUnsupported", name, got)
		}
	}
	if err := CheckUsageSource(UsageSource{Kind: UsageSourceEndpoint}); err != nil {
		t.Fatalf("endpoint source refused: %v", err)
	}
}

func second[T any](_ T, err error) error { return err }

// endpointSrc is an endpoint source over the given pointers.
func endpointSrc(used, limit, util, resets string) UsageSource {
	return UsageSource{Kind: UsageSourceEndpoint, Window: "5h", Path: "/usage",
		UsedPointer: used, LimitPointer: limit, UtilizationPointer: util, ResetsAtPointer: resets}
}

func TestDecodeEndpointPointers(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	body := []byte(`{"w":[{"a/b":{"m~n":40}},{"lim":160}],"pct":12.5,"at":"2026-09-06T17:00:00Z","sec":1800003600}`)
	obs, err := DecodeEndpoint(endpointSrc("/w/0/a~1b/m~0n", "/w/1/lim", "/pct", "/at"), body, now)
	if err != nil {
		t.Fatalf("DecodeEndpoint: %v", err)
	}
	if obs.Used != 40 || obs.Limit != 160 || obs.UtilizationPct != 12.5 || !obs.ObservedAt.Equal(now) ||
		!obs.ResetsAt.Equal(time.Date(2026, 9, 6, 17, 0, 0, 0, time.UTC)) || obs.Window != "5h" {
		t.Fatalf("obs = %+v", obs)
	}
	obs, err = DecodeEndpoint(endpointSrc("/w/0/a~1b/m~0n", "/w/1/lim", "", "/sec"), body, now)
	if err != nil || obs.UtilizationPct != 25 || !obs.ResetsAt.Equal(time.Unix(1_800_003_600, 0).UTC()) {
		t.Fatalf("derived obs = %+v, %v", obs, err)
	}
	for name, src := range map[string]UsageSource{
		"empty":     endpointSrc("", "", "", ""),
		"missing":   endpointSrc("/nope", "/w/9/lim", "/w/x", "/pct/deeper"),
		"null leaf": endpointSrc("/n", "/w/-1", "/w/1/lim/x", ""),
	} {
		obs, err := DecodeEndpoint(src, []byte(`{"n":null,"w":[1,{"lim":2}],"pct":3}`), now)
		if err != nil || obs.Used != -1 || obs.Limit != -1 || obs.UtilizationPct != -1 || !obs.ResetsAt.IsZero() {
			t.Fatalf("%s: obs = %+v, %v; want every figure unknown", name, obs, err)
		}
	}
}

func TestDecodeEndpointRefusesBadBodies(t *testing.T) {
	now := time.Unix(1, 0)
	for name, tc := range map[string]struct {
		src  UsageSource
		body string
	}{
		"not json":       {endpointSrc("/u", "", "", ""), `{`},
		"used string":    {endpointSrc("/u", "", "", ""), `{"u":"7"}`},
		"limit negative": {endpointSrc("", "/l", "", ""), `{"l":-1}`},
		"util bool":      {endpointSrc("", "", "/p", ""), `{"p":true}`},
		"resets bad":     {endpointSrc("", "", "", "/r"), `{"r":"tomorrow"}`},
		"resets zero":    {endpointSrc("", "", "", "/r"), `{"r":0}`},
		"resets object":  {endpointSrc("", "", "", "/r"), `{"r":{}}`},
	} {
		_, err := DecodeEndpoint(tc.src, []byte(tc.body), now)
		var ce *cascade.Error
		if !errors.As(err, &ce) || ce.Kind != cascade.KindIntegrity {
			t.Fatalf("%s: err = %v, want KindIntegrity", name, err)
		}
		if strings.Contains(err.Error(), tc.body) && tc.body != `{` {
			t.Fatalf("%s: error carries body content: %v", name, err)
		}
	}
}

package conductor

// Purpose (this file): hostile BaseURL spellings that must never classify as
//   controller-local, and the refusals restricted work meets over them.
//
// SPORT: conductor.lane-classify (TEST) - P1-SEC-02.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// anyNonLocal marks a case whose exact class depends on how the running Go
// version's net/url treats a malformed host (error -> unresolved, accepted ->
// external); the invariant is only that it is never controller-local.
var anyNonLocal = LaneType(250)

// hostileCases: each BaseURL classifies as a lane that is NEVER
// controller-local, and restricted work over it refuses by default.
var hostileCases = []struct {
	name    string
	baseURL string
	want    LaneType
}{
	{"domain starting with 127.", "http://127.evil.com:8080", LaneExternalAPI},
	{"localhost subdomain", "http://localhost.evil.com", LaneExternalAPI},
	{"unix url naming a remote host", "unix://api.evil.com/run/cascade.sock", LaneExternalAPI},
	{"userinfo disguise", "http://127.0.0.1@evil.com/v1", LaneExternalAPI},
	// A dotted quad that is NOT in 127.0.0.0/8 is an ordinary external
	// host: pinning the first octet, so "any IPv4 literal is local" fails.
	{"private LAN address", "http://10.0.0.5:11434", LaneExternalAPI},
	{"other private LAN address", "http://192.168.1.10:8080", LaneExternalAPI},
	{"public address", "http://8.8.8.8", LaneExternalAPI},
	{"126.x neighbour of loopback", "http://126.255.255.255", LaneExternalAPI},
	{"128.x neighbour of loopback", "http://128.0.0.1", LaneExternalAPI},
	// Non-canonical spellings: Go's proxy selection does not treat these
	// as loopback, so a request could leave the machine via HTTP_PROXY.
	{"zero-padded loopback", "http://127.000.000.001", LaneExternalAPI},
	{"single leading zero loopback", "http://127.0.0.01:8080", LaneExternalAPI},
	{"double zero octet loopback", "http://127.00.0.1", LaneExternalAPI},
	{"empty base url", "", LaneUnresolved},
	{"unbracketed IPv6 loopback with an empty port", "http://::1:", anyNonLocal},
	{"unbracketed IPv6 loopback", "http://::1", anyNonLocal},
	{"unbracketed IPv6 loopback with a port", "http://::1:8080", anyNonLocal},
	{"IPv4 loopback with an empty port", "http://127.0.0.1:", LaneExternalAPI},
	{"localhost with an empty port", "http://localhost:", LaneExternalAPI},
}

// TestClassifyLaneHostileHosts classifies every hostileCases URL and routes
// restricted work over it. An unresolvable lane (empty BaseURL) stays refused
// even when it is named in the permit list.
func TestClassifyLaneHostileHosts(t *testing.T) {
	for _, tc := range hostileCases {
		t.Run(tc.name, func(t *testing.T) { checkHostileCase(t, tc.baseURL, tc.want) })
	}
}

func checkHostileCase(t *testing.T, baseURL string, want LaneType) {
	got := ClassifyLane(provider.ProviderInfo{Name: "p", BaseURL: baseURL})
	if got == LaneControllerLocal {
		t.Fatalf("ClassifyLane(%q) = controller-local", baseURL)
	}
	if want != anyNonLocal && got != want {
		t.Fatalf("ClassifyLane(%q) = %s, want %s", baseURL, got, want)
	}
	reg := &fakeRegistry{
		providers: []provider.ProviderInfo{{Name: "prov", BaseURL: baseURL, HealthStatus: "healthy"}},
		lanes:     []provider.LaneInfo{{LaneName: "lane-h", ProviderName: "prov", State: "active"}},
	}
	r := NewRouter(reg, &fakeQuota{order: []LaneID{"lane-h"}}, nil, nil)
	req := chatReq()
	req.Sensitivity = provider.SensitivityRestricted
	_, err := r.Select(context.Background(), req)
	if err == nil || !errors.Is(err, ErrSensitivityViolation) || !strings.Contains(err.Error(), `"lane-h"`) {
		t.Fatalf("restricted work over %q: err = %v, want ErrSensitivityViolation naming lane-h", baseURL, err)
	}
	if got != LaneUnresolved {
		return
	}
	// A permit does not make an unresolvable lane routable.
	WithRestrictedPermits(permitsOf(t, "lane-h"))(r)
	_, err = r.Select(context.Background(), req)
	if err == nil || !errors.Is(err, ErrSensitivityViolation) || !strings.Contains(err.Error(), "explicit base URL") {
		t.Fatalf("a permit on the unresolved lane %q: err = %v, want a refusal telling the operator to set a base URL", baseURL, err)
	}
}

// TestRefusalHintNamesTheKeyInAMixedSet: with an unresolved lane sorting
// before a permittable external one, the refusal must still name the key.
func TestRefusalHintNamesTheKeyInAMixedSet(t *testing.T) {
	reg := &fakeRegistry{
		providers: []provider.ProviderInfo{
			{Name: "pa", BaseURL: "", HealthStatus: "healthy"},
			{Name: "pb", BaseURL: "https://b.example.com", HealthStatus: "healthy"},
		},
		lanes: []provider.LaneInfo{
			{LaneName: "lane-a", ProviderName: "pa", State: "active"},
			{LaneName: "lane-b", ProviderName: "pb", State: "active"},
		},
	}
	r := NewRouter(reg, &fakeQuota{order: []LaneID{"lane-a", "lane-b"}}, nil, nil)
	req := chatReq()
	req.Sensitivity = provider.SensitivityRestricted
	_, err := r.Select(context.Background(), req)
	if err == nil || !errors.Is(err, ErrSensitivityViolation) {
		t.Fatalf("err = %v, want ErrSensitivityViolation", err)
	}
	for _, want := range []string{"conductor.restricted_external_lanes", "explicit base URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err.Error(), want)
		}
	}
}

// TestRefusalHintLocalOnlyUnresolved pins the local-only wording: only a
// loopback or unix-socket URL helps at that tier.
func TestRefusalHintLocalOnlyUnresolved(t *testing.T) {
	reg := &fakeRegistry{
		providers: []provider.ProviderInfo{{Name: "pa", BaseURL: "", HealthStatus: "healthy"}},
		lanes:     []provider.LaneInfo{{LaneName: "lane-a", ProviderName: "pa", State: "active"}},
	}
	r := NewRouter(reg, &fakeQuota{order: []LaneID{"lane-a"}}, nil, nil)
	ctx := ContextWithThreadPrivacy(context.Background(), ThreadPrivacy{ThreadID: "th-l", Mode: provider.SensitivityLocalOnly})
	_, err := r.Select(ctx, chatReq())
	if err == nil || !errors.Is(err, ErrSensitivityViolation) || !strings.Contains(err.Error(), "loopback or unix-socket base URL") {
		t.Fatalf("err = %v, want a refusal asking for a loopback or unix-socket base URL", err)
	}
}

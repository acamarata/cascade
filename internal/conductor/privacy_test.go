package conductor

// Purpose (this file): every cell of the (privacy_mode x lane_type) table
//   privacy.go decides, plus the two properties a table alone cannot
//   prove - that a refusal reaches the caller as ErrSensitivityViolation
//   NAMING the thread, and that a refused request never reaches dispatch.
//
// SPORT: conductor.privacy (TEST) — P1-E20-W5-S44-T2.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// allModes is every tier the table is total over, plus one that is not a
// tier at all: §5.16 says an unset mode is restricted, so the table must
// answer for values outside the four as well as for the four.
var allModes = []struct {
	name string
	mode provider.SensitivityTier
}{
	{"local-only", provider.SensitivityLocalOnly},
	{"restricted", provider.SensitivityRestricted},
	{"internal", provider.SensitivityInternal},
	{"public", provider.SensitivityPublic},
	{"out-of-range", provider.SensitivityTier(200)},
}

// The three declared lane types plus one that is not declared at all: an
// undeclared LaneType must be answered as strictly as an unresolved one,
// so the table is total over LaneType's whole range too.
var allLaneTypes = []LaneType{LaneControllerLocal, LaneExternalAPI, LaneUnresolved, LaneType(99)}

// TestPrivacyModeTable asserts EVERY (privacy_mode x lane_type) cell, by
// naming each expected answer here rather than by re-deriving it from the
// same switch the implementation uses. A table test that computes its own
// expectation from the code under test asserts nothing.
func TestPrivacyModeTable(t *testing.T) {
	// Columns, in allLaneTypes order: local, external, unresolved, undeclared.
	want := map[string][4]bool{
		"local-only":   {true, false, false, false},
		"restricted":   {true, true, false, false},
		"internal":     {true, true, false, false},
		"public":       {true, true, true, true},
		"out-of-range": {true, true, false, false}, // read as restricted
	}
	cells := 0
	for _, m := range allModes {
		row, ok := want[m.name]
		if !ok {
			t.Fatalf("no expectation declared for mode %s", m.name)
		}
		for i, lane := range allLaneTypes {
			cells++
			if got := PrivacyAllows(m.mode, lane); got != row[i] {
				t.Errorf("PrivacyAllows(%s, %s) = %v, want %v", m.name, lane, got, row[i])
			}
		}
	}
	if cells != len(allModes)*len(allLaneTypes) {
		t.Fatalf("checked %d cells, want %d: the table did not resolve", cells, len(allModes)*len(allLaneTypes))
	}
}

// TestPrivacyModeClassifyLane pins the three computable lane types, and in
// particular the distinction computedLocalityIsLocal collapses: "another
// machine" and "no machine anyone can name" are both not-local, and only
// one of them is an ordinary external lane.
func TestPrivacyModeClassifyLane(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baseURL string
		want    LaneType
	}{
		{"loopback v4", "http://127.0.0.1:8080", LaneControllerLocal},
		{"loopback name", "http://localhost:1234", LaneControllerLocal},
		{"loopback v6", "http://[::1]:1234", LaneControllerLocal},
		{"unix socket", "unix:///var/run/cascade.sock", LaneControllerLocal},
		{"remote host", "https://api.example.com", LaneExternalAPI},
		// A REGISTRABLE DOMAIN that starts with "127." — the fail-open the
		// independent review of this ticket found. Its owner points it
		// wherever they like; a prefix test called it controller-local and
		// every local-only gate built on that predicate waved it through.
		{"domain masquerading as loopback", "http://127.evil.com", LaneExternalAPI},
		{"subdomain of a loopback-looking domain", "http://127.0.0.1.evil.com", LaneExternalAPI},
		{"loopback with a port is still loopback", "http://127.0.0.53:9000", LaneControllerLocal},
		{"empty base url", "", LaneUnresolved},
		{"no host", "https://", LaneUnresolved},
		{"unparseable", "://not a url", LaneUnresolved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyLane(provider.ProviderInfo{Name: "p", BaseURL: tc.baseURL})
			if got != tc.want {
				t.Fatalf("ClassifyLane(%q) = %s, want %s", tc.baseURL, got, tc.want)
			}
		})
	}
}

// privacyRegistry builds a registry with one controller-local lane and one
// external lane, both healthy and capability-clean.
func privacyRegistry() *fakeRegistry {
	return &fakeRegistry{
		providers: []provider.ProviderInfo{
			{Name: "prov-local", BaseURL: "http://127.0.0.1:8080", HealthStatus: "healthy"},
			{Name: "prov-remote", BaseURL: "https://api.example.com", HealthStatus: "healthy"},
		},
		lanes: []provider.LaneInfo{
			{LaneName: "lane-local", ProviderName: "prov-local", State: "active"},
			{LaneName: "lane-remote", ProviderName: "prov-remote", State: "active"},
		},
	}
}

// externalOnlyRegistry keeps only the external lane, so a local-only
// thread has nowhere to go and the gate must refuse.
func externalOnlyRegistry() *fakeRegistry {
	reg := privacyRegistry()
	reg.providers = reg.providers[1:]
	reg.lanes = reg.lanes[1:]
	return reg
}

// TestPrivacyModeRouterFilters asserts the gate removes lanes through the
// real Select path, not only through PrivacyAllows in isolation.
func TestPrivacyModeRouterFilters(t *testing.T) {
	reg := privacyRegistry()
	// lane-remote FIRST in the spill order on purpose: with it first, a
	// filterPrivacy that removed nothing would select it, so the selected
	// lane alone proves the filtering. Ordering lane-local first would
	// make this test pass against a gate that filters nothing.
	quota := &fakeQuota{order: []LaneID{"lane-remote", "lane-local"}}
	r := NewRouter(reg, quota, nil, nil)
	ctx := ContextWithThreadPrivacy(context.Background(), ThreadPrivacy{
		ThreadID: "th-1", Mode: provider.SensitivityLocalOnly,
	})

	sel, flags, err := r.SelectExplain(ctx, chatReq())
	if err != nil {
		t.Fatalf("SelectExplain: %v", err)
	}
	if sel.LaneID != "lane-local" {
		t.Fatalf("LaneID = %q, want lane-local: the external lane survived a local-only thread", sel.LaneID)
	}
	if !hasFlagPrefix(flags, "privacy:local-only:1-filtered") {
		t.Fatalf("flags = %v, want a privacy:local-only:1-filtered entry", flags)
	}
}

// TestPrivacyModeNoThreadIsUntouched pins the other direction: a request
// with no thread in scope must leave both the candidates AND the explain
// trail exactly as they were, because S-23.T5's goldens are that trail.
func TestPrivacyModeNoThreadIsUntouched(t *testing.T) {
	reg := privacyRegistry()
	quota := &fakeQuota{order: []LaneID{"lane-local"}}
	r := NewRouter(reg, quota, nil, nil)

	_, flags, err := r.SelectExplain(context.Background(), chatReq())
	if err != nil {
		t.Fatalf("SelectExplain: %v", err)
	}
	// Asserted first: a "contains no privacy: entry" loop over an EMPTY
	// slice passes while proving nothing.
	if len(flags) == 0 {
		t.Fatal("SelectExplain returned no flags at all; the loop below would assert nothing")
	}
	for _, f := range flags {
		if strings.HasPrefix(f, "privacy:") {
			t.Fatalf("flags = %v, want no privacy: entry when no thread is in scope", flags)
		}
	}
}

func hasFlagPrefix(flags []string, prefix string) bool {
	for _, f := range flags {
		if strings.HasPrefix(f, prefix) {
			return true
		}
	}
	return false
}

// TestPrivacyModeIntegration is the contract's named integration check: a
// local-only thread routed against a registry that offers only an external
// lane must return ErrSensitivityViolation, and must not reach dispatch.
//
// fakeQuota is the stub lane recorder — FILTER 4 is the first stage that
// would consult a lane for dispatch, so callN == 0 is the assertion that
// no external lane was called.
func TestPrivacyModeIntegration(t *testing.T) {
	t.Run("local-only thread refuses an external-only registry", func(t *testing.T) {
		quota := &fakeQuota{order: []LaneID{"lane-remote"}}
		r := NewRouter(externalOnlyRegistry(), quota, nil, nil)
		ctx := ContextWithThreadPrivacy(context.Background(), ThreadPrivacy{
			ThreadID: "th-secret", Mode: provider.SensitivityLocalOnly,
		})

		_, err := r.Select(ctx, chatReq())
		assertPrivacyRefusal(t, err, "th-secret", "local-only", "lane-remote")
		if quota.callN != 0 {
			t.Fatalf("quota.NextLane called %d times, want 0: a refused thread reached dispatch", quota.callN)
		}
	})

	t.Run("an unresolved lane is refused by the restricted default", func(t *testing.T) {
		reg := externalOnlyRegistry()
		reg.providers[0].BaseURL = "" // locality cannot be computed
		quota := &fakeQuota{order: []LaneID{"lane-remote"}}
		r := NewRouter(reg, quota, nil, nil)
		// Mode left at its zero value ON PURPOSE: §5.16's unset-is-restricted
		// rule is the thing under test, not a tier the caller named.
		ctx := ContextWithThreadPrivacy(context.Background(), ThreadPrivacy{ThreadID: "th-default"})

		_, err := r.Select(ctx, chatReq())
		assertPrivacyRefusal(t, err, "th-default", "restricted", "lane-remote")
		if quota.callN != 0 {
			t.Fatalf("quota.NextLane called %d times, want 0", quota.callN)
		}
	})

	t.Run("a public thread may use the same unresolved lane", func(t *testing.T) {
		reg := externalOnlyRegistry()
		reg.providers[0].BaseURL = ""
		quota := &fakeQuota{order: []LaneID{"lane-remote"}}
		r := NewRouter(reg, quota, nil, nil)
		ctx := ContextWithThreadPrivacy(context.Background(), ThreadPrivacy{
			ThreadID: "th-open", Mode: provider.SensitivityPublic,
		})

		if _, err := r.Select(ctx, chatReq()); err != nil {
			t.Fatalf("Select for a public thread: %v; the gate refused a mode that permits everything", err)
		}
	})
}

// assertPrivacyRefusal checks the kind, the sentinel AND the three facts
// the contract requires the message to name.
//
// The message check is not belt-and-braces: (*cascade.Error).Is compares
// KIND alone, so errors.Is against ErrSensitivityViolation holds for ANY
// KindPolicyDenied error this package might return. Without the message
// assertions this test would pass against an unrelated policy refusal.
func assertPrivacyRefusal(t *testing.T, err error, threadID, mode, lane string) {
	t.Helper()
	if err == nil {
		t.Fatal("Select returned nil; a refused thread was routed")
	}
	if !errors.Is(err, ErrSensitivityViolation) {
		t.Fatalf("err = %v, want ErrSensitivityViolation", err)
	}
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("err = %v, want KindPolicyDenied", err)
	}
	for _, want := range []string{threadID, mode, lane} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q; the operator cannot act on it", err.Error(), want)
		}
	}
}

// TestPrivacyModeValueSemantics covers the two small value methods the
// router only ever reaches along one branch each: a String case nothing
// formats on the refusal path, and EffectiveMode's valid-tier leg.
func TestPrivacyModeValueSemantics(t *testing.T) {
	for _, tc := range []struct {
		lane LaneType
		want string
	}{
		{LaneControllerLocal, "controller-local"},
		{LaneExternalAPI, "external-api"},
		{LaneUnresolved, "unresolved"},
		{LaneType(99), "unresolved"}, // an unnamed type reads as the strict one
	} {
		if got := tc.lane.String(); got != tc.want {
			t.Errorf("LaneType(%d).String() = %q, want %q", tc.lane, got, tc.want)
		}
	}

	for _, tc := range []struct {
		name string
		in   provider.SensitivityTier
		want provider.SensitivityTier
	}{
		{"unset reads as restricted", provider.SensitivityTier(0), provider.SensitivityRestricted},
		{"out of range reads as restricted", provider.SensitivityTier(200), provider.SensitivityRestricted},
		{"local-only is kept", provider.SensitivityLocalOnly, provider.SensitivityLocalOnly},
		{"public is kept", provider.SensitivityPublic, provider.SensitivityPublic},
		{"internal is kept", provider.SensitivityInternal, provider.SensitivityInternal},
	} {
		if got := (ThreadPrivacy{Mode: tc.in}).EffectiveMode(); got != tc.want {
			t.Errorf("%s: EffectiveMode() = %s, want %s", tc.name, got, tc.want)
		}
	}
}

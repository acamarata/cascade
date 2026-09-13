package dispatch

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/transport"
)

// fakeTransport is a transport.Transport double: no socket, no net/http
// import. This package's own tests only need Resolve to reach r.build
// with a non-nil transport - the driver adapters' own request/response
// translation is providers/transport's own suite's job
// (FIX-transport-tier-relocation.md), not this package's.
type fakeTransport struct{}

var _ transport.Transport = fakeTransport{}

func (fakeTransport) Send(context.Context, string, string, map[string]string, []byte) (int, map[string][]string, io.ReadCloser, error) {
	return 0, nil, nil, nil
}

// fakeLookup is a RegistryLookup double that never opens a real database,
// so these tests never import "net"/"net/http" transitively through sql
// or touch a real registry file.
type fakeLookup struct {
	lanes     []registry.LaneRecord
	providers map[string]registry.ProviderRecord
	lanesErr  error
}

func (f *fakeLookup) ListLanes(context.Context) ([]registry.LaneRecord, error) {
	if f.lanesErr != nil {
		return nil, f.lanesErr
	}
	return f.lanes, nil
}

func (f *fakeLookup) GetProvider(_ context.Context, name string) (registry.ProviderRecord, error) {
	rec, ok := f.providers[name]
	if !ok {
		return registry.ProviderRecord{}, cascade.Newf(cascade.KindNotFound, "test: provider %q not found", name)
	}
	return rec, nil
}

// fakeCredentials is a CredentialSource double. It never touches a real
// vault; the zero value refuses every ref.
type fakeCredentials struct {
	values map[string]string
}

func (f *fakeCredentials) Resolve(_ context.Context, ref string) (string, error) {
	if v, ok := f.values[ref]; ok {
		return v, nil
	}
	return "", cascade.Newf(cascade.KindNotFound, "test: credential %q not found", ref)
}

func testLookup(rec registry.ProviderRecord) *fakeLookup {
	return &fakeLookup{
		lanes:     []registry.LaneRecord{{LaneName: "lane-1", ProviderName: rec.Name}},
		providers: map[string]registry.ProviderRecord{rec.Name: rec},
	}
}

func keyProvider(name string, driver registry.DriverKind) registry.ProviderRecord {
	return registry.ProviderRecord{
		Name: name, Driver: driver, BaseURL: "https://example.invalid",
		Auth: registry.AuthKey, AuthRef: registry.VaultKeyRef(name + "-key"),
		AccountKind: registry.AccountPersonal, Tier: registry.Tier("standard"), HealthStatus: registry.HealthStatus("unknown"),
	}
}

func TestNewResolver_RequiresCollaborators(t *testing.T) {
	if _, err := NewResolver(nil, nil, runtime.SystemClock{}, &fakeTransport{}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil lookup: got %v, want KindInvalidInput", err)
	}
	if _, err := NewResolver(&fakeLookup{}, nil, nil, &fakeTransport{}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil clock: got %v, want KindInvalidInput", err)
	}
	if _, err := NewResolver(&fakeLookup{}, nil, runtime.SystemClock{}, nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil http client: got %v, want KindInvalidInput", err)
	}
}

func TestResolve_LaneNotRegistered(t *testing.T) {
	res, err := NewResolver(&fakeLookup{}, nil, runtime.SystemClock{}, &fakeTransport{})
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	_, rerr := res.Resolve(context.Background(), provider.Selection{LaneID: "missing"})
	if !cascade.HasKind(rerr, cascade.KindNotFound) {
		t.Fatalf("got %v, want KindNotFound", rerr)
	}
}

func TestResolve_ListLanesErrorPropagates(t *testing.T) {
	lookup := &fakeLookup{lanesErr: cascade.New(cascade.KindUnavailable, "test: registry unreachable")}
	res, err := NewResolver(lookup, nil, runtime.SystemClock{}, &fakeTransport{})
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	_, rerr := res.Resolve(context.Background(), provider.Selection{LaneID: "lane-1"})
	if !cascade.HasKind(rerr, cascade.KindUnavailable) {
		t.Fatalf("got %v, want KindUnavailable propagated from ListLanes", rerr)
	}
}

func TestResolve_ProviderNotRegistered(t *testing.T) {
	lookup := &fakeLookup{lanes: []registry.LaneRecord{{LaneName: "lane-1", ProviderName: "ghost"}}}
	res, err := NewResolver(lookup, nil, runtime.SystemClock{}, &fakeTransport{})
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	_, rerr := res.Resolve(context.Background(), provider.Selection{LaneID: "lane-1"})
	if !cascade.HasKind(rerr, cascade.KindNotFound) {
		t.Fatalf("got %v, want KindNotFound", rerr)
	}
}

func TestResolve_OAuthProviderIsUnsupported(t *testing.T) {
	rec := keyProvider("anthropic-oauth", registry.DriverAnthropic)
	rec.Auth = registry.AuthOAuth
	res, err := NewResolver(testLookup(rec), &fakeCredentials{}, runtime.SystemClock{}, &fakeTransport{})
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	_, rerr := res.Resolve(context.Background(), provider.Selection{LaneID: "lane-1"})
	if !cascade.HasKind(rerr, cascade.KindUnsupported) {
		t.Fatalf("got %v, want KindUnsupported", rerr)
	}
}

// TestResolve_NoCredentialSourceFailsClosed is the acceptance-critical
// case: today's daemon composition root passes a nil CredentialSource
// (the owner decision on daemon credential custody is outstanding), and
// Resolve must fail closed with a typed, named error - never construct a
// driver, never panic.
func TestResolve_NoCredentialSourceFailsClosed(t *testing.T) {
	rec := keyProvider("anthropic-1", registry.DriverAnthropic)
	res, err := NewResolver(testLookup(rec), nil, runtime.SystemClock{}, &fakeTransport{})
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	got, rerr := res.Resolve(context.Background(), provider.Selection{LaneID: "lane-1"})
	if got != nil {
		t.Fatalf("got a non-nil ModelProvider with no credential source: %#v", got)
	}
	if !cascade.HasKind(rerr, cascade.KindUnavailable) {
		t.Fatalf("got %v, want KindUnavailable", rerr)
	}
	if !strings.Contains(rerr.Error(), "anthropic-1") || !strings.Contains(rerr.Error(), "anthropic-1-key") {
		t.Fatalf("error %q does not name the missing provider and credential", rerr.Error())
	}
}

// TestResolve_InvalidDriverKindFailsClosed exercises the defensive check
// against a registry read that returns a DriverKind the write-path
// Validate() should have refused. A fake lookup can return this directly,
// bypassing Validate() entirely, so the check is real and testable even
// though it should be unreachable via the real registry.
func TestResolve_InvalidDriverKindFailsClosed(t *testing.T) {
	rec := keyProvider("bad-driver", registry.DriverKind("not-a-real-driver"))
	res, err := NewResolver(testLookup(rec), &fakeCredentials{}, runtime.SystemClock{}, &fakeTransport{})
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	_, rerr := res.Resolve(context.Background(), provider.Selection{LaneID: "lane-1"})
	if !cascade.HasKind(rerr, cascade.KindInternal) {
		t.Fatalf("got %v, want KindInternal", rerr)
	}
}

// TestResolve_InvalidAuthTypeFailsClosed mirrors the DriverKind case for
// AuthType.
func TestResolve_InvalidAuthTypeFailsClosed(t *testing.T) {
	rec := keyProvider("bad-auth", registry.DriverAnthropic)
	rec.Auth = registry.AuthType("not-a-real-auth-type")
	res, err := NewResolver(testLookup(rec), &fakeCredentials{}, runtime.SystemClock{}, &fakeTransport{})
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	_, rerr := res.Resolve(context.Background(), provider.Selection{LaneID: "lane-1"})
	if !cascade.HasKind(rerr, cascade.KindInternal) {
		t.Fatalf("got %v, want KindInternal", rerr)
	}
}

func TestResolve_UnsupportedDriverKind(t *testing.T) {
	rec := keyProvider("local-1", registry.DriverLocalLLM)
	res, err := NewResolver(testLookup(rec), &fakeCredentials{values: map[string]string{"local-1-key": "v"}}, runtime.SystemClock{}, &fakeTransport{})
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	_, rerr := res.Resolve(context.Background(), provider.Selection{LaneID: "lane-1"})
	if !cascade.HasKind(rerr, cascade.KindUnsupported) {
		t.Fatalf("got %v, want KindUnsupported", rerr)
	}
}

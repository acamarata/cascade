package update

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/build"
	"github.com/acamarata/cascade/internal/hooks/egress"
)

// TestUpdateEgressListed is acceptance [9]: internal/update is a normative
// network importer mapped to release-download, and transport.go is the
// only file here that imports net/http (none imports net).
func TestUpdateEgressListed(t *testing.T) {
	listed := false
	for _, e := range build.EgressNetNormative {
		listed = listed || e.Dir == "internal/update"
	}
	if !listed || build.EgressNetPurpose["internal/update"] != string(egress.EgressClassReleaseDownload) {
		t.Fatalf("internal/update listed=%v purpose=%q, want listed and %q", listed, build.EgressNetPurpose["internal/update"], egress.EgressClassReleaseDownload)
	}
	entries, err := os.ReadDir(".")
	must(t, err)
	var importers []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), e.Name(), nil, parser.ImportsOnly)
		must(t, err)
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == "net" || p == "net/http" {
				importers = append(importers, e.Name()+":"+p)
			}
		}
	}
	if strings.Join(importers, ",") != "transport.go:net/http" {
		t.Fatalf("network importers = %v, want only transport.go:net/http", importers)
	}
}

// TestReleaseDownloadRegistered pins the init registration.
func TestReleaseDownloadRegistered(t *testing.T) {
	cfg, ok := egress.DefaultRegistry().Lookup(egress.EgressClassReleaseDownload)
	if !ok || !cfg.Enabled || cfg.AllowRestricted || cfg.AllowLocalOnly || cfg.Owner != "P1-SHIP-04" {
		t.Fatalf("release-download registration = %+v (registered %v)", cfg, ok)
	}
}

// TestProcessInterceptorFailsClosed: with no process firewall installed the
// interceptor refuses; the factory is consulted once, on first use.
func TestProcessInterceptorFailsClosed(t *testing.T) {
	ic := ProcessInterceptor()
	if _, err := ic.InterceptClass(context.Background(), egress.EgressClassReleaseDownload, egress.TierPublic, []byte("u")); err != egress.ErrNoProcessFirewall {
		t.Fatalf("no firewall = %v, want egress.ErrNoProcessFirewall", err)
	}
	sentinel := errors.New("custody failed")
	calls := 0
	egress.SetProcessEngineFactory(func(*egress.Registry) (*egress.Engine, error) { calls++; return nil, sentinel })
	t.Cleanup(func() { egress.SetProcessEngineFactory(nil) })
	lazy := ProcessInterceptor()
	if calls != 0 {
		t.Fatal("ProcessInterceptor opened the firewall before its first request")
	}
	for i := 0; i < 2; i++ {
		if _, err := lazy.InterceptClass(context.Background(), egress.EgressClassReleaseDownload, egress.TierPublic, []byte("u")); err != sentinel {
			t.Fatalf("factory error = %v, want it unchanged", err)
		}
	}
	if calls != 1 {
		t.Fatalf("factory consulted %d times, want 1", calls)
	}
}

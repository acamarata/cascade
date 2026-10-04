//go:build integration

package plugins

// Purpose: the real-socket half of ProvisionElevated's RuntimeRemote branch,
//   driven through the PRODUCTION interceptor seam and never a stand-in.
//   (1) With the process-wide default registry (plugin-remote registered
//   disabled) the flag=true path refuses before a single byte reaches the
//   loopback server. (2) An isolated registry with the class enabled builds
//   the real egress engine through newDispatchRemoteInterceptorFrom, the
//   server receives the handshake, and dispatch still ends deferred.
//   (3) A go/parser check proves no pass-through interceptor type is left
//   in this package. Integration-tagged because it imports net/http
//   (internal/build's no-network unit-lane gate).
// SPORT: internal/plugins dispatch-remote (ADD).

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/plugins/remote"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// handshakeServer is a loopback remote that records every request body.
type handshakeServer struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []string
}

func newHandshakeServer(t *testing.T) *handshakeServer {
	t.Helper()
	h := &handshakeServer{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.bodies = append(h.bodies, string(body))
		h.mu.Unlock()
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"abi_version":1}}`))
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *handshakeServer) received() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.bodies...)
}

// provisionRemote runs ProvisionElevated with the flag on against h.
func provisionRemote(t *testing.T, h *handshakeServer, intercept remote.Interceptor) (*storetest.MemStore, error) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(h.srv.URL, "http://"))
	if err != nil {
		t.Fatalf("split %q: %v", h.srv.URL, err)
	}
	store := storetest.NewMemStore()
	m := mustParseManifest(t, strings.NewReplacer("127.0.0.1", host, "\nport = 1", "\nport = "+portStr).Replace(remoteManifest))
	_, err = ProvisionElevated(context.Background(), openDispatchTestDB(t), migrate.SQLiteEmitter{}, dispatchFixedClock{}, "", "",
		store, storage.NewPluginDomainRegistry(), m, "", true, intercept)
	return store, err
}

func TestProvisionElevated_RemoteTierRealInterceptorRefusesDisabledClass_Integration(t *testing.T) {
	h := newHandshakeServer(t)
	store, err := provisionRemote(t, h, nil)
	if !errors.Is(err, egress.ErrClassDisabled) {
		t.Fatalf("ProvisionElevated(flag on, production interceptor) = %v, want ErrClassDisabled", err)
	}
	if !strings.Contains(err.Error(), `class "plugin-remote"`) || !strings.Contains(err.Error(), "no bytes may egress") {
		t.Fatalf("the refusal does not name the plugin-remote class: %q", err.Error())
	}
	if got := h.received(); len(got) != 0 {
		t.Fatalf("the loopback server was hit %d times; the disabled class must refuse before any byte", len(got))
	}
	if _, ok, _ := LoadMetadata(context.Background(), store, "demo"); ok {
		t.Fatal("a refused remote-tier add wrote metadata")
	}
}

// enabledRegistry is an isolated registry holding only plugin-remote, copied
// from the default policy with Enabled flipped. The default is untouched.
func enabledRegistry(t *testing.T) *egress.Registry {
	t.Helper()
	cfg, ok := egress.DefaultRegistry().Lookup(egress.EgressClassPluginRemote)
	if !ok || cfg.Enabled {
		t.Fatalf("default plugin-remote policy: registered=%v enabled=%v, want registered and disabled", ok, cfg.Enabled)
	}
	cfg.Enabled = true
	reg := egress.NewRegistry()
	if err := reg.Register(egress.EgressClassPluginRemote, cfg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return reg
}

func TestProvisionElevated_RemoteTierRealEngineOnEnabledRegistry_Integration(t *testing.T) {
	intercept, err := newDispatchRemoteInterceptorFrom(enabledRegistry(t))
	if err != nil {
		t.Fatalf("newDispatchRemoteInterceptorFrom(enabled): %v", err)
	}
	probe := "key=" + "AKIA" + "ABCDEFGHIJKLMNOP"
	if out, ierr := intercept.Intercept(context.Background(), []byte(probe)); ierr == nil && strings.Contains(string(out), "AKIA"+"ABCDEFGHIJKLMNOP") {
		t.Fatal("the interceptor passed a credential-shaped value through unchanged; it is not the real engine")
	}
	h := newHandshakeServer(t)
	store, err := provisionRemote(t, h, intercept)
	if want := remote.ErrRemoteRuntimeDeferred("demo"); err == nil || err.Error() != want.Error() || !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("ProvisionElevated(enabled registry) = %v, want %v", err, want)
	}
	got := h.received()
	if len(got) != 1 || !strings.Contains(got[0], `"abi_version"`) {
		t.Fatalf("the loopback server received %q, want exactly one handshake", got)
	}
	if _, ok, _ := LoadMetadata(context.Background(), store, "demo"); ok {
		t.Fatal("a deferred remote-tier handshake wrote metadata")
	}
	if _, derr := newDispatchRemoteInterceptorFrom(egress.DefaultRegistry()); !errors.Is(derr, egress.ErrClassDisabled) {
		t.Fatalf("enabling an isolated registry leaked into the default one: %v", derr)
	}
}

// TestNoPassthroughInterceptor parses every Go file in this package: the old
// stand-in type must be gone everywhere, and no production Intercept method
// may return its input unchanged.
func TestNoPassthroughInterceptor(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) < 5 {
		t.Fatalf("globbing the package: %d files, err %v", len(files), err)
	}
	sawProduction := false
	for _, path := range files {
		file, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		sawProduction = sawProduction || path == "dispatch_remote.go"
		ast.Inspect(file, func(n ast.Node) bool {
			switch d := n.(type) {
			case *ast.TypeSpec:
				if d.Name.Name == "passthroughRemoteIntercept" {
					t.Errorf("%s declares %s", path, d.Name.Name)
				}
			case *ast.FuncDecl:
				if !strings.HasSuffix(path, "_test.go") && d.Recv != nil && d.Name.Name == "Intercept" && returnsInputUnchanged(d) {
					t.Errorf("%s: an Intercept method returns its input unchanged", path)
				}
			}
			return true
		})
	}
	if !sawProduction {
		t.Fatal("dispatch_remote.go was not scanned")
	}
}

// returnsInputUnchanged reports a body of exactly `return <second param>, nil`.
func returnsInputUnchanged(d *ast.FuncDecl) bool {
	params := d.Type.Params.List
	if len(params) < 2 || len(params[1].Names) != 1 || d.Body == nil || len(d.Body.List) != 1 {
		return false
	}
	ret, ok := d.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 2 {
		return false
	}
	first, ok := ret.Results[0].(*ast.Ident)
	return ok && first.Name == params[1].Names[0].Name
}

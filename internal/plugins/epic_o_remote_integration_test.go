//go:build integration

package plugins

// Purpose: the real-socket half of epic_o_accept_test.go's remote-
// handshake acceptance criterion (P1-E15-W4-S33-T4): flag=true, a real
// loopback JSON-RPC handshake, ErrRemoteRuntimeDeferred on success.
// Moved behind the integration build tag for the same reason
// dispatch_remote_integration_test.go's header comment states in full:
// internal/build's TestNoNetworkUnitTest_RealTreeGreen gate forbids
// net/net-http/httptest in any non-integration _test.go tree-wide, and
// remote.Dispatch's public signature always uses its production
// realDoer with no injection seam this package can reach. The flag=false
// (no-network) half stays in the default lane
// (epic_o_accept_test.go's testEpicORemoteHandshake).
// SPORT: internal/plugins epic-o-acceptance (ADD) — P1-E15-W4-S33-T4.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

type epicOPassthroughIntercept struct{}

func (epicOPassthroughIntercept) Intercept(_ context.Context, content []byte) ([]byte, error) {
	return content, nil
}

func splitEpicOURL(t *testing.T, url string) (host, port string) {
	t.Helper()
	trimmed := strings.TrimPrefix(url, "http://")
	idx := strings.LastIndex(trimmed, ":")
	if idx < 0 {
		t.Fatalf("no port in %q", url)
	}
	return trimmed[:idx], trimmed[idx+1:]
}

// TestEpicORemoteHandshake_Enabled_Integration proves the flag=true
// path: a real handshake round trip over a real loopback server, ending
// in ErrRemoteRuntimeDeferred — never a fabricated success.
func TestEpicORemoteHandshake_Enabled_Integration(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"abi_version":1}}`))
	}))
	defer srv.Close()
	host, port := splitEpicOURL(t, srv.URL)

	m := epicORemoteManifest(t, host, port)
	ctx := context.Background()
	store := storetest.NewMemStore()
	db := openDispatchTestDB(t)
	domains := storage.NewPluginDomainRegistry()
	_, err := ProvisionElevated(ctx, db, migrate.SQLiteEmitter{}, dispatchFixedClock{}, "", "", store, domains, m, "",
		true, epicOPassthroughIntercept{})
	if err == nil {
		t.Fatal("ProvisionElevated(enableRemoteRuntime=true): want ErrRemoteRuntimeDeferred, got nil")
	}
	if !hit {
		t.Error("loopback handshake server was never hit")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindUnsupported, true)", kind, ok)
	}
}

//go:build integration

package plugins

// Purpose: the real-socket counterpart to dispatch_test.go's
//   TestProvisionElevated_RemoteTierRefuses: proves ProvisionElevated's
//   RuntimeRemote branch, end to end, over a REAL loopback handshake —
//   moved behind the integration build tag because
//   internal/build's TestNoNetworkUnitTest_RealTreeGreen gate (the
//   "default unit lane forbids net/net/http in any non-integration
//   _test.go", AGENT-BRIEF/LANE-RULES §6) forbids it in the default
//   lane, and remote.Dispatch's own public signature always uses its
//   production realDoer (internal/plugins/remote/doer.go) with no
//   injection seam a caller outside that package can reach — unlike
//   internal/plugins/remote's OWN unit tests, which inject a fake doer
//   directly (same-package access to the unexported dialRemote). Per
//   AGENT-BRIEF, an integration-tagged test contributes NOTHING to
//   measured coverage; the flag=false (no-network) half of this same
//   scenario is proven in the default lane by dispatch_test.go's
//   TestProvisionElevated_RemoteTierRefuses.
// SPORT: internal/plugins dispatch-remote (ADD) — P1-E15-W4-S33-T4.

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// passthroughRemoteIntercept is the minimal remote.Interceptor stub this
// file needs to prove ProvisionElevated's RuntimeRemote branch really
// invokes the handshake when enableRemoteRuntime is true and a caller
// supplies its own interceptor.
type passthroughRemoteIntercept struct{}

func (passthroughRemoteIntercept) Intercept(_ context.Context, content []byte) ([]byte, error) {
	return content, nil
}

// TestProvisionElevated_RemoteTierHandshakeDeferred_Integration proves
// the flag=true path end to end through the real composition root: a
// real loopback handshake server, a real injected Interceptor,
// ErrRemoteRuntimeDeferred on success, and — real STORE STATE — no
// metadata record written, since P1 never installs a remote-runtime
// plugin (dispatch is deferred, not merely unexecuted).
func TestProvisionElevated_RemoteTierHandshakeDeferred_Integration(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"abi_version":1}}`))
	}))
	defer srv.Close()
	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("split %q: %v", srv.URL, err)
	}

	ctx := context.Background()
	store := storetest.NewMemStore()
	db := openDispatchTestDB(t)
	domains := storage.NewPluginDomainRegistry()
	m := mustParseManifest(t, strings.NewReplacer("127.0.0.1", host, "\nport = 1", "\nport = "+portStr).Replace(remoteManifest))

	_, err = ProvisionElevated(ctx, db, migrate.SQLiteEmitter{}, dispatchFixedClock{}, "", "", store, domains, m, "", true, passthroughRemoteIntercept{})
	if err == nil {
		t.Fatal("ProvisionElevated(enableRemoteRuntime=true): want ErrRemoteRuntimeDeferred, got nil")
	}
	if !hit {
		t.Error("ProvisionElevated(enableRemoteRuntime=true): loopback handshake server was never hit")
	}
	if !strings.Contains(err.Error(), "deferred to P2") {
		t.Errorf("ProvisionElevated error = %q, want it to contain %q", err.Error(), "deferred to P2")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindUnsupported, true)", kind, ok)
	}
	if _, ok, _ := LoadMetadata(ctx, store, "demo"); ok {
		t.Error("LoadMetadata after a deferred remote-tier handshake: ok=true, want false (no install this phase)")
	}
}

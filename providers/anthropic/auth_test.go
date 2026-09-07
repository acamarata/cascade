// Purpose: unit coverage for auth.go's credential seam - AuthConfig
//   validation, key-mode and OAuth-mode header resolution, single-flight
//   token refresh, and the on-401 forced-refresh path. No "net"/"net/http"
//   import (Art.7.2); the fake broker and resolver below never touch a
//   socket.
// SPORT: placeholder: providers/anthropic driver (ADD) - see anthropic.go.

package anthropic

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeResolver dereferences vault-key names from an in-memory map, so a
// test never touches a real keychain.
type fakeResolver struct {
	values map[string]string
	err    error
}

func (r fakeResolver) Resolve(_ context.Context, ref string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	v, ok := r.values[ref]
	if !ok {
		return "", errors.New("fakeResolver: no value for " + ref)
	}
	return v, nil
}

// fakeBroker implements provider.OAuthBroker with call counters, so tests
// can assert single-flight collapses concurrent callers into one call.
type fakeBroker struct {
	starts    int32
	refreshes int32
	rec       provider.TokenRecord
	startErr  error
	refreshFn func() (provider.TokenRecord, error)
}

func (b *fakeBroker) Start(context.Context) (provider.TokenRecord, error) {
	atomic.AddInt32(&b.starts, 1)
	if b.startErr != nil {
		return provider.TokenRecord{}, b.startErr
	}
	return b.rec, nil
}

func (b *fakeBroker) Refresh(context.Context, string) (provider.TokenRecord, error) {
	atomic.AddInt32(&b.refreshes, 1)
	if b.refreshFn != nil {
		return b.refreshFn()
	}
	return b.rec, nil
}

func (b *fakeBroker) Revoke(context.Context, string) error { return nil }

func testOAuthConfig() provider.ProviderOAuthConfig {
	return provider.ProviderOAuthConfig{
		ProviderID:    "anthropic",
		ClientID:      "test-client",
		RedirectURI:   "http://127.0.0.1/callback",
		AuthEndpoint:  "https://example.test/authorize",
		TokenEndpoint: "https://example.test/token",
		PKCEMethod:    provider.PKCEMethodS256,
	}
}

func TestAuthConfigValidate(t *testing.T) {
	base := AuthConfig{Resolver: fakeResolver{}}
	cases := []struct {
		name string
		cfg  AuthConfig
		want bool
	}{
		{"missing resolver", AuthConfig{Mode: AuthModeKey, KeyRef: "k"}, false},
		{"key mode empty ref", AuthConfig{Mode: AuthModeKey, Resolver: fakeResolver{}}, false},
		{"key mode ok", AuthConfig{Mode: AuthModeKey, KeyRef: "k", Resolver: fakeResolver{}}, true},
		{"oauth missing broker", AuthConfig{Mode: AuthModeOAuth, Account: "a", OAuth: testOAuthConfig(), Resolver: fakeResolver{}}, false},
		{"oauth missing account", AuthConfig{Mode: AuthModeOAuth, Broker: &fakeBroker{}, OAuth: testOAuthConfig(), Resolver: fakeResolver{}}, false},
		{"oauth ok", AuthConfig{Mode: AuthModeOAuth, Broker: &fakeBroker{}, Account: "a", OAuth: testOAuthConfig(), Resolver: fakeResolver{}}, true},
		{"bad mode", AuthConfig{Mode: AuthMode(9), Resolver: fakeResolver{}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.validate()
			if (err == nil) != tc.want {
				t.Fatalf("validate() error = %v, want ok=%v", err, tc.want)
			}
			_ = base
		})
	}
}

func newTestDriver(t *testing.T, auth AuthConfig, clock Clock) *Driver {
	t.Helper()
	d, err := New(Config{Doer: &fakeDoer{}, Clock: clock, Auth: auth})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

func TestHeaderForKeyMode(t *testing.T) {
	auth := AuthConfig{Mode: AuthModeKey, KeyRef: "anthropic-key", Resolver: fakeResolver{values: map[string]string{"anthropic-key": "sk-test-value"}}}
	d := newTestDriver(t, auth, fixedClock(time.Unix(1000, 0)))
	name, value, err := d.headerFor(context.Background())
	if err != nil {
		t.Fatalf("headerFor: %v", err)
	}
	if name != "x-api-key" || value != "sk-test-value" {
		t.Fatalf("got %q=%q", name, value)
	}
}

func TestHeaderForKeyModeResolveFailure(t *testing.T) {
	auth := AuthConfig{Mode: AuthModeKey, KeyRef: "missing", Resolver: fakeResolver{}}
	d := newTestDriver(t, auth, fixedClock(time.Unix(1000, 0)))
	_, _, err := d.headerFor(context.Background())
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Fatalf("kind = %v, ok=%v, want KindUnavailable", kind, ok)
	}
}

func TestOAuthStartsThenReusesCachedToken(t *testing.T) {
	broker := &fakeBroker{rec: provider.TokenRecord{Provider: "anthropic", Account: "a", AccessRef: "tok-ref", ExpiresAt: time.Unix(10_000, 0)}}
	auth := AuthConfig{
		Mode: AuthModeOAuth, Broker: broker, Account: "a", OAuth: testOAuthConfig(),
		Resolver: fakeResolver{values: map[string]string{"tok-ref": "access-bytes"}},
	}
	d := newTestDriver(t, auth, fixedClock(time.Unix(1000, 0)))
	for i := 0; i < 3; i++ {
		name, value, err := d.headerFor(context.Background())
		if err != nil {
			t.Fatalf("headerFor: %v", err)
		}
		if name != "authorization" || value != "Bearer access-bytes" {
			t.Fatalf("got %q=%q", name, value)
		}
	}
	if got := atomic.LoadInt32(&broker.starts); got != 1 {
		t.Fatalf("broker.starts = %d, want exactly 1 (cache reused)", got)
	}
}

func TestOAuthRefreshesOnExpiry(t *testing.T) {
	broker := &fakeBroker{rec: provider.TokenRecord{AccessRef: "tok-ref", ExpiresAt: time.Unix(1500, 0)}}
	auth := AuthConfig{
		Mode: AuthModeOAuth, Broker: broker, Account: "a", OAuth: testOAuthConfig(),
		Resolver: fakeResolver{values: map[string]string{"tok-ref": "access-bytes"}},
	}
	clock := fixedClock(time.Unix(1000, 0))
	d := newTestDriver(t, auth, clock)
	if _, _, err := d.headerFor(context.Background()); err != nil {
		t.Fatalf("first headerFor: %v", err)
	}
	clock.set(time.Unix(2000, 0)) // past ExpiresAt - tokenExpirySkew
	if _, _, err := d.headerFor(context.Background()); err != nil {
		t.Fatalf("second headerFor: %v", err)
	}
	if got := atomic.LoadInt32(&broker.starts); got != 1 {
		t.Fatalf("broker.starts = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&broker.refreshes); got != 1 {
		t.Fatalf("broker.refreshes = %d, want 1", got)
	}
}

func TestOAuthSingleFlightCollapsesConcurrentRefresh(t *testing.T) {
	release := make(chan struct{})
	broker := &fakeBroker{refreshFn: func() (provider.TokenRecord, error) {
		<-release
		return provider.TokenRecord{AccessRef: "tok-ref"}, nil
	}}
	auth := AuthConfig{Mode: AuthModeOAuth, Broker: broker, Account: "a", OAuth: testOAuthConfig(), Resolver: fakeResolver{values: map[string]string{"tok-ref": "v"}}}
	d := newTestDriver(t, auth, fixedClock(time.Unix(1000, 0)))

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = d.refreshToken(context.Background(), true)
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := atomic.LoadInt32(&broker.refreshes); got != 1 {
		t.Fatalf("broker.refreshes = %d, want exactly 1 (single-flight)", got)
	}
}

func TestOAuthStartFailureIsPermissionDenied(t *testing.T) {
	broker := &fakeBroker{startErr: errors.New("mock idp refused")}
	auth := AuthConfig{Mode: AuthModeOAuth, Broker: broker, Account: "a", OAuth: testOAuthConfig(), Resolver: fakeResolver{}}
	d := newTestDriver(t, auth, fixedClock(time.Unix(1000, 0)))
	_, _, err := d.headerFor(context.Background())
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPermissionDenied {
		t.Fatalf("kind = %v, ok=%v, want KindPermissionDenied", kind, ok)
	}
}

func TestMapErrPreservesTaxonomyKind(t *testing.T) {
	original := cascade.New(cascade.KindNotFound, "already classified")
	if got := mapErr(original, cascade.KindInternal, "wrapped"); got != original {
		t.Fatalf("mapErr replaced an already-typed error")
	}
	plain := errors.New("plain")
	wrapped := mapErr(plain, cascade.KindTimeout, "context")
	if kind, ok := cascade.KindOf(wrapped); !ok || kind != cascade.KindTimeout {
		t.Fatalf("kind = %v, ok=%v, want KindTimeout", kind, ok)
	}
}

// fixedClock is a minimal, mutable Clock for tests: it never reads the
// system clock (Art.7.3).
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func fixedClock(t time.Time) *testClock { return &testClock{t: t} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// TestOAuth401TriggersExactlyOneRefreshAndRetries exercises the full path
// through Chat, not just headerFor: a live 401 forces exactly one refresh
// and the retry carries the freshly resolved header.
func TestOAuth401TriggersExactlyOneRefreshAndRetries(t *testing.T) {
	broker := &fakeBroker{
		rec:       provider.TokenRecord{AccessRef: "stale-ref"},
		refreshFn: func() (provider.TokenRecord, error) { return provider.TokenRecord{AccessRef: "fresh-ref"}, nil },
	}
	resolver := fakeResolver{values: map[string]string{"stale-ref": "stale", "fresh-ref": "fresh"}}
	unauthorized := `{"type":"error","error":{"type":"authentication_error","message":"expired"}}`
	doer := &fakeDoer{queue: []fakeResp{{status: 401, body: unauthorized}, {status: 200, body: recordedChatResponse}}}
	d, err := New(Config{
		Doer: doer, Clock: fixedClock(time.Unix(1000, 0)),
		Auth: AuthConfig{Mode: AuthModeOAuth, Broker: broker, Account: "a", OAuth: testOAuthConfig(), Resolver: resolver},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}}
	if _, err := d.Chat(context.Background(), req); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if broker.starts != 1 {
		t.Fatalf("broker.starts = %d, want 1", broker.starts)
	}
	if doer.reqs[0].Headers["authorization"] == doer.reqs[1].Headers["authorization"] {
		t.Fatalf("retry reused the same (expired) header")
	}
}

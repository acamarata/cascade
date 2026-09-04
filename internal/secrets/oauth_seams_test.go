// Purpose: cover the PRODUCTION transport seams in the default unit lane.
//
// Constraints: no "net"/"net/http" import here either. Every seam's
//
//	signature is expressed in this package's own types and in plain
//	strings, so the real loopback listener and the real HTTP exchanger can
//	be driven from an untagged test without naming a net type - the same
//	discipline internal/client/transport_test.go uses to exercise its
//	production dialer. Two production components talking to each other
//	over the machine's own loopback is what a callback IS; asserting it
//	here rather than only behind the integration tag is what keeps the
//	seam honest in the lane CI actually measures.
//
// SPORT: OAUTH_BROKER: ADD (transport seam tests).

package secrets

import (
	"context"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestLoopbackListenerReceivesTheRedirectAndAnswersWithoutEchoingIt(t *testing.T) {
	listener, err := listenLoopback(context.Background())
	if err != nil {
		t.Fatalf("listenLoopback: %v", err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Port()
	if port == "" || port == "0" {
		t.Fatalf("no ephemeral port was bound (got %q)", port)
	}

	got := make(chan string, 1)
	go func() {
		q, waitErr := listener.Wait(context.Background())
		if waitErr != nil {
			t.Errorf("Wait: %v", waitErr)
		}
		got <- q
	}()

	const query = "code=" + canaryCode + "&state=some-state"
	body, status, err := newHTTPExchanger().Exchange(context.Background(),
		"http://127.0.0.1:"+port+"/callback?"+query, url.Values{})
	if err != nil {
		t.Fatalf("driving the listener: %v", err)
	}
	if status != 200 {
		t.Fatalf("the listener answered HTTP %d", status)
	}
	assertNoCanary(t, "the callback page cascade serves to the browser", string(body))
	if !strings.Contains(string(body), "close this tab") {
		t.Fatalf("the callback page is not the expected prose: %q", body)
	}
	if received := <-got; received != query {
		t.Fatalf("Wait returned %q, want %q", received, query)
	}
}

func TestLoopbackListenerWaitEndsOnCloseAndOnCancellation(t *testing.T) {
	closed, err := listenLoopback(context.Background())
	if err != nil {
		t.Fatalf("listenLoopback: %v", err)
	}
	if err := closed.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := closed.Wait(context.Background()); !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("Wait after Close returned %v, want a canceled refusal", err)
	}
	if err := closed.Close(); err != nil {
		t.Fatalf("Close is not idempotent: %v", err)
	}

	live, err := listenLoopback(context.Background())
	if err != nil {
		t.Fatalf("listenLoopback: %v", err)
	}
	defer func() { _ = live.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := live.Wait(ctx); err == nil {
		t.Fatal("Wait ignored a canceled context")
	}
}

func TestHTTPExchangerClassifiesItsFailures(t *testing.T) {
	exchanger := newHTTPExchanger()
	if _, _, err := exchanger.Exchange(context.Background(), "://not-a-url", url.Values{}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("an unbuildable request was not KindInvalidInput: %v", err)
	}
	// A port nothing serves: bind one, release it, then dial it. The
	// connect(2) fails before any byte moves, so no peer is needed.
	listener, err := listenLoopback(context.Background())
	if err != nil {
		t.Fatalf("listenLoopback: %v", err)
	}
	port := listener.Port()
	if err := listener.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err = exchanger.Exchange(ctx, "http://127.0.0.1:"+port+"/token", url.Values{})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("an unreachable endpoint was not KindUnavailable: %v", err)
	}
	assertNoCanary(t, "the transport failure", err.Error())
}

func TestNewOAuthBrokerWiresTheProductionSeams(t *testing.T) {
	vault, _ := newTestBroker(t, &allowGate{})
	deps := OAuthDeps{Vault: vault, Clock: fixedClock{at: time.Unix(1, 0)}}
	broker, err := NewOAuthBroker(testOAuthConfig(), deps)
	if err != nil {
		t.Fatalf("NewOAuthBroker: %v", err)
	}
	if broker.exchange == nil || broker.listen == nil || broker.open == nil {
		t.Fatal("a production broker was built with a missing seam")
	}
	invalid := testOAuthConfig()
	invalid.RedirectURI = "https://example.com/callback"
	if _, err := NewOAuthBroker(invalid, deps); err == nil {
		t.Fatal("a non-loopback redirect target was accepted by the production constructor")
	}
}

func TestDefaultLookupEnvAndEntropyAreReal(t *testing.T) {
	t.Setenv("CASCADE_OAUTH_SEAM_PROBE", "present")
	if v, ok := defaultLookupEnv("CASCADE_OAUTH_SEAM_PROBE"); !ok || v != "present" {
		t.Fatalf("defaultLookupEnv returned %q/%v", v, ok)
	}
	if _, ok := defaultLookupEnv("CASCADE_OAUTH_SEAM_ABSENT"); ok {
		t.Fatal("defaultLookupEnv invented a value for an unset variable")
	}
	first, err := newPKCECodes(defaultEntropy())
	if err != nil {
		t.Fatalf("newPKCECodes over the production entropy source: %v", err)
	}
	second, err := newPKCECodes(defaultEntropy())
	if err != nil {
		t.Fatalf("newPKCECodes: %v", err)
	}
	if string(first.verifier.Bytes()) == string(second.verifier.Bytes()) {
		t.Fatal("the production entropy source produced the same verifier twice")
	}
}

func TestBrowserCommandNeverBuildsAShellLine(t *testing.T) {
	const hostile = "https://idp.example/authorize?x=$(rm -rf /);`id`&y=|tee"
	name, args, ok := browserCommand(hostile)
	switch runtime.GOOS {
	case "darwin", "windows", "linux", "freebsd", "openbsd", "netbsd":
		if !ok {
			t.Fatalf("no opener for GOOS %q, which is on the supported list", runtime.GOOS)
		}
	default:
		if ok {
			t.Fatalf("an opener was claimed for the unsupported GOOS %q", runtime.GOOS)
		}
		return
	}
	for _, shell := range []string{"/sh", "/bash", "/zsh", "cmd.exe", "powershell"} {
		if strings.HasSuffix(name, shell) {
			t.Fatalf("the opener runs a shell (%s); the URL would be interpreted", name)
		}
	}
	whole := false
	for _, arg := range args {
		if arg == hostile {
			whole = true
		}
	}
	if !whole {
		t.Fatalf("the URL was split or rewritten rather than passed as one argv element: %v", args)
	}
}

func TestOpenSystemBrowserRefusesOnADeadContext(t *testing.T) {
	// A canceled context makes exec refuse BEFORE spawning anything, so
	// this covers the failure branch without opening a browser on the
	// machine running the tests.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := openSystemBrowser(ctx, "https://idp.example/authorize"); err == nil {
		t.Fatal("openSystemBrowser reported success on a canceled context")
	}
}

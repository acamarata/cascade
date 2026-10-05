package egressproxy

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// failingReader is an entropy source that always fails.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// noJournal is a Journal that stores nothing.
func noJournal(context.Context, Decision) error { return nil }

func TestStartRefusesInvalidOptions(t *testing.T) {
	good := func() Options {
		return Options{DriverID: DriverCodex, JobID: "job-1", Allow: DestinationAllowlist{allowed}, Dial: ProductionDial(), Journal: noJournal}
	}
	cases := map[string]func(*Options){
		"Options.Dial is nil":    func(o *Options) { o.Dial = nil },
		"Options.Journal is nil": func(o *Options) { o.Journal = nil },
		`driver id "gemini"`:     func(o *Options) { o.DriverID = "gemini" },
		"Options.JobID is empty": func(o *Options) { o.JobID = "" },
		`"*.example.test:443"`:   func(o *Options) { o.Allow = DestinationAllowlist{allowed, "*.example.test:443"} },
	}
	for msg, mutate := range cases {
		o := good()
		mutate(&o)
		p, err := Start(context.Background(), o)
		if p != nil {
			_ = p.Close()
			t.Fatalf("%s: a proxy was started", msg)
		}
		var ce *cascade.Error
		if !errors.As(err, &ce) || ce.Kind != cascade.KindInvalidInput || !strings.Contains(err.Error(), msg) {
			t.Fatalf("%s: err = %v, want KindInvalidInput naming it", msg, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p, err := Start(ctx, good()); p != nil || !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("canceled ctx: p=%v err=%v, want KindCanceled and no proxy", p, err)
	}
	cfg := defaultSettings()
	cfg.entropy = failingReader{}
	if p, err := newProxy(good(), cfg); p != nil || !cascade.HasKind(err, cascade.KindUnavailable) ||
		!strings.Contains(err.Error(), "credential could not be generated") {
		t.Fatalf("failing entropy: p=%v err=%v", p, err)
	}
}

func TestProxyCredentialIsPerSpawn(t *testing.T) {
	a := newHarness(t, nil)
	b := newHarness(t, nil)
	if a.p.cred == b.p.cred {
		t.Fatal("two spawns share one credential")
	}
	if !strings.HasPrefix(a.p.cred, proxyUser+":") || len(a.p.cred) != len(proxyUser)+1+64 {
		t.Fatalf("credential shape %d bytes, want cascade:<64 hex>", len(a.p.cred))
	}
}

func TestProxyEnvExact(t *testing.T) {
	const u = "http://cascade:" + "not-a-secret" + "@127.0.0.1:4242" // split per C22
	p := &Proxy{url: u}
	want := []string{
		"HTTPS_PROXY=" + u, "HTTP_PROXY=" + u, "https_proxy=" + u, "http_proxy=" + u, "NO_PROXY=", "no_proxy=",
	}
	got := p.Env()
	if len(got) != len(want) {
		t.Fatalf("Env() = %q, want exactly %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Env()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if p.URL() != u {
		t.Fatalf("URL() = %q", p.URL())
	}
}

func TestCloseUnboundIsIdempotentAndEndsConnections(t *testing.T) {
	h := newHarness(t, []Destination{allowed})
	client, br, status := h.open(t, h.connect(string(allowed), h.auth()))
	if status != 200 {
		t.Fatalf("status %d", status)
	}
	for i := 0; i < 2; i++ {
		if err := h.p.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i, err)
		}
	}
	if _, err := br.ReadByte(); err != io.EOF {
		t.Fatalf("open tunnel read after Close: %v, want EOF", err)
	}
	_ = client.Close()
	h.wantRows(t, row(PhaseIntent, allowed, ReasonAllowed), row(PhaseConfirm, allowed, ReasonConnected),
		row(PhaseConfirm, allowed, ReasonClosed))
	if h.p.track(client) {
		t.Fatal("a closed proxy accepted a new connection")
	}
}

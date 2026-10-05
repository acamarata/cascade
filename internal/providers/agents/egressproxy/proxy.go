package egressproxy

// Purpose: the Proxy lifecycle: Start validates Options, binds 127.0.0.1:0,
// mints the per-spawn credential and serves; URL and Env expose it; Close
// tears it down.
//
// Constraints: one proxy per spawn, owned by it; no Options field selects
// the listen address. Close closes the listener synchronously first, then
// every open connection, then waits for every handler to return.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// proxyUser is the fixed user half of the proxy credential.
const proxyUser = "cascade"

// Options configures one spawn's proxy.
type Options struct {
	DriverID DriverID
	JobID    string
	Allow    DestinationAllowlist
	Dial     DialFunc
	Journal  Journal
}

// settings are the fixed limits plus the listen and entropy seams. Start
// always uses defaultSettings; only tests substitute them.
type settings struct {
	headerTimeout  time.Duration
	maxTunnels     int
	maxHeaderBytes int64
	listen         func(ctx context.Context) (net.Listener, error)
	entropy        io.Reader
}

// defaultSettings: 10 s to read the request head, 64 open tunnels, 32 KiB
// of request head.
func defaultSettings() settings {
	return settings{
		headerTimeout:  10 * time.Second,
		maxTunnels:     64,
		maxHeaderBytes: 32 << 10,
		listen:         listenLoopback,
		entropy:        rand.Reader,
	}
}

// listenLoopback binds an ephemeral port on the IPv4 loopback only.
func listenLoopback(ctx context.Context) (net.Listener, error) {
	return (&net.ListenConfig{}).Listen(ctx, "tcp", provider.LoopbackHost+":0")
}

// Proxy is one spawn's running egress proxy.
type Proxy struct {
	driver  DriverID
	jobID   string
	allow   allowSet
	journal Journal
	dial    func(ctx context.Context, addr string) (io.ReadWriteCloser, error)
	cfg     settings
	cred    string // "cascade:<hex>", compared in constant time
	url     string

	ctx      context.Context // canceled by Close; bounds Dial and Journal
	cancel   context.CancelFunc
	listener net.Listener
	wg       sync.WaitGroup

	mu      sync.Mutex
	closed  bool
	conns   map[io.Closer]struct{}
	tunnels int
	once    sync.Once
}

// Start validates opts and serves one proxy on 127.0.0.1:<ephemeral>. A
// nil Dial or Journal, an unknown DriverID, an empty JobID or an invalid
// Allow entry is KindInvalidInput. Cancelling ctx closes the proxy.
func Start(ctx context.Context, opts Options) (*Proxy, error) {
	return start(ctx, opts, defaultSettings())
}

// start is Start with explicit settings.
func start(ctx context.Context, opts Options, cfg settings) (*Proxy, error) {
	if err := ctx.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindCanceled, err, "egressproxy: start")
	}
	p, err := newProxy(opts, cfg)
	if err != nil {
		return nil, err
	}
	l, err := cfg.listen(ctx)
	if err != nil {
		p.cancel()
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "egressproxy: the loopback listener could not be bound")
	}
	p.listener = l
	p.url = "http://" + p.cred + "@" + l.Addr().String()
	p.wg.Add(1)
	go p.acceptLoop()
	go func() {
		select {
		case <-ctx.Done():
			_ = p.Close()
		case <-p.ctx.Done():
		}
	}()
	return p, nil
}

// newProxy validates opts and builds an unbound proxy.
func newProxy(opts Options, cfg settings) (*Proxy, error) {
	switch {
	case opts.Dial == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "egressproxy: Options.Dial is nil")
	case opts.Journal == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "egressproxy: Options.Journal is nil")
	case !opts.DriverID.known():
		return nil, cascade.Newf(cascade.KindInvalidInput, "egressproxy: driver id %q is not in the closed set", string(opts.DriverID))
	case opts.JobID == "":
		return nil, cascade.New(cascade.KindInvalidInput, "egressproxy: Options.JobID is empty")
	}
	allow, err := newAllowSet(opts.Allow)
	if err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := io.ReadFull(cfg.entropy, secret); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "egressproxy: the proxy credential could not be generated")
	}
	p := &Proxy{
		driver: opts.DriverID, jobID: opts.JobID, allow: allow, journal: opts.Journal,
		dial: adaptDial(opts.Dial), cfg: cfg, cred: proxyUser + ":" + hex.EncodeToString(secret),
		conns: make(map[io.Closer]struct{}),
	}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	return p, nil
}

// adaptDial narrows a DialFunc to the byte stream the tunnel copies. A nil
// connection with a nil error is a failed dial, never a tunnel.
func adaptDial(dial DialFunc) func(ctx context.Context, addr string) (io.ReadWriteCloser, error) {
	return func(ctx context.Context, addr string) (io.ReadWriteCloser, error) {
		c, err := dial(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		if c == nil {
			return nil, cascade.New(cascade.KindInternal, "egressproxy: Dial returned no connection")
		}
		return c, nil
	}
}

// URL is the proxy URL: scheme http, user "cascade", the per-spawn
// credential as the password, host 127.0.0.1 and the bound port.
func (p *Proxy) URL() string { return p.url }

// Env is exactly the six proxy variables a driver child receives: both
// cases of HTTPS_PROXY and HTTP_PROXY set to URL, and both cases of
// NO_PROXY set empty so an inherited bypass cannot survive.
func (p *Proxy) Env() []string {
	u := p.URL()
	return []string{
		"HTTPS_PROXY=" + u, "HTTP_PROXY=" + u,
		"https_proxy=" + u, "http_proxy=" + u,
		"NO_PROXY=", "no_proxy=",
	}
}

// Close is idempotent. The listener is closed synchronously first, then
// every open connection and tunnel, then in-flight Dial and Journal calls
// are canceled and every handler is awaited.
func (p *Proxy) Close() error {
	var err error
	p.once.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		if p.listener != nil {
			err = p.listener.Close()
		}
		p.closeAll()
		p.cancel()
		p.wg.Wait()
	})
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "egressproxy: the listener could not be closed")
	}
	return nil
}

// acceptLoop serves until the listener fails. A failure while open closes
// the whole proxy: a proxy that cannot accept refuses everything.
func (p *Proxy) acceptLoop() {
	defer p.wg.Done()
	for {
		c, err := p.listener.Accept()
		if err != nil {
			go func() { _ = p.Close() }()
			return
		}
		if !p.track(c) {
			_ = c.Close()
			continue
		}
		p.wg.Add(1)
		go p.serveConn(c)
	}
}

// track registers c so Close can end it. It refuses once closed.
func (p *Proxy) track(c io.Closer) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.conns[c] = struct{}{}
	return true
}

// untrack forgets c.
func (p *Proxy) untrack(c io.Closer) {
	p.mu.Lock()
	delete(p.conns, c)
	p.mu.Unlock()
}

// closeAll closes every tracked connection.
func (p *Proxy) closeAll() {
	p.mu.Lock()
	conns := make([]io.Closer, 0, len(p.conns))
	for c := range p.conns {
		conns = append(conns, c)
	}
	p.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

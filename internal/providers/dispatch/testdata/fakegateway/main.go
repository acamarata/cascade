// Command fakegateway is a loopback-only stand-in for an Anthropic-compatible
// gateway. The acceptance tests build it, start it on 127.0.0.1 with a
// per-test token and a control directory, and read its request journal.
// Every response shape is constructed from the public Messages API
// reference; no live capture exists (see README.md).
//
// Control files (re-read on every request, so a test can change them while
// the program runs):
//
//	<control>/faults  the /v1/messages fault script: "ok",
//	                  "<N> x <shape> then ok" or "always <shape>"; a changed
//	                  script restarts its count
//	<control>/usage   "ok" (default) or "fail": the usage source answers 503
//
// The journal is one JSON line per request: method, path, status and
// whether the request carried the expected token. It never holds headers,
// bodies or the token.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "fakegateway:", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:0", "loopback listen address")
	control := flag.String("control", "", "control directory (faults, usage)")
	journal := flag.String("journal", "", "request journal path (JSON lines)")
	tokenEnv := flag.String("token-env", "FAKEGW_TOKEN", "environment variable holding the expected token")
	flag.Parse()
	if *control == "" || *journal == "" {
		return errors.New("-control and -journal are required")
	}
	token := os.Getenv(*tokenEnv)
	if token == "" {
		return fmt.Errorf("%s is empty", *tokenEnv)
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("refusing non-loopback address %q", *addr)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	g, err := newGateway(*control, *journal, token)
	if err != nil {
		_ = ln.Close()
		return err
	}
	defer func() { _ = g.close() }()
	srv := &http.Server{Handler: g.routes(), ReadHeaderTimeout: 10 * time.Second}
	if _, err := fmt.Printf("listening http://%s\n", ln.Addr()); err != nil {
		return err
	}
	return serve(srv, ln)
}

// serve runs srv until SIGTERM or SIGINT, then shuts it down.
func serve(srv *http.Server, ln net.Listener) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

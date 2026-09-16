package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the ONE client-side dial of the daemon's /events endpoint.
//
// Inputs: a socket dialer (client.UnixDialer in production), the daemon
//
//	socket path, and the topic to filter on.
//
// Outputs: the response body as a stream, plus its release function.
// Constraints: opening a socket belongs at the composition root, which is
//
//	this package (journals/RULING-coverage-socket-code.md). Before this
//	existed there were two hand-rolled transports for one endpoint — one
//	here for `cascade fleet sessions --watch`, one in
//	internal/fleet/sessions for the harness watch — which is the very thing
//	fleet_watch.go's own comment said not to do. The packages those callers
//	live in keep the decisions: internal/fleet/sessions owns the SSE fold
//	and the topic constant, internal/plugins owns the record translation.
//
// SPORT: cmd/cascade:daemon-events-dial (ADD) — P1-E16-W4-S34-T1.

// eventsDialer opens a connection to the daemon socket.
type eventsDialer func(ctx context.Context, socketPath string) (net.Conn, error)

// dialDaemonEvents opens GET /events?topic=<topic> over the daemon socket.
//
// A nil dialer or a blank socket path is a MISWIRING and is refused rather
// than dereferenced: for a background watch a panic here means a dead
// goroutine and a daemon that silently stops observing.
func dialDaemonEvents(ctx context.Context, dial eventsDialer, socketPath, topic string) (io.ReadCloser, func(), error) {
	noop := func() {}
	if strings.TrimSpace(socketPath) == "" {
		return nil, noop, cascade.New(cascade.KindInvalidInput,
			"cascade: no daemon socket path to subscribe on")
	}
	if dial == nil {
		return nil, noop, cascade.New(cascade.KindInternal,
			"cascade: no dialer was wired for the daemon event stream")
	}

	httpClient := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dial(ctx, socketPath)
		},
	}}
	// The host is a placeholder: the transport above ignores it and dials
	// the unix socket instead. The path comes from the package that MOUNTS
	// the handler rather than being spelled again here — a second copy of
	// "/events" is a second thing to keep in step, and this dial would go
	// on succeeding against a stale route until someone moved the mount.
	// The topic is escaped because it reaches a query string.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://unix"+sessions.EventsPath+"?topic="+url.QueryEscape(topic), nil)
	if err != nil {
		return nil, noop, cascade.Wrap(cascade.KindInternal, err,
			"cascade: building the daemon /events request")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, noop, cascade.Wrap(cascade.KindUnavailable, err,
			"cascade: dialing the daemon /events stream")
	}
	return resp.Body, func() { _ = resp.Body.Close() }, nil
}

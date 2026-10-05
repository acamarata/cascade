package egressproxy

// Purpose: one client connection, decided in the fixed order (a) credential
// 407, (b) method 403, (c) authority-form target 400, (d) exact membership
// 403, (e) tunnel cap 503, (f) intent row or 403, (g) dial or 502/403,
// (h) confirm row, 200, byte copy, confirm/closed.
//
// Constraints: one request per connection; every refusal closes it. The
// request head is read under a deadline and a byte cap, the raw target is
// never journaled, and a Journal error or panic on a refusal still refuses.

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// serveConn runs one connection to completion.
func (p *Proxy) serveConn(conn io.ReadWriteCloser) {
	defer p.wg.Done()
	defer p.untrack(conn)
	defer func() { _ = conn.Close() }()
	req, pending, ok := p.readHead(conn)
	if !ok {
		p.refuse(conn, p.row(PhaseDecide, "", ReasonMalformedDestination))
		return
	}
	dest, reason := p.decide(req)
	if reason != ReasonAllowed {
		p.refuse(conn, p.row(PhaseDecide, dest, reason))
		return
	}
	if !p.reserve() {
		p.refuse(conn, p.row(PhaseDecide, dest, ReasonTunnelLimit))
		return
	}
	defer p.release()
	if err := p.record(p.ctx, p.row(PhaseIntent, dest, ReasonAllowed)); err != nil {
		p.refuse(conn, p.row(PhaseDecide, dest, ReasonJournalUnavailable))
		return
	}
	dialCtx, cancelDial := context.WithTimeout(p.ctx, dialTimeout)
	up, err := p.dial(dialCtx, string(dest))
	cancelDial()
	if err != nil {
		p.refuse(conn, p.row(PhaseConfirm, dest, dialReason(err)))
		return
	}
	p.tunnel(conn, up, pending, dest)
}

// decide applies steps (a)-(d). The returned Destination is empty until
// the target has parsed.
func (p *Proxy) decide(req *http.Request) (Destination, Reason) {
	if !p.authorized(req.Header) {
		return "", ReasonProxyAuthRequired
	}
	if req.Method != http.MethodConnect {
		return "", ReasonMethodNotConnect
	}
	dest, ok := normalizeTarget(req.RequestURI)
	if !ok {
		return "", ReasonMalformedDestination
	}
	if !p.allow.contains(dest) {
		return dest, ReasonDestinationNotAllowed
	}
	return dest, ReasonAllowed
}

// readHead reads one request head under the header deadline and byte cap.
// pending is whatever the client sent after the head (early tunnel bytes).
func (p *Proxy) readHead(conn io.ReadWriteCloser) (*http.Request, []byte, bool) {
	limited := &io.LimitedReader{R: conn, N: p.cfg.maxHeaderBytes}
	br := bufio.NewReader(limited)
	timer := time.AfterFunc(p.cfg.headerTimeout, func() { _ = conn.Close() })
	req, err := http.ReadRequest(br)
	if fired := !timer.Stop(); fired || err != nil {
		return nil, nil, false
	}
	pending, _ := br.Peek(br.Buffered())
	return req, pending, true
}

// authorized checks for exactly one Proxy-Authorization header carrying
// Basic credentials equal, in constant time, to this proxy's credential.
func (p *Proxy) authorized(h http.Header) bool {
	values := h.Values("Proxy-Authorization")
	if len(values) != 1 {
		return false
	}
	scheme, encoded, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Basic") {
		return false
	}
	got, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, []byte(p.cred)) == 1
}

// row builds a Decision for this proxy.
func (p *Proxy) row(phase Phase, dest Destination, reason Reason) Decision {
	allowed := reason == ReasonAllowed || reason == ReasonConnected || reason == ReasonClosed
	return Decision{DriverID: p.driver, JobID: p.jobID, Destination: dest, Phase: phase, Allowed: allowed, Reason: reason}
}

// refuse journals d (its error cannot turn a refusal into an admission)
// and writes the matching status. The connection is closed by the caller.
func (p *Proxy) refuse(conn io.Writer, d Decision) {
	_ = p.record(context.WithoutCancel(p.ctx), d)
	writeStatus(conn, statusFor(d.Reason))
}

// record calls the Journal. A panic in the Journal is an error, so it
// refuses rather than taking the daemon down.
func (p *Proxy) record(ctx context.Context, d Decision) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = cascade.Newf(cascade.KindInternal, "egressproxy: journal panicked: %v", r)
		}
	}()
	return p.journal(ctx, d)
}

// writeStatus writes a bodiless response that closes the connection.
func writeStatus(w io.Writer, status int) {
	extra := ""
	if status == http.StatusProxyAuthRequired {
		extra = "Proxy-Authenticate: Basic realm=\"cascade\"\r\n"
	}
	_, _ = fmt.Fprintf(w, "HTTP/1.1 %d %s\r\n%sContent-Length: 0\r\nConnection: close\r\n\r\n",
		status, http.StatusText(status), extra)
}

// Purpose: httpPoster — the one place this module touches net/http. It is
//   deliberately the thinnest thing that can be called a transport: build a
//   POST, send it, read a bounded body back.
//
// Inputs: an *http.Client (nil -> a default with a timeout above the
//   long-poll wait, so a 30s getUpdates is not cut off by its own client).
//
// Outputs: the raw response bytes, or errPostFailed.
//
// Constraints: NO ERROR FROM net/http EVER ESCAPES THIS FILE. net/http
//   returns *url.Error, whose Error() embeds the full request URL, and the
//   Bot API puts the token in the URL path — so returning or wrapping it
//   would publish the token. Every failure here becomes errPostFailed, a
//   fixed sentinel with no interpolation at all; apicall.go then reports the
//   method name and nothing more. This is why the poster seam exists rather
//   than httpDoer calling http.Client directly: the redaction rule has one
//   enforceable home, and its violation is a one-line diff in a 25-line
//   file.
//
//   plugins/* is the normative "net" importer (internal/build/
//   egress_allow.go), so this import is sanctioned; Art.7.2 bars net/http
//   from an untagged _test.go, which is why nothing tests this file directly
//   and everything it is a seam for is tested through poster fakes.
//
// SPORT: plugins/cascade-pa/telegram httpPoster/ADDED
//   (P1-E23-W5-S48-T1).

package telegram

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// httpClientTimeout must exceed DefaultPollTimeout: a long-poll that waits
// 30s server-side needs a client that waits longer, or every poll ends in a
// client-side timeout that looks like a network fault.
const httpClientTimeout = DefaultPollTimeout + 35*time.Second

// maxResponseBytes bounds what one Bot API reply may spend of this process's
// memory. Telegram's own getUpdates batches are orders of magnitude smaller;
// the cap is here because an unbounded ReadAll on a remote body is a
// remote-controlled allocation.
const maxResponseBytes = 4 << 20

// errPostFailed is the ONLY error this file returns. Fixed text, no
// interpolation, so no URL and therefore no token can reach it.
var errPostFailed = cascade.New(cascade.KindUnavailable,
	"cascade-pa/telegram: the HTTP request to the Bot API did not complete")

// httpPoster is the production poster.
type httpPoster struct {
	client *http.Client
}

// newHTTPPoster builds the production transport. A nil client gets one whose
// timeout clears the long-poll wait.
func newHTTPPoster(client *http.Client) *httpPoster {
	if client == nil {
		client = &http.Client{Timeout: httpClientTimeout}
	}
	return &httpPoster{client: client}
}

// Post sends body to url and returns the response bytes.
func (p *httpPoster) Post(ctx context.Context, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, errPostFailed
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, errPostFailed
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, errPostFailed
	}
	return raw, nil
}

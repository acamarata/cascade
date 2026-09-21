package telegram

// Purpose (this file): the transport-adjacent properties the mutation review
//   found asserted by NOTHING — the pinned host, the method allowlist (which
//   is what makes "no getFile call exists" able to fail), and the rule that a
//   bot token never appears in an error.
//
// Constraints: driven through the poster seam, so this file imports no
//   net/http and the untagged unit lane stays legal (Art.7.2).
//
// SPORT: plugins/cascade-pa/telegram apicall-tests/TEST (P1-E23-W5-S48-T1).

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// recordingPoster captures the URL and body it was asked to post and answers
// with a scripted response or error.
type recordingPoster struct {
	urls  []string
	reply []byte
	err   error
}

func (p *recordingPoster) Post(_ context.Context, url string, _ []byte) ([]byte, error) {
	p.urls = append(p.urls, url)
	if p.err != nil {
		return nil, p.err
	}
	return p.reply, nil
}

// TestHTTPDoer_DialsTheTelegramAPIHostOnly is the host pin. The review's
// mutation repointed telegramAPIBase at a plaintext local address and the
// whole suite stayed green; this test is what turns that mutation red.
func TestHTTPDoer_DialsTheTelegramAPIHostOnly(t *testing.T) {
	post := &recordingPoster{reply: []byte(`{"ok":true,"result":{"message_id":1}}`)}
	d := newAPIDoer(syntheticToken, post)
	if err := d.Do(context.Background(), MethodSendMessage, sendMessageParams{ChatID: 1, Text: "x"}, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(post.urls) != 1 {
		t.Fatalf("poster saw %d calls, want 1", len(post.urls))
	}
	const wantPrefix = "https://api.telegram.org/bot"
	if !strings.HasPrefix(post.urls[0], wantPrefix) {
		t.Fatalf("dialled %q, want a URL beginning %q", post.urls[0], wantPrefix)
	}
	if !strings.HasSuffix(post.urls[0], "/"+MethodSendMessage) {
		t.Fatalf("dialled %q, want it to end in /%s", post.urls[0], MethodSendMessage)
	}
	// The literal is spelled out here rather than read from the constant, so
	// editing the constant cannot edit the expectation with it.
	if telegramAPIBase != "https://api.telegram.org" {
		t.Fatalf("telegramAPIBase = %q; the bridge egress class is registered for api.telegram.org only",
			telegramAPIBase)
	}
	if strings.HasPrefix(post.urls[0], "http://") {
		t.Fatal("the Bot API was dialled over plaintext http")
	}
}

// TestHTTPDoer_RefusesEveryMethodOutsideTheAllowlist is what gives the
// "no getFile call exists" acceptance criterion a way to fail: the refusal is
// enforced at the transport, not merely unexercised.
func TestHTTPDoer_RefusesEveryMethodOutsideTheAllowlist(t *testing.T) {
	post := &recordingPoster{reply: []byte(`{"ok":true}`)}
	d := newAPIDoer(syntheticToken, post)
	for _, method := range []string{"getFile", "downloadFile", "sendPhoto", "getMe", ""} {
		err := d.Do(context.Background(), method, struct{}{}, nil)
		if err == nil {
			t.Fatalf("method %q was allowed", method)
		}
		if !strings.Contains(err.Error(), "not one of the three Bot API methods") {
			t.Fatalf("method %q refused with the wrong error: %v", method, err)
		}
	}
	if len(post.urls) != 0 {
		t.Fatalf("a disallowed method reached the transport: %v", post.urls)
	}
	for _, method := range []string{MethodGetUpdates, MethodSendMessage, MethodAnswerCallbackQuery} {
		if !methodAllowed(method) {
			t.Fatalf("method %q is not allowed but must be", method)
		}
	}
}

// TestHTTPDoer_TransportErrorNeverCarriesTheToken is the token-leak proof. The
// poster returns exactly what net/http returns — a *url.Error-shaped string
// carrying the whole request URL, token included — and nothing of it may reach
// the error the caller sees.
func TestHTTPDoer_TransportErrorNeverCarriesTheToken(t *testing.T) {
	urlErrText := `Post "https://api.telegram.org/bot` + syntheticToken +
		`/getUpdates": dial tcp 149.154.167.220:443: connect: connection refused`
	post := &recordingPoster{err: errors.New(urlErrText)}
	d := newAPIDoer(syntheticToken, post)
	var out []Update
	err := d.Do(context.Background(), MethodGetUpdates, getUpdatesParams{}, &out)
	if err == nil {
		t.Fatal("Do succeeded against a failing transport")
	}
	if !tokenAbsent(err.Error(), syntheticToken) {
		t.Fatalf("the bot token reached the error string: %q", err.Error())
	}
	if strings.Contains(err.Error(), "dial tcp") || strings.Contains(err.Error(), "api.telegram.org") {
		t.Fatalf("the transport error text was wrapped through: %q", err.Error())
	}
	if !strings.Contains(err.Error(), MethodGetUpdates) {
		t.Fatalf("the error names no method, so an operator cannot act on it: %q", err.Error())
	}
}

// TestHTTPDoer_NoTransportRefuses: a doer with no poster must refuse, not
// panic on a nil interface.
func TestHTTPDoer_NoTransportRefuses(t *testing.T) {
	d := newAPIDoer(syntheticToken, nil)
	err := d.Do(context.Background(), MethodSendMessage, sendMessageParams{}, nil)
	if err == nil || !strings.Contains(err.Error(), "no HTTP transport is configured") {
		t.Fatalf("got %v, want the no-transport refusal", err)
	}
}

// TestHTTPDoer_UnmarshalableParamsNeverDial keeps an encoding failure from
// reaching the network with a half-built body.
func TestHTTPDoer_UnmarshalableParamsNeverDial(t *testing.T) {
	post := &recordingPoster{reply: []byte(`{"ok":true}`)}
	d := newAPIDoer(syntheticToken, post)
	err := d.Do(context.Background(), MethodSendMessage, func() {}, nil)
	if err == nil {
		t.Fatal("an unencodable request body was accepted")
	}
	if len(post.urls) != 0 {
		t.Fatalf("the transport was reached with an unencodable body: %v", post.urls)
	}
}

// TestHTTPDoer_DecodesTheEnvelope closes the happy path: a successful reply
// reaches the caller's out parameter.
func TestHTTPDoer_DecodesTheEnvelope(t *testing.T) {
	post := &recordingPoster{reply: mustReadTestdata(t, "getupdates_text.json")}
	d := newAPIDoer(syntheticToken, post)
	var out []Update
	if err := d.Do(context.Background(), MethodGetUpdates, getUpdatesParams{}, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(out) != 1 || out[0].UpdateID != 900000001 {
		t.Fatalf("decoded %+v, want the fixture's single update", out)
	}
}

// TestSubjectFromToken_IsADigestNotTheToken proves the identity a subject
// carries discloses nothing: the "paired: <subject>" confirmation and the
// durable row key are both derived from this.
func TestSubjectFromToken_IsADigestNotTheToken(t *testing.T) {
	subject := SubjectFromToken(syntheticToken)
	if !tokenAbsent(subject, syntheticToken) {
		t.Fatalf("subject %q contains the token", subject)
	}
	if !strings.HasPrefix(subject, "tg-") {
		t.Fatalf("subject %q does not name its transport", subject)
	}
	if subject != SubjectFromToken(syntheticToken) {
		t.Fatal("SubjectFromToken is not stable for one token")
	}
	if subject == SubjectFromToken(syntheticToken+"x") {
		t.Fatal("two different tokens produced the same subject")
	}
}

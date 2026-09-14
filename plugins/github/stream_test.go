package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/plugins/github/tools"
)

// Purpose (this file): the failure paths of the stdio loop — a host that
//   sends malformed params, and a host that goes away mid-conversation.
//   These are the conditions a plugin meets in production and never in a
//   happy-path test.
// SPORT: plugins/github tests (ADD) — P1-E25-W5-S51-T1.

// brokenWriter fails every write, standing in for a host that closed the
// pipe while this plugin was still answering.
type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("pipe closed") }

// TestRunStopsWhenTheHostGoesAway proves a failed reply write ends the loop
// with an error instead of spinning through the rest of the stream writing
// into a pipe nobody is reading.
func TestRunStopsWhenTheHostGoesAway(t *testing.T) {
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"cascade.hello"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"cascade.hello"}` + "\n")
	var stderr strings.Builder

	p, _ := newTestPlugin(&fakeDoer{}, nil, nil)
	err := p.run(in, brokenWriter{}, &stderr)
	if err == nil {
		t.Fatal("run returned nil after every reply write failed")
	}
	if !strings.Contains(err.Error(), "pipe closed") {
		t.Errorf("error = %v, want it to carry the write failure", err)
	}
}

// TestRunReportsAnOversizeFrame proves a line past the scanner's buffer
// limit ends the loop with an error rather than being silently truncated
// into a different, still-parseable frame.
func TestRunReportsAnOversizeFrame(t *testing.T) {
	huge := `{"jsonrpc":"2.0","id":1,"method":"cascade.hello","params":{"pad":"` +
		strings.Repeat("x", 9*1024*1024) + `"}}` + "\n"
	var stdout, stderr strings.Builder

	p, _ := newTestPlugin(&fakeDoer{}, nil, nil)
	err := p.run(strings.NewReader(huge), &stdout, &stderr)
	if err == nil {
		t.Fatal("run accepted a frame larger than its buffer limit without reporting it")
	}
	if stdout.Len() != 0 {
		t.Errorf("a reply was written for an unreadable frame: %q", stdout.String())
	}
}

// TestMalformedParamsBecomeAnErrorReply covers both decode sites. A host
// that sends params of the wrong shape must get a refusal naming the
// problem — the alternative is a call made with zero-valued arguments,
// which for an issue or a pull request means acting on the wrong thing.
func TestMalformedParamsBecomeAnErrorReply(t *testing.T) {
	p, _ := newTestPlugin(&fakeDoer{}, &fakeWaiter{}, nil)
	for _, method := range []string{"cascade.auth.begin", PluginName + ".repos.get"} {
		id := uint64(9)
		out, reply := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: method,
			Params: json.RawMessage(`{"owner":[1,2,3]}`)})
		if !reply {
			t.Fatalf("%s produced no reply", method)
		}
		if out.Error == nil {
			t.Errorf("%s accepted params of the wrong shape: %+v", method, out.Result)
		}
	}
}

// TestToolCallRefusesAnUnknownVerb proves a namespaced method that is not a
// declared tool is refused by the builder rather than routed somewhere
// approximate.
func TestToolCallRefusesAnUnknownVerb(t *testing.T) {
	p, _ := newTestPlugin(&fakeDoer{}, nil, nil)
	id := uint64(11)
	out, reply := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: PluginName + ".repos.delete",
		Params: json.RawMessage(`{"owner":"a","repo":"b"}`)})
	if !reply || out.Error == nil {
		t.Fatalf("an undeclared tool was accepted: %+v", out)
	}
}

// TestToolCallReachesTheAPIAndDecodesTheAnswer pins the whole path: the
// request that goes out, and the decoded value that comes back.
//
// The URL assertion is the security-relevant half — every call this plugin
// makes must be rooted at api.github.com, the single host its manifest
// declares.
func TestToolCallReachesTheAPIAndDecodesTheAnswer(t *testing.T) {
	doer := &fakeDoer{body: []byte(`{"number":7,"title":"t","state":"open","html_url":"https://github.com/a/b/issues/7"}`)}
	p, _ := newTestPlugin(doer, nil, nil)

	id := uint64(12)
	out, reply := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: PluginName + ".issues.create",
		Params: json.RawMessage(`{"owner":"acamarata","repo":"cascade","title":"t","body":"b"}`)})
	if !reply || out.Error != nil {
		t.Fatalf("issues.create returned %+v", out)
	}

	sent := doer.last(t)
	if sent.Method != "POST" {
		t.Errorf("method = %q, want POST", sent.Method)
	}
	if !strings.HasPrefix(sent.URL(), "https://api.github.com/") {
		t.Errorf("url = %q, want it rooted at the single declared host", sent.URL())
	}
	if !strings.Contains(sent.URL(), "/repos/acamarata/cascade/issues") {
		t.Errorf("url = %q, want the issues path", sent.URL())
	}
	if !strings.Contains(string(sent.Body), `"title":"t"`) {
		t.Errorf("body = %q, want the issue title", sent.Body)
	}

	issue, ok := out.Result.(tools.Issue)
	if !ok {
		t.Fatalf("result = %T, want a decoded tools.Issue", out.Result)
	}
	if issue.Number != 7 {
		t.Errorf("decoded issue number = %d, want 7", issue.Number)
	}
}

// TestAnErrorStatusBecomesATypedRefusal proves a GitHub error status is
// mapped onto the error taxonomy rather than decoded as if it were data.
func TestAnErrorStatusBecomesATypedRefusal(t *testing.T) {
	doer := &fakeDoer{status: 404, body: []byte(`{"message":"Not Found"}`)}
	p, _ := newTestPlugin(doer, nil, nil)

	id := uint64(13)
	out, _ := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: PluginName + ".repos.get",
		Params: json.RawMessage(`{"owner":"acamarata","repo":"nope"}`)})
	if out.Error == nil {
		t.Fatal("a 404 was reported as success")
	}
	if !strings.Contains(out.Error.Message, "not found") {
		t.Errorf("error = %q, want GitHub's reason", out.Error.Message)
	}
}

// TestATransportFailureIsReportedAsUnavailable separates "the call failed"
// from "GitHub said no" — the first is worth retrying, the second is not.
func TestATransportFailureIsReportedAsUnavailable(t *testing.T) {
	p, _ := newTestPlugin(&fakeDoer{err: errTransport}, nil, nil)

	id := uint64(14)
	out, _ := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: PluginName + ".repos.list",
		Params: json.RawMessage(`{"owner":"acamarata"}`)})
	if out.Error == nil {
		t.Fatal("a transport failure was reported as success")
	}
	if !strings.Contains(out.Error.Message, "dial refused") {
		t.Errorf("error = %q, want it to carry the transport failure", out.Error.Message)
	}
}

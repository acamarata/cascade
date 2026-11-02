package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
)

// Purpose (this file): the plugin's own contract — its manifest loads under
//   the real v2 loader, it registers no builtin, its stdio dispatch answers
//   the handshake, routes every advertised tool and the generic-verb envelope.
// SPORT: plugins/github tests (ADD) — P1-E25-W5-S51-T1.

// loadManifest parses manifest.toml with the REAL loader, which runs with
// DisallowUnknownFields — so a key the schema does not define fails here
// rather than at install time on someone's machine.
func loadManifest(t *testing.T) plugin.Manifest {
	t.Helper()
	f, err := os.Open("manifest.toml")
	if err != nil {
		t.Fatalf("open manifest.toml: %v", err)
	}
	defer func() { _ = f.Close() }()
	m, err := plugin.ParseManifest(f)
	if err != nil {
		t.Fatalf("manifest.toml does not parse under the real v2 loader: %v", err)
	}
	return m
}

// TestManifestParsesAndValidates is the check that makes the manifest real
// rather than decorative: the host will parse and validate this exact file,
// and a manifest that fails either is a plugin that silently cannot install.
func TestManifestParsesAndValidates(t *testing.T) {
	m := loadManifest(t)
	if errs := plugin.Validate(m); len(errs) > 0 {
		t.Fatalf("manifest rejected by the host validator: %v", errs)
	}
	if m.ID != PluginName {
		t.Errorf("id = %q, want %q", m.ID, PluginName)
	}
	if m.Runtime != plugin.RuntimeProcess {
		t.Fatalf("runtime = %q, want %q: this plugin is process tier", m.Runtime, plugin.RuntimeProcess)
	}
}

// TestThisPackageRegistersNoBuiltin is this package's half of the
// contract's "never in the builtin registry" assertion.
//
// A builtin registers itself from an init() in its own package, so the way
// cascade-github could WRONGLY end up in the registry is a
// plugin.RegisterBuiltin call added here. Linking this package must
// therefore register nothing at all, and that is what this asserts — the
// registry read here reflects exactly the inits this test binary links.
//
// This is deliberately NOT written as "the registry contains no
// cascade-github entry": this binary links no builtin plugin packages, so
// such a check would pass with an empty registry no matter what. The
// assertion against a POPULATED registry lives in
// internal/plugins/builtin_tier_only_test.go, where every first-party
// builtin is linked.
func TestThisPackageRegistersNoBuiltin(t *testing.T) {
	if got := plugin.Builtins(); len(got) != 0 {
		t.Fatalf("linking plugins/github registered %d builtin(s) (%v); it is process tier "+
			"and must register none", len(got), got)
	}
}

// TestManifestDeclaresTheNetworkAndOAuthScope pins what an operator
// consents to. The v2 schema has no dedicated net-scope or OAuth-scope key,
// so both are declared through `requires` and repeated in the permission
// text; this test is what keeps those two places from drifting apart.
//
// allowedNetScopes is the closed set: api.github.com (this plugin's REST
// API) and github.com (P1-E25-W5-S51-T6's wiki git clone/push endpoint,
// distinct from the API host). Anything else is an extra host nobody
// reviewed.
func TestManifestDeclaresTheNetworkAndOAuthScope(t *testing.T) {
	m := loadManifest(t)
	allowedNetScopes := map[string]bool{
		"net.http:api.github.com": true,
		"net.http:github.com":     true,
	}

	required := strings.Join(m.Requires, " ")
	for _, want := range []string{"api.github.com", "github.com", "repo"} {
		if !strings.Contains(required, want) {
			t.Errorf("requires = %v, want it to declare %q", m.Requires, want)
		}
	}
	gotNetScopes := map[string]bool{}
	for _, r := range m.Requires {
		if !strings.HasPrefix(r, "net.http:") {
			continue
		}
		gotNetScopes[r] = true
		if !allowedNetScopes[r] {
			t.Errorf("requires declares the extra net scope %q; only api.github.com and github.com are reviewed hosts", r)
		}
	}
	for want := range allowedNetScopes {
		if !gotNetScopes[want] {
			t.Errorf("requires = %v, missing the exact scope %q (a substring match on the joined list is not enough)", m.Requires, want)
		}
	}

	consent := ""
	for _, p := range m.Permissions {
		consent += p.Description + " "
	}
	if !strings.Contains(consent, "api.github.com") {
		t.Error("no permission text names api.github.com; an operator consents to what they can read")
	}
	if !strings.Contains(consent, ".wiki.git") {
		t.Error("no permission text names the wiki git endpoint; an operator consents to what they can read")
	}
	if !strings.Contains(consent, "never written") {
		t.Error("no permission text states that the token is not written to config")
	}
}

// TestManifestToolsMatchTheDispatchTable proves every advertised tool
// actually routes, all the way through the transport and its decoder. A
// manifest naming a tool the binary cannot serve is a tool that fails only
// when someone calls it.
func TestManifestToolsMatchTheDispatchTable(t *testing.T) {
	m := loadManifest(t)
	if len(m.Provides.Tools) == 0 {
		t.Fatal("the manifest advertises no tools")
	}
	const args = `{"owner":"acamarata","repo":"cascade","number":1,"title":"t","head":"f",` +
		`"base":"main","body":"b","merge_method":"squash","reviewers":["someone"]}`

	for _, tool := range m.Provides.Tools {
		if !strings.HasPrefix(tool.Name, PluginName+".") {
			t.Errorf("tool %q is not in this plugin's namespace", tool.Name)
			continue
		}
		verb := strings.TrimPrefix(tool.Name, PluginName+".")
		doer := &fakeDoer{body: cannedBody(verb)}
		p, _ := newTestPlugin(doer, nil, nil)

		id := uint64(1)
		out, reply := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: tool.Name,
			Params: json.RawMessage(args)})
		if !reply {
			t.Errorf("tool %q produced no reply", tool.Name)
			continue
		}
		if out.Error != nil {
			t.Errorf("tool %q is advertised but does not route: %s", tool.Name, out.Error.Message)
			continue
		}
		if len(doer.calls) != 1 {
			t.Errorf("tool %q made %d API calls, want exactly 1", tool.Name, len(doer.calls))
		}
	}
}

// cannedBody returns a minimal valid response body for verb.
func cannedBody(verb string) []byte {
	switch {
	case verb == "repos.clone_url" || verb == "repos.get":
		return []byte(`{"name":"cascade","clone_url":"https://github.com/acamarata/cascade.git"}`)
	case strings.HasSuffix(verb, ".list"):
		return []byte(`[]`)
	default:
		return []byte(`{}`)
	}
}

// TestHandshakeAnswersHello pins the one frame the host requires before it
// will consider the plugin launched.
func TestHandshakeAnswersHello(t *testing.T) {
	p, _ := newTestPlugin(&fakeDoer{}, nil, nil)
	id := uint64(7)
	out, reply := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: "cascade.hello",
		Params: json.RawMessage(`{"min_protocol_version":"1.0.0"}`)})
	if !reply {
		t.Fatal("cascade.hello produced no reply")
	}
	if out.ID == nil || *out.ID != id {
		t.Fatalf("reply id = %v, want the request's id %d", out.ID, id)
	}
	ack, ok := out.Result.(helloAck)
	if !ok {
		t.Fatalf("result = %T, want a helloAck", out.Result)
	}
	if ack.ProtocolVersion != ProtocolVersion {
		t.Fatalf("protocol_version = %q, want %q", ack.ProtocolVersion, ProtocolVersion)
	}
}

// TestNotificationsGetNoReply proves the plugin does not answer a frame
// with no id. The host correlates replies by id, so an unsolicited frame
// would sit on the wire matching nothing.
func TestNotificationsGetNoReply(t *testing.T) {
	p, _ := newTestPlugin(&fakeDoer{}, nil, nil)
	if _, reply := p.dispatch(frame{JSONRPC: "2.0", Method: "cascade.hello"}); reply {
		t.Fatal("a notification was answered")
	}
}

// TestUnknownMethodIsARealRefusal proves an unrecognized method returns a
// JSON-RPC error naming it, rather than silence the host would wait out.
func TestUnknownMethodIsARealRefusal(t *testing.T) {
	p, _ := newTestPlugin(&fakeDoer{}, nil, nil)
	id := uint64(1)
	out, reply := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: "cascade.nope"})
	if !reply || out.Error == nil {
		t.Fatal("an unknown method did not produce an error reply")
	}
	if !strings.Contains(out.Error.Message, "cascade.nope") {
		t.Errorf("error = %q, want it to name the method", out.Error.Message)
	}
}

// TestRunAnswersAStreamAndSurvivesABadFrame drives the real loop. The
// undecodable line in the middle is the point: the host owns the stream's
// framing, and one unreadable line must not take down a plugin that can
// still serve the next call.
func TestRunAnswersAStreamAndSurvivesABadFrame(t *testing.T) {
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"cascade.hello","params":{"min_protocol_version":"1.0.0"}}` + "\n" +
			`{not json` + "\n" +
			`{"jsonrpc":"2.0","method":"some.notification"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"cascade.hello","params":{}}` + "\n")
	var stdout, stderr bytes.Buffer

	p, _ := newTestPlugin(&fakeDoer{}, nil, nil)
	if err := p.run(in, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("wrote %d replies, want 2 (one per request, none for the notification or the bad line):\n%s",
			len(lines), stdout.String())
	}
	if !strings.Contains(stderr.String(), "undecodable") {
		t.Errorf("stderr = %q, want the skipped frame reported", stderr.String())
	}
}

// TestGitHubPluginHandlesCmdEnvelope maps the {args, flags} envelope onto the
// repos.list tool and returns the recorded fixture's result.
func TestGitHubPluginHandlesCmdEnvelope(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "fixtures", "repos.list.json"))
	if err != nil {
		t.Fatal(err)
	}
	doer := &fakeDoer{body: fixture}
	p, _ := newTestPlugin(doer, nil, nil)
	id := uint64(1)
	viaCmd, ok := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: PluginName + ".cmd.repos",
		Params: json.RawMessage(`{"args":["list","acamarata"],"flags":{"per-page":"2"}}`)})
	if got := doer.last(t).Path; !ok || viaCmd.Error != nil || got != "/users/acamarata/repos?per_page=2" {
		t.Fatalf("cmd.repos = %+v, request path %q; the flag or owner did not survive the envelope", viaCmd, got)
	}
	viaTool, _ := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: PluginName + ".repos.list",
		Params: json.RawMessage(`{"owner":"acamarata","per_page":2}`)})
	a, _ := json.Marshal(viaCmd.Result)
	b, _ := json.Marshal(viaTool.Result)
	if string(a) != string(b) || !strings.Contains(string(a), "acamarata/qibla") {
		t.Fatalf("cmd result %.120s differs from the tool result %.120s", a, b)
	}
}

// TestGitHubPluginRefusesBadCmdEnvelopes: typed refusals, no egress.
func TestGitHubPluginRefusesBadCmdEnvelopes(t *testing.T) {
	for name, c := range map[string]struct{ method, params, want string }{
		"unknown noun":     {".cmd.nope", `{"args":["list"]}`, "no generic verb"},
		"no sub-verb":      {".cmd.repos", `{"args":[]}`, "sub-verb is required"},
		"unknown sub-verb": {".cmd.issues", `{"args":["frobnicate"]}`, "unknown sub-verb"},
		"no target":        {".cmd.issues", `{"args":["get"]}`, "owner argument"},
		"no slash":         {".cmd.issues", `{"args":["get","acamarata"]}`, "<owner>/<repo>"},
		"no number":        {".cmd.issues", `{"args":["get","a/b"]}`, "needs a number"},
		"bad number":       {".cmd.prs", `{"args":["merge","a/b","x"]}`, "not a number"},
		"unknown flag":     {".cmd.prs", `{"args":["list","a/b"],"flags":{"nope":"1"}}`, "unknown flag"},
		"bad boolean":      {".cmd.prs", `{"args":["create","a/b"],"flags":{"draft":"maybe"}}`, "not a boolean"},
		"bad envelope":     {".cmd.prs", `{"args":"list"}`, "verb envelope"},
	} {
		doer, id := &fakeDoer{}, uint64(1)
		p, _ := newTestPlugin(doer, nil, nil)
		out, _ := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: PluginName + c.method, Params: json.RawMessage(c.params)})
		if out.Error == nil || !strings.Contains(out.Error.Message, c.want) {
			t.Errorf("%s: reply = %+v, want an error containing %q", name, out, c.want)
		}
		if len(doer.calls) != 0 {
			t.Errorf("%s: a refused envelope reached the transport", name)
		}
	}
}

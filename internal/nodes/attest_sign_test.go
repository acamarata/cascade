package nodes

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

type attestClock struct{ t time.Time }

func (c attestClock) Now() time.Time { return c.t }

type memBinding struct {
	b  ControllerBinding
	ok bool
}

func (m memBinding) Load() (ControllerBinding, bool, error) { return m.b, m.ok, nil }

type attestRig struct {
	deps    attestSignDeps
	ctrl    ed25519.PrivateKey
	calls   *int
	now     time.Time
	request AttestSignRequest
}

func newAttestRig(t *testing.T) attestRig {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	calls := 0
	signer := func(context.Context, AttestSignRequest) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"signed":true}`), nil
	}
	binding := ControllerBinding{Endpoint: "http://c.invalid", EnrollmentID: "e1", ControllerPubKeyB64: base64.StdEncoding.EncodeToString(pub)}
	r := attestRig{deps: attestSignDeps{binding: memBinding{b: binding, ok: true}, clock: attestClock{now}, nodeID: "node-1", signer: signer},
		ctrl: priv, calls: &calls, now: now,
		request: AttestSignRequest{ExecutionID: "exec-1", Nonce: "n1", ExpiresAt: now.Add(time.Hour), CheckpointID: "cp",
			RepoID: "repo", JobSpecHash: "spec", PolicyVersion: "pv"}}
	return r
}

// signed returns req signed for node-1 by key.
func (r attestRig) signed(t *testing.T, req AttestSignRequest, key ed25519.PrivateKey) json.RawMessage {
	t.Helper()
	payload, err := AttestSignPayload("node-1", req)
	if err != nil {
		t.Fatal(err)
	}
	req.ControllerSig = base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload))
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestNodeSignRefusesUnenrolledCaller(t *testing.T) {
	r := newAttestRig(t)
	if _, err := serveAttestSign(context.Background(), r.deps, r.signed(t, r.request, r.ctrl)); err != nil || *r.calls != 1 {
		t.Fatalf("pinned controller refused: %v (calls %d)", err, *r.calls)
	}
	_, stranger, _ := ed25519.GenerateKey(rand.Reader)
	cases := map[string]func(attestRig) (attestSignDeps, json.RawMessage){
		"no binding": func(r attestRig) (attestSignDeps, json.RawMessage) {
			d := r.deps
			d.binding = memBinding{}
			return d, r.signed(t, r.request, r.ctrl)
		},
		"nil binding store": func(r attestRig) (attestSignDeps, json.RawMessage) {
			d := r.deps
			d.binding = nil
			return d, r.signed(t, r.request, r.ctrl)
		},
		"binding without a key": func(r attestRig) (attestSignDeps, json.RawMessage) {
			d := r.deps
			d.binding = memBinding{b: ControllerBinding{Endpoint: "x", EnrollmentID: "e"}, ok: true}
			return d, r.signed(t, r.request, r.ctrl)
		},
		"wrong key": func(r attestRig) (attestSignDeps, json.RawMessage) {
			return r.deps, r.signed(t, r.request, stranger)
		},
		"bad signature": func(r attestRig) (attestSignDeps, json.RawMessage) {
			req := r.request
			req.ControllerSig = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
			raw, _ := json.Marshal(req)
			return r.deps, raw
		},
		"signed for another node": func(r attestRig) (attestSignDeps, json.RawMessage) {
			d := r.deps
			d.nodeID = "node-2"
			return d, r.signed(t, r.request, r.ctrl)
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			r := newAttestRig(t)
			d, raw := build(r)
			_, err := serveAttestSign(context.Background(), d, raw)
			wantKind(t, err, cascade.KindPermissionDenied)
			if *r.calls != 0 {
				t.Fatal("the signer ran for an unverified caller")
			}
		})
	}
}

func TestNodeSignIgnoresCallerSuppliedResults(t *testing.T) {
	for _, extra := range []string{`"exit_status":0`, `"tree_hash":"t"`, `"result_hashes":["unit:x"]`, `"commit_sha":"c"`, `"binding":{}`} {
		r := newAttestRig(t)
		raw := r.signed(t, r.request, r.ctrl)
		withExtra := append(append(bytes.TrimSuffix(raw, []byte("}")), []byte(","+extra)...), '}')
		_, err := serveAttestSign(context.Background(), r.deps, withExtra)
		wantKind(t, err, cascade.KindInvalidInput)
		if *r.calls != 0 {
			t.Fatalf("%s: a request carrying a caller-supplied field reached the signer", extra)
		}
	}
	r := newAttestRig(t)
	_, err := serveAttestSign(context.Background(), r.deps, append(r.signed(t, r.request, r.ctrl), []byte(`{}`)...))
	wantKind(t, err, cascade.KindInvalidInput)
}

func TestAttestSignPayloadUnambiguous(t *testing.T) {
	r := newAttestRig(t)
	base := r.request
	shifted := base
	shifted.ExecutionID, shifted.Nonce = "exec-1n", "1"
	a, errA := AttestSignPayload("node-1", base)
	b, errB := AttestSignPayload("node-1", shifted)
	if errA != nil || errB != nil || bytes.Equal(a, b) {
		t.Fatalf("shifted fields share a payload: %v %v", errA, errB)
	}
	if !bytes.HasPrefix(a, []byte("cascade.ci.attest.sign.v1\x1fnode-1\x1f")) {
		t.Fatalf("payload lacks the domain prefix: %q", a)
	}
	seen := map[string]string{}
	for i, mutate := range []func(*AttestSignRequest){
		func(*AttestSignRequest) {}, func(q *AttestSignRequest) { q.ExecutionID += "x" },
		func(q *AttestSignRequest) { q.Nonce += "x" }, func(q *AttestSignRequest) { q.ExpiresAt = q.ExpiresAt.Add(time.Second) },
		func(q *AttestSignRequest) { q.CheckpointID += "x" }, func(q *AttestSignRequest) { q.RepoID += "x" },
		func(q *AttestSignRequest) { q.JobSpecHash += "x" }, func(q *AttestSignRequest) { q.PolicyVersion += "x" },
	} {
		q := base
		mutate(&q)
		p, err := AttestSignPayload("node-1", q)
		if err != nil {
			t.Fatal(err)
		}
		if prev, dup := seen[string(p)]; dup {
			t.Fatalf("tuple %d collides with %s", i, prev)
		}
		seen[string(p)] = strconv.Itoa(i)
	}
	for _, mutate := range []func(*AttestSignRequest){
		func(q *AttestSignRequest) { q.RepoID = "re\x1fpo" }, func(q *AttestSignRequest) { q.Nonce = "" },
		func(q *AttestSignRequest) { q.ExpiresAt = q.ExpiresAt.Add(time.Millisecond) },
	} {
		q := base
		mutate(&q)
		if _, err := AttestSignPayload("node-1", q); err == nil {
			t.Fatal("an ambiguous field was accepted")
		}
	}
	t.Run("expired request refused", func(t *testing.T) {
		r := newAttestRig(t)
		q := r.request
		q.ExpiresAt = r.now.Add(-time.Second)
		_, err := serveAttestSign(context.Background(), r.deps, r.signed(t, q, r.ctrl))
		wantKind(t, err, cascade.KindPermissionDenied)
		if *r.calls != 0 {
			t.Fatal("an expired request reached the signer")
		}
	})
}

// TestNodesImportsNoCIOrJobs guards verification[6]: internal/nodes never
// imports internal/ci or internal/jobs.
func TestNodesImportsNoCIOrJobs(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v", err)
	}
	for _, f := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			for _, banned := range []string{"/internal/ci", "/internal/jobs"} {
				if strings.HasSuffix(path, banned) || strings.Contains(path, banned+"/") {
					t.Errorf("%s imports %s", f, path)
				}
			}
		}
	}
}

func TestServeRegistryMountsAttestSignOnlyWithSigner(t *testing.T) {
	r := newAttestRig(t)
	without := NewServeRegistry(ServeDeps{Clock: r.deps.clock})
	err := without.Call(context.Background(), AttestSignMethod, json.RawMessage(r.signed(t, r.request, r.ctrl)), nil)
	wantKind(t, err, cascade.KindNotFound)
	with := NewServeRegistry(ServeDeps{Clock: r.deps.clock, Self: Identity{NodeID: "node-1"},
		AttestSigner: r.deps.signer, ControllerBinding: r.deps.binding})
	var out map[string]bool
	if err := with.Call(context.Background(), AttestSignMethod, json.RawMessage(r.signed(t, r.request, r.ctrl)), &out); err != nil || !out["signed"] {
		t.Fatalf("mounted call: %v %v", out, err)
	}
}

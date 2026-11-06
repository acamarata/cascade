package nodes

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

type putRig struct {
	binding memBinding
	ctrl    ed25519.PrivateKey
	calls   *int
	got     *AttestPutRequest
	putter  AttestPutter
	request AttestPutRequest
}

func newPutRig(t *testing.T) putRig {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	var got AttestPutRequest
	putter := func(_ context.Context, put AttestPutRequest) error {
		calls++
		got = put
		return nil
	}
	binding := ControllerBinding{Endpoint: "http://c.invalid", EnrollmentID: "e1", ControllerPubKeyB64: base64.StdEncoding.EncodeToString(pub)}
	return putRig{binding: memBinding{b: binding, ok: true}, ctrl: priv, calls: &calls, got: &got, putter: putter,
		request: AttestPutRequest{
			SignedAttestation: json.RawMessage(`{"record":{"run":"r1"},"sig":"s"}`),
			SignerPubKeyB64:   "c2lnbmVy",
			Acceptance: AttestPutAcceptance{CommitSHA: "c1", TreeHash: "t1", CheckpointID: "cp", Nonce: "n1",
				RepoID: "repo", JobSpecHash: "spec", PolicyVersion: "pv", PlanKinds: []string{"unit", "lint"},
				AcceptedAt: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)},
		}}
}

func (r putRig) deps() attestPutDeps { return attestPutDeps{binding: r.binding, putter: r.putter} }

// signedOver returns req carrying a signature by key over payload.
func signedOver(t *testing.T, req AttestPutRequest, key ed25519.PrivateKey, payload []byte) json.RawMessage {
	t.Helper()
	req.ControllerSig = base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload))
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (r putRig) signed(t *testing.T, req AttestPutRequest, key ed25519.PrivateKey) json.RawMessage {
	t.Helper()
	payload, err := AttestPutPayload(req)
	if err != nil {
		t.Fatal(err)
	}
	return signedOver(t, req, key, payload)
}

func withSig(t *testing.T, raw json.RawMessage, sig string) json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["controller_sig"], _ = json.Marshal(sig)
	out, _ := json.Marshal(m)
	return out
}

// unenrolledCallerCases builds the refusal table: each case returns a request
// the putter must reject as PermissionDenied.
func unenrolledCallerCases(strangerPub string) map[string]func(t *testing.T, r *putRig) json.RawMessage {
	return map[string]func(t *testing.T, r *putRig) json.RawMessage{
		"no pin": func(t *testing.T, r *putRig) json.RawMessage {
			r.binding = memBinding{}
			return r.signed(t, r.request, r.ctrl)
		},
		"pin of another key": func(t *testing.T, r *putRig) json.RawMessage {
			r.binding.b.ControllerPubKeyB64 = strangerPub
			return r.signed(t, r.request, r.ctrl)
		},
		"non-base64 sig": func(t *testing.T, r *putRig) json.RawMessage {
			return withSig(t, r.signed(t, r.request, r.ctrl), "!!not base64!!")
		},
		"63-byte sig": func(t *testing.T, r *putRig) json.RawMessage {
			payload, _ := AttestPutPayload(r.request)
			sig := ed25519.Sign(r.ctrl, payload)[:63]
			return withSig(t, r.signed(t, r.request, r.ctrl), base64.StdEncoding.EncodeToString(sig))
		},
		"acceptance altered after signing": func(t *testing.T, r *putRig) json.RawMessage {
			payload, _ := AttestPutPayload(r.request)
			q := r.request
			q.Acceptance.TreeHash = "t2"
			return signedOver(t, q, r.ctrl, payload)
		},
		"sig over bare canonical JSON": func(t *testing.T, r *putRig) json.RawMessage {
			payload, _ := AttestPutPayload(r.request)
			return signedOver(t, r.request, r.ctrl, bytes.TrimPrefix(payload, []byte(attestPutDomain+"\x1f")))
		},
		"sig under the attest-sign domain": func(t *testing.T, r *putRig) json.RawMessage {
			payload, _ := AttestPutPayload(r.request)
			body := bytes.TrimPrefix(payload, []byte(attestPutDomain+"\x1f"))
			return signedOver(t, r.request, r.ctrl, append([]byte("cascade.ci.attest.sign.v1\x1f"), body...))
		},
	}
}

// requirePinnedControllerStored asserts the pinned controller's request is stored verbatim.
func requirePinnedControllerStored(t *testing.T) {
	t.Helper()
	r := newPutRig(t)
	res, err := serveAttestPut(context.Background(), r.deps(), r.signed(t, r.request, r.ctrl))
	if err != nil || !res.Stored || *r.calls != 1 {
		t.Fatalf("pinned controller refused: %v %v (calls %d)", res, err, *r.calls)
	}
	want := r.request
	want.ControllerSig = r.got.ControllerSig
	wantRaw, _ := json.Marshal(want)
	gotRaw, _ := json.Marshal(*r.got)
	if !bytes.Equal(wantRaw, gotRaw) || r.got.ControllerSig == "" {
		t.Fatalf("putter got %s, want %s", gotRaw, wantRaw)
	}
}

func TestAttestPutRefusesUnenrolledCaller(t *testing.T) {
	_, stranger, _ := ed25519.GenerateKey(rand.Reader)
	strangerPub := base64.StdEncoding.EncodeToString(stranger.Public().(ed25519.PublicKey))
	cases := unenrolledCallerCases(strangerPub)
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			r := newPutRig(t)
			raw := build(t, &r)
			_, err := serveAttestPut(context.Background(), r.deps(), raw)
			wantKind(t, err, cascade.KindPermissionDenied)
			if *r.calls != 0 {
				t.Fatal("the putter ran for an unverified caller")
			}
		})
	}
	requirePinnedControllerStored(t)
}
func TestAttestPutPayloadExactBytes(t *testing.T) {
	r := newPutRig(t)
	got, err := AttestPutPayload(r.request)
	if err != nil {
		t.Fatal(err)
	}
	want := "cascade.ci.attest.put.v1" + "\x1f" +
		`{"acceptance":{"accepted_at":"2026-11-02T12:00:00Z","checkpoint_id":"cp","commit_sha":"c1",` +
		`"job_spec_hash":"spec","nonce":"n1","plan_kinds":["unit","lint"],"policy_version":"pv",` +
		`"repo_id":"repo","tree_hash":"t1"},"signed_attestation":{"record":{"run":"r1"},"sig":"s"},` +
		`"signer_pub_key_b64":"c2lnbmVy"}`
	if string(got) != want {
		t.Fatalf("payload\n got %q\nwant %q", got, want)
	}
	reordered := `{"controller_sig":"","acceptance":{"tree_hash":"t1","repo_id":"repo","policy_version":"pv",
		"plan_kinds":["unit","lint"],"nonce":"n1","job_spec_hash":"spec","commit_sha":"c1","checkpoint_id":"cp",
		"accepted_at":"2026-11-02T14:00:00+02:00"},"signer_pub_key_b64":"c2lnbmVy",
		"signed_attestation":{ "record" : { "run" : "r1" } ,
		"sig" : "s" }}`
	req, err := decodeAttestPutRequest(json.RawMessage(reordered))
	if err != nil {
		t.Fatal(err)
	}
	if p, err := AttestPutPayload(req); err != nil || string(p) != want {
		t.Fatalf("reordered params changed the payload: %q %v", p, err)
	}
	nilKinds, emptyKinds := r.request, r.request
	nilKinds.Acceptance.PlanKinds, emptyKinds.Acceptance.PlanKinds = nil, []string{}
	a, errA := AttestPutPayload(nilKinds)
	b, errB := AttestPutPayload(emptyKinds)
	if errA != nil || errB != nil || !bytes.Equal(a, b) || !strings.Contains(string(a), `"plan_kinds":[]`) {
		t.Fatalf("nil and empty plan_kinds differ: %q %q %v %v", a, b, errA, errB)
	}
}

func TestAttestPutStrictDecode(t *testing.T) {
	r := newPutRig(t)
	good := string(r.signed(t, r.request, r.ctrl))
	acc := `"acceptance":{`
	bad := map[string]string{
		"top-level job_id":       strings.Replace(good, "{", `{"job_id":"j1",`, 1),
		"acceptance expires_at":  strings.Replace(good, acc, acc+`"expires_at":"2026-11-02T13:00:00Z",`, 1),
		"trailing data":          good + `{}`,
		"missing tree_hash":      strings.Replace(good, `,"tree_hash":"t1"`, "", 1),
		"sub-second accepted_at": strings.Replace(good, `"2026-11-02T12:00:00Z"`, `"2026-11-02T12:00:00.5Z"`, 1),
		"signed_attestation x":   strings.Replace(good, `"signed_attestation":{"record":{"run":"r1"},"sig":"s"}`, `"signed_attestation":"x"`, 1),
	}
	for name, raw := range bad {
		t.Run(name, func(t *testing.T) {
			if raw == good {
				t.Fatal("the case did not change the params")
			}
			r := newPutRig(t)
			_, err := serveAttestPut(context.Background(), r.deps(), json.RawMessage(raw))
			wantKind(t, err, cascade.KindInvalidInput)
			if *r.calls != 0 {
				t.Fatal("refused params reached the putter")
			}
		})
	}
	payload, err := AttestPutPayload(r.request)
	if err != nil {
		t.Fatal(err)
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(r.ctrl, payload))
	literal := `{"signed_attestation":{"record":{"run":"r1"},"sig":"s"},"signer_pub_key_b64":"c2lnbmVy",` +
		`"acceptance":{"commit_sha":"c1","tree_hash":"t1","checkpoint_id":"cp","nonce":"n1","repo_id":"repo",` +
		`"job_spec_hash":"spec","policy_version":"pv","plan_kinds":["unit","lint"],"accepted_at":"2026-11-02T12:00:00Z"},` +
		`"controller_sig":"` + sig + `"}`
	if res, err := serveAttestPut(context.Background(), r.deps(), json.RawMessage(literal)); err != nil || !res.Stored || *r.calls != 1 {
		t.Fatalf("contract literal refused: %v %v (calls %d)", res, err, *r.calls)
	}
}

func TestServeRegistryMountsAttestPutOnlyWithPutter(t *testing.T) {
	r := newPutRig(t)
	raw := r.signed(t, r.request, r.ctrl)
	without := NewServeRegistry(ServeDeps{ControllerBinding: r.binding})
	wantKind(t, without.Call(context.Background(), AttestPutMethod, raw, nil), cascade.KindNotFound)
	with := NewServeRegistry(ServeDeps{AttestPutter: r.putter, ControllerBinding: r.binding})
	var out AttestPutResult
	if err := with.Call(context.Background(), AttestPutMethod, raw, &out); err != nil || !out.Stored || *r.calls != 1 {
		t.Fatalf("mounted call: %v %v (calls %d)", out, err, *r.calls)
	}
}

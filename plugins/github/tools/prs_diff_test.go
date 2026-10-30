package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// captureDigest is the sha256 of testdata/prs-diff/response.diff as
// recorded in testdata/README.md.
const captureDigest = "807e98f3f92408debcb5027b3b51ed45ba084b39855fec2f507e0e60ba171815"

func diffArgs(number int) Args { return Args{Owner: "octocat", Repo: "Hello-World", Number: number} }

// TestPRDiffRequestSendsDiffAccept pins the host, path and diff media type,
// and that every other tool still asks for GitHub JSON.
func TestPRDiffRequestSendsDiffAccept(t *testing.T) {
	req, err := BuildRequest("prs.diff", diffArgs(1))
	if err != nil {
		t.Fatal(err)
	}
	got, err := (HTTPDoer{}).build(context.Background(), req, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "GET" || got.URL.String() != "https://api.github.com/repos/octocat/Hello-World/pulls/1" {
		t.Fatalf("request = %s %s", got.Method, got.URL)
	}
	if a := got.Header.Get("Accept"); a != "application/vnd.github.diff" {
		t.Fatalf("Accept = %q, want application/vnd.github.diff", a)
	}
	plain, err := BuildRequest("prs.get", diffArgs(1))
	if err != nil {
		t.Fatal(err)
	}
	if plain.Accept != "" || APIHeaders(plain, "")["Accept"] != "application/vnd.github+json" {
		t.Fatalf("prs.get Accept = %q / %q, want empty / GitHub JSON", plain.Accept, APIHeaders(plain, "")["Accept"])
	}
}

// TestDecodePRDiffCapsSize checks both sides of the exact 8 MiB boundary in
// the decoder and that the transport hands the decoder one byte past it.
func TestDecodePRDiffCapsSize(t *testing.T) {
	if MaxPRDiffBytes != 8*1024*1024 {
		t.Fatalf("MaxPRDiffBytes = %d, want 8 MiB", MaxPRDiffBytes)
	}
	for _, size := range []int{0, MaxPRDiffBytes - 1, MaxPRDiffBytes} {
		body := bytes.Repeat([]byte("x"), size)
		got, err := DecodePRDiff(body)
		if err != nil || got.Diff != string(body) || got.Bytes != size {
			t.Fatalf("size %d: diff len %d bytes %d err %v", size, len(got.Diff), got.Bytes, err)
		}
	}
	over := bytes.Repeat([]byte("x"), MaxPRDiffBytes+1)
	got, err := DecodePRDiff(over)
	requireKindMsg(t, err, cascade.KindInvalidInput, "pull request diff exceeds 8 MiB")
	if got != (PRDiff{}) {
		t.Fatalf("refusal returned a partial diff of %d bytes", got.Bytes)
	}
	read, err := readResponse(Request{Accept: AcceptPRDiff}, bytes.NewReader(append(over, 'y')))
	if err != nil || len(read) != MaxPRDiffBytes+1 {
		t.Fatalf("transport read %d bytes, err %v; want %d and no error", len(read), err, MaxPRDiffBytes+1)
	}
}

// TestPRDiffMapsTooLargeToUnsupported: GitHub's 406 for prs.diff is
// KindUnsupported; an over-cap 200 is KindInvalidInput; 404 and 401/403
// keep their mapping; a 406 on another tool keeps the generic mapping.
func TestPRDiffMapsTooLargeToUnsupported(t *testing.T) {
	cases := []struct {
		doer *recordingDoer
		tool string
		want cascade.Kind
	}{
		{&recordingDoer{status: 406, body: []byte(`{"message":"Sorry, the diff exceeded the maximum number of files"}`)}, "prs.diff", cascade.KindUnsupported},
		{&recordingDoer{status: 406}, "prs.get", cascade.KindInvalidInput},
		{&recordingDoer{status: 200, body: bytes.Repeat([]byte("x"), MaxPRDiffBytes+1)}, "prs.diff", cascade.KindInvalidInput},
		{&recordingDoer{status: 404}, "prs.diff", cascade.KindNotFound},
		{&recordingDoer{status: 401}, "prs.diff", cascade.KindPermissionDenied},
		{&recordingDoer{status: 403, body: []byte(`{"message":"Resource not accessible"}`)}, "prs.diff", cascade.KindPermissionDenied},
	}
	for _, tc := range cases {
		_, err := (Client{Doer: tc.doer}).Call(context.Background(), tc.tool, diffArgs(1))
		if got, ok := cascade.KindOf(err); !ok || got != tc.want {
			t.Errorf("%s status %d: kind %v (err %v), want %v", tc.tool, tc.doer.status, got, err, tc.want)
		}
	}
}

// TestPRDiffRejectsBadNumber: number <= 0 is KindInvalidInput before any
// request is sent.
func TestPRDiffRejectsBadNumber(t *testing.T) {
	for _, number := range []int{-1, 0} {
		doer := &recordingDoer{status: 200, body: []byte("diff --git a b\n")}
		_, err := (Client{Doer: doer}).Call(context.Background(), "prs.diff", diffArgs(number))
		if got, ok := cascade.KindOf(err); !ok || got != cascade.KindInvalidInput {
			t.Fatalf("number %d: kind %v err %v, want KindInvalidInput", number, got, err)
		}
		if len(doer.calls) != 0 {
			t.Fatalf("number %d reached the transport", number)
		}
	}
}

// TestPRDiffRealTranscript decodes the api.github.com capture: status 200,
// the diff media type, the recorded digest, and the body unchanged.
func TestPRDiffRealTranscript(t *testing.T) {
	body, err := os.ReadFile("testdata/prs-diff/response.diff")
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(body); hex.EncodeToString(sum[:]) != captureDigest {
		t.Fatalf("capture digest = %x, want %s", sum, captureDigest)
	}
	meta := captureMeta(t)
	if meta["status"] != "200" || !strings.HasPrefix(meta["content-type"], AcceptPRDiff+";") {
		t.Fatalf("capture status %q content-type %q: GitHub's diff media type differs (escalate)", meta["status"], meta["content-type"])
	}
	readme, err := os.ReadFile("testdata/README.md")
	if err != nil || !strings.Contains(string(readme), meta["x-github-request-id"]) || !strings.Contains(string(readme), captureDigest) {
		t.Fatalf("testdata/README.md lacks this capture's provenance (err %v)", err)
	}
	doer := &recordingDoer{status: 200, body: body}
	got, err := (Client{Doer: doer}).Call(context.Background(), "prs.diff", diffArgs(1))
	if err != nil {
		t.Fatal(err)
	}
	if got != (PRDiff{Diff: string(body), Bytes: len(body)}) || len(body) != 520 {
		t.Fatalf("decoded %#v, want the 520-byte capture unchanged", got)
	}
	if len(doer.calls) != 1 || doer.calls[0].Accept != AcceptPRDiff || doer.calls[0].URL() != meta["request"][len("GET "):] {
		t.Fatalf("sent %+v, want the captured request", doer.calls)
	}
}

func captureMeta(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("testdata/prs-diff/capture.txt")
	if err != nil {
		t.Fatal(err)
	}
	meta := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if k, v, ok := strings.Cut(line, ": "); ok {
			meta[k] = v
		}
	}
	return meta
}

func requireKindMsg(t *testing.T, err error, kind cascade.Kind, msg string) {
	t.Helper()
	if got, ok := cascade.KindOf(err); !ok || got != kind || !strings.Contains(err.Error(), msg) {
		t.Fatalf("err = %v (kind %v), want %v %q", err, got, kind, msg)
	}
}

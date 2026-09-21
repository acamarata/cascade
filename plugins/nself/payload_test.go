// Purpose: the payload's two credential layers and the fuzz targets over
//
//	the two things this package parses or produces from untrusted bytes.
//
// WHY THE FUZZ ORACLES ARE INDEPENDENT. The review found the deleted
//
//	handshake fuzzer's property oracle WAS the implementation's own prefix
//	helper, so it could only ever agree with itself and the no-panic claim
//	was the only real one. Both targets below assert properties stated
//	WITHOUT reference to the code under test: one decodes the same bytes
//	with encoding/json into a generic map, the other extracts every
//	URL-shaped run's authority by hand and checks it for userinfo (with
//	net/url as a second opinion wherever the run parses).
//
// SPORT: plugins/nself entity (TEST) — P1-E25-W5-S52-T2.

package nself

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestScrub_ReplacesCredentialShapesAndLeavesPathsAlone(t *testing.T) {
	redacted := []string{
		"postgres://cascade:hunter2@127.0.0.1:5432/cascade",
		"redis://user:pw@localhost:6379/0",
		"amqps://a:b@host/vhost",
		"password=hunter2",
		"API_KEY: abc123",
		"the token = abc",
	}
	for _, in := range redacted {
		if got := scrub(in); got != redactedMarker {
			t.Errorf("scrub(%q) = %q, want %q", in, got, redactedMarker)
		}
	}
	kept := []string{
		"", "none", "marker-dir", "/opt/homebrew/bin/nself",
		"/Users/someone/Sites/app/.nself",
		"postgres://127.0.0.1:5432/cascade", // no userinfo: not credential-shaped
		scanWarningText,
		redactedMarker, // idempotent
	}
	for _, in := range kept {
		if got := scrub(in); got != in {
			t.Errorf("scrub(%q) = %q, want it unchanged", in, got)
		}
	}
}

// TestScrub_RedactsCloudAndBareTokenShapes pins the survivors the confirming
// review found at THIS layer, independently of what the egress firewall
// would have done with them: the two AWS shapes the old `\b` could not
// match after an underscore, a bearer token, and a bare hex token (the one
// shape that survived BOTH layers). Every credential below is a published
// documentation placeholder, not a real one.
func TestScrub_RedactsCloudAndBareTokenShapes(t *testing.T) {
	redacted := map[string]string{
		"aws key id":     "aws_access_key_id=AKIAIOSFODNN7EXAMPLE",
		"aws secret env": "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"bearer":         "Authorization: Bearer sk-ant-api03-not-a-real-token",
		"bare 40-hex":    "/srv/app/0123456789abcdef0123456789abcdef01234567",
		"bare 32-hex":    "0123456789ABCDEF0123456789abcdef",
		"bare 64-hex":    strings.Repeat("ab", 32),
	}
	for name, in := range redacted {
		if got := scrub(in); got != redactedMarker {
			t.Errorf("scrub(%s) = %q, want %q", name, got, redactedMarker)
		}
	}
	kept := map[string]string{
		"short hex path segment": "/var/cache/ab12cd34/build-version",
		"16-hex corpus filename": "testdata/fuzz/FuzzNselfProjectInfoPayload/f0d146a1bbdf5b4b",
		"a normal word":          "marker-dir",
		"a bearer of bad news":   "the bearer of bad news",
		"the binary path":        "/opt/homebrew/bin/nself",
	}
	for name, in := range kept {
		if got := scrub(in); got != in {
			t.Errorf("scrub(%s) = %q, want it unchanged", name, got)
		}
	}
}

func TestScrubbed_CoversEveryStringFieldOfTheResponse(t *testing.T) {
	const leak = "postgres://u:p@h:5432/db"
	in := projectInfoResponse{
		Method:     leak,
		MarkerPath: leak,
		Doctor: doctorWire{
			BinaryPath:   leak,
			ProbeOutcome: leak,
			ScanWarning:  leak,
			Error:        leak,
		},
	}
	got := in.scrubbed()
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "u:p@") || strings.Contains(string(encoded), "postgres://") {
		t.Fatalf("scrubbed response = %s, want every field redacted", encoded)
	}
	for name, field := range map[string]string{
		"Method": got.Method, "MarkerPath": got.MarkerPath,
		"BinaryPath": got.Doctor.BinaryPath, "ProbeOutcome": got.Doctor.ProbeOutcome,
		"ScanWarning": got.Doctor.ScanWarning, "Error": got.Doctor.Error,
	} {
		if field != redactedMarker {
			t.Errorf("%s = %q, want %q", name, field, redactedMarker)
		}
	}
}

// TestNewProjectInfoResponse_NeverCarriesSubprocessOutput is layer 1: the
// response is BUILT from code-chosen values, so a probe error that somehow
// carried the child's output still cannot put it on the wire.
func TestNewProjectInfoResponse_NeverCarriesSubprocessOutput(t *testing.T) {
	leaky := errors.New("nself said postgres://u:hunter2@db:5432/x is down")
	res := result{Method: "none", ProbeErr: leaky, ScanErr: errors.New("permission denied: /Users/someone/private")}
	got := newProjectInfoResponse(res, doctorResult{}, nil)
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{"hunter2", "postgres://", "/Users/someone/private", "permission denied"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("response = %s, want no trace of %q", encoded, forbidden)
		}
	}
	if got.Doctor.ProbeOutcome != probeOutcomeUnavailable {
		t.Errorf("ProbeOutcome = %q, want %q", got.Doctor.ProbeOutcome, probeOutcomeUnavailable)
	}
	if got.Doctor.ScanWarning != scanWarningText {
		t.Errorf("ScanWarning = %q, want the one fixed sentence", got.Doctor.ScanWarning)
	}
}

func TestClassifyProbe_EveryTypedOutcome(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"nil":      {nil, probeOutcomeOK},
		"absent":   {&binaryAbsentError{Binary: "nself", GOOS: "darwin"}, probeOutcomeBinaryAbsent},
		"timeout":  {&probeTimeoutError{Binary: "nself", Timeout: time.Second}, probeOutcomeTimedOut},
		"non-zero": {&probeFailedError{Binary: "nself", ExitCode: 1}, probeOutcomeRanAndFailed},
		"other":    {errors.New("boom"), probeOutcomeUnavailable},
	}
	for name, c := range cases {
		if got := classifyProbe(c.err); got != c.want {
			t.Errorf("classifyProbe(%s) = %q, want %q", name, got, c.want)
		}
	}
}

// FuzzNselfToolInput fuzzes the one thing this package decodes from an
// untrusted caller. Oracle, stated independently of parseToolInput: it must
// never panic, and whenever it reports a root directory, a plain
// encoding/json decode of the SAME bytes into a generic map must carry that
// exact string under a key that encoding/json would bind to the root_dir
// field.
//
// WHY THE LOOKUP IS CASE-INSENSITIVE. The first version of this oracle asked
// the generic map for "root_dir" exactly, and the confirming review's live
// fuzz found {"Root_dir":"0"} in 0.35s: encoding/json matches struct fields
// case-insensitively when no exact match is present, so the struct decode
// binds "Root_dir" while an exact map lookup finds nothing. That is Go's
// documented default and harmless here — the oracle was the thing that was
// wrong. The crasher is committed as a seed
// (testdata/fuzz/FuzzNselfToolInput/seed002).
func FuzzNselfToolInput(f *testing.F) {
	f.Add([]byte(`{"root_dir":"/srv/app"}`))
	f.Add([]byte(`{"root_dir":123}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))
	f.Add([]byte(``))
	f.Fuzz(func(t *testing.T, data []byte) {
		got := parseToolInput(data)
		if got.RootDir == "" {
			return
		}
		var generic map[string]any
		if err := json.Unmarshal(data, &generic); err != nil {
			t.Fatalf("parseToolInput reported root_dir=%q from bytes encoding/json rejects", got.RootDir)
		}
		for key, value := range generic {
			s, isString := value.(string)
			if isString && s == got.RootDir && strings.EqualFold(key, "root_dir") {
				return
			}
		}
		t.Fatalf("parseToolInput root_dir = %q, no case-insensitive root_dir key in the independent decode carries it: %v", got.RootDir, generic)
	})
}

// urlRun matches any scheme://... run in the encoded payload, so the oracle
// below never asks the implementation what a URL is.
var urlRun = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.\-]*://[^"\s]*`)

// FuzzNselfProjectInfoPayload fuzzes the fields a hostile or buggy
// environment could push into the payload. Oracle, stated independently of
// scrub: the encoded bytes must be valid JSON, and every URL run they
// contain must parse with net/url and carry NO userinfo — the one property
// that holds for arbitrary input. The narrower "the value comes from a
// literal set" property is asserted where it is actually true, over the
// real detection outcomes, by
// TestNewProjectInfoResponse_MethodAndOutcomeComeFromLiteralSets below.
func FuzzNselfProjectInfoPayload(f *testing.F) {
	f.Add("marker-dir", "/srv/app/.nself", "/opt/bin/nself")
	f.Add("none", "", "postgres://cascade:hunter2@127.0.0.1:5432/cascade")
	f.Add("subprocess", "password=hunter2", "redis://u:p@h/0")
	f.Fuzz(func(t *testing.T, method, markerPath, binaryPath string) {
		resp := projectInfoResponse{
			Method:     method,
			MarkerPath: markerPath,
			Doctor:     doctorWire{BinaryPath: binaryPath, ProbeOutcome: probeOutcomeOK},
		}
		encoded, err := json.Marshal(resp.scrubbed())
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !json.Valid(encoded) {
			t.Fatalf("encoded payload is not valid JSON: %s", encoded)
		}
		for _, run := range urlRun.FindAllString(string(encoded), -1) {
			authority := run[strings.Index(run, "://")+len("://"):]
			if i := strings.IndexAny(authority, "/?#"); i >= 0 {
				authority = authority[:i]
			}
			if strings.Contains(authority, "@") {
				t.Fatalf("payload carries userinfo in %q", run)
			}
			if u, perr := url.Parse(run); perr == nil && u.User != nil {
				t.Fatalf("net/url reports userinfo in %q", run)
			}
		}
	})
}

// TestNewProjectInfoResponse_MethodAndOutcomeComeFromLiteralSets asserts the
// literal-set property over the REAL detection outcomes: whatever the
// filesystem and the probe do, Method and ProbeOutcome are always one of
// this package's own constants, never a value carried in from outside.
func TestNewProjectInfoResponse_MethodAndOutcomeComeFromLiteralSets(t *testing.T) {
	methods := map[string]bool{"marker-dir": true, "subprocess": true, "none": true}
	outcomes := map[string]bool{
		probeOutcomeOK: true, probeOutcomeBinaryAbsent: true, probeOutcomeTimedOut: true,
		probeOutcomeRanAndFailed: true, probeOutcomeUnavailable: true,
	}
	cases := []struct {
		name   string
		fs     fakeFS
		runner *recordingRunner
	}{
		{"marker hit", newFakeFS().project("/repo"), &recordingRunner{}},
		{"probe ok", newFakeFS(), &recordingRunner{out: []byte(`{}`)}},
		{"probe failed", newFakeFS(), &recordingRunner{err: &probeFailedError{Binary: "nself", ExitCode: 1}}},
		{"binary absent", newFakeFS(), &recordingRunner{err: &binaryAbsentError{Binary: "nself", GOOS: "darwin"}}},
		{"timed out", newFakeFS(), &recordingRunner{err: &probeTimeoutError{Binary: "nself", Timeout: time.Second}}},
	}
	for _, c := range cases {
		d := &detector{fs: c.fs, runner: c.runner, binary: "nself", rootDir: "/repo"}
		got := newProjectInfoResponse(d.Detect(context.Background()), doctorResult{}, nil).scrubbed()
		if !methods[got.Method] {
			t.Errorf("%s: Method = %q, outside the literal set", c.name, got.Method)
		}
		if !outcomes[got.Doctor.ProbeOutcome] {
			t.Errorf("%s: ProbeOutcome = %q, outside the literal set", c.name, got.Doctor.ProbeOutcome)
		}
	}
}

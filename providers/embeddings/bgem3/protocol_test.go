package bgem3

// Purpose: the protocol half's unit suite plus FuzzBgeM3SidecarDecode, the
//   required fuzz target over decodeResponseFrame (06-FORGE-SPEC §5 rule 7).
//   Everything decodeResponseFrame reads comes from a separate process, so
//   it is untrusted input by this repo's own definition and may never
//   panic, block, or allocate ahead of the bytes that arrived.
// Constraints: seed corpus at internal/testdata/fuzz/bgem3-sidecar/ with a
//   provenance README this file asserts exists. Deterministic, no network,
//   no temp files.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fuzzSeedDir is the shared corpus root this plan mandates, relative to
// this package.
const fuzzSeedDir = "../../../internal/testdata/fuzz/bgem3-sidecar"

// frameOf wraps payload in the protocol's length prefix, bypassing
// encodeFrame so a test can build frames encodeFrame would refuse.
func frameOf(payload []byte) []byte {
	out := make([]byte, frameHeaderBytes+len(payload))
	binary.BigEndian.PutUint32(out[:frameHeaderBytes], uint32(len(payload)))
	copy(out[frameHeaderBytes:], payload)
	return out
}

// headerFor builds a bare length prefix declaring n payload bytes.
func headerFor(n uint32) []byte {
	out := make([]byte, frameHeaderBytes)
	binary.BigEndian.PutUint32(out, n)
	return out
}

func TestEncodeRequestFrameRoundTrips(t *testing.T) {
	frame, err := encodeRequestFrame("bge-m3", 4, []string{"alpha", "beta"})
	if err != nil {
		t.Fatalf("encodeRequestFrame: %v", err)
	}
	declared := binary.BigEndian.Uint32(frame[:frameHeaderBytes])
	if int(declared) != len(frame)-frameHeaderBytes {
		t.Fatalf("length prefix %d does not match payload of %d bytes",
			declared, len(frame)-frameHeaderBytes)
	}
	payload := string(frame[frameHeaderBytes:])
	for _, want := range []string{`"protocol_version":"1.0"`, `"op":"embed"`,
		`"model":"bge-m3"`, `"dimensions":4`, `"inputs":["alpha","beta"]`} {
		if !strings.Contains(payload, want) {
			t.Errorf("request payload %s missing %s", payload, want)
		}
	}
}

func TestEncodeFrameRefusesUnsendablePayloads(t *testing.T) {
	if _, err := encodeFrame(nil); !cascade.HasKind(err, cascade.KindInternal) {
		t.Errorf("empty payload: got %v, want KindInternal", err)
	}
	if _, err := encodeFrame(make([]byte, maxFrameBytes+1)); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("oversized payload: got %v, want KindInvalidInput", err)
	}
}

func TestDecodeResponseFrameAcceptsAConformingFrame(t *testing.T) {
	body := `{"protocol_version":"1.0","model":"bge-m3","dimensions":2,"vectors":[[0.5,-0.5]]}`
	resp, err := decodeResponseFrame(bytes.NewReader(frameOf([]byte(body))))
	if err != nil {
		t.Fatalf("decodeResponseFrame: %v", err)
	}
	if resp.Model != "bge-m3" || resp.Dimensions != 2 || len(resp.Vectors) != 1 {
		t.Fatalf("decoded %+v, want the frame's own values", resp)
	}
}

func TestDecodeResponseFrameStopsAtTheFrameBoundary(t *testing.T) {
	stream := append(frameOf([]byte(`{"protocol_version":"1.0"}`)), []byte("TRAILING")...)
	r := bytes.NewReader(stream)
	if _, err := decodeResponseFrame(r); err != nil {
		t.Fatalf("decodeResponseFrame: %v", err)
	}
	rest, _ := io.ReadAll(r)
	if string(rest) != "TRAILING" {
		t.Fatalf("decoder consumed past its frame: %q left, want %q", rest, "TRAILING")
	}
}

func TestDecodeResponseFrameRefusesMalformedFrames(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		is   error
	}{
		{name: "empty stream", in: nil, is: errFrameHeaderShort},
		{name: "partial header", in: headerFor(64)[:2], is: errFrameHeaderShort},
		{name: "zero length", in: headerFor(0), is: errFrameEmpty},
		{name: "over the cap", in: headerFor(maxFrameBytes + 1), is: errFrameTooLarge},
		{name: "max uint32 length", in: headerFor(^uint32(0)), is: errFrameTooLarge},
		{name: "truncated payload", in: append(headerFor(64), []byte(`{"pro`)...), is: errFramePayloadShort},
		{is: errPayloadNotJSON, name: "garbage payload", in: frameOf([]byte("\x00\x01\x02not json"))},
		{is: errPayloadNotJSON, name: "scalar payload", in: frameOf([]byte("123"))},
		{is: errPayloadNotJSON, name: "array payload", in: frameOf([]byte("[1,2,3]"))},
		{is: errPayloadNotJSON, name: "component overflows float32", in: frameOf([]byte(`{"vectors":[[1e400]]}`))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := decodeResponseFrame(bytes.NewReader(tc.in))
			if err == nil {
				t.Fatalf("decoded %+v, want an error", resp)
			}
			if resp != nil {
				t.Errorf("returned a response alongside the error: %+v", resp)
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Errorf("got %v, want it to wrap %v", err, tc.is)
			}
		})
	}
}

// TestDecodeResponseFrameDoesNotAllocateTheDeclaredLength pins the property
// the corpus's at-cap-but-truncated seed exists for: a peer that announces
// the 16 MiB cap and then sends two bytes must cost two bytes, not 16 MiB.
func TestDecodeResponseFrameDoesNotAllocateTheDeclaredLength(t *testing.T) {
	in := append(headerFor(maxFrameBytes), []byte("{}")...)
	allocs := testing.AllocsPerRun(3, func() {
		_, _ = decodeResponseFrame(bytes.NewReader(in))
	})
	if allocs > 32 {
		t.Fatalf("%v allocations for a 2-byte truncated frame; the decoder is sizing from the declared length", allocs)
	}
}

func TestKindForSidecarCodeMapsTheClosedSet(t *testing.T) {
	want := map[string]cascade.Kind{
		"invalid_input":             cascade.KindInvalidInput,
		"unsupported":               cascade.KindUnsupported,
		"unavailable":               cascade.KindUnavailable,
		"timeout":                   cascade.KindTimeout,
		"canceled":                  cascade.KindCanceled,
		"quota_exhausted":           cascade.KindQuotaExhausted,
		"permission_denied":         cascade.KindPermissionDenied,
		"model_mismatch":            cascade.KindIntegrity,
		"internal":                  cascade.KindInternal,
		"":                          cascade.KindInternal,
		"a_code_no_version_defines": cascade.KindInternal,
	}
	for code, kind := range want {
		if got := kindForSidecarCode(code); got != kind {
			t.Errorf("code %q mapped to %v, want %v", code, got, kind)
		}
	}
}

func TestCheckProtocolVersion(t *testing.T) {
	tests := []struct {
		version string
		wantErr bool
	}{
		{version: "1.0"},
		{version: "1.9"},
		{version: "1.42"},
		{version: "2.0", wantErr: true},
		{version: "0.9", wantErr: true},
		{version: "", wantErr: true},
		{version: "1", wantErr: true},
		{version: "1.0.0", wantErr: true},
		{version: "one.zero", wantErr: true},
		{version: "1.x", wantErr: true},
		{version: "-1.0", wantErr: true},
	}
	for _, tc := range tests {
		err := checkProtocolVersion(tc.version)
		switch {
		case tc.wantErr && !cascade.HasKind(err, cascade.KindUnsupported):
			t.Errorf("version %q: got %v, want KindUnsupported", tc.version, err)
		case !tc.wantErr && err != nil:
			t.Errorf("version %q: got %v, want it accepted", tc.version, err)
		}
	}
}

func TestFuzzBgeM3SidecarDecodeSeedProvenanceExists(t *testing.T) {
	info, err := os.Stat(filepath.Join(fuzzSeedDir, "README.md"))
	if err != nil {
		t.Fatalf("provenance README missing at %s: %v", fuzzSeedDir, err)
	}
	if info.IsDir() {
		t.Fatalf("%s/README.md is a directory, want a file", fuzzSeedDir)
	}
}

// loadSeedFrames reads every *.bin seed. Go auto-loads only a target
// package's own testdata/fuzz/<FuzzName>/, and this corpus lives at the
// plan's shared root, so the explicit load is what makes the seeds count.
func loadSeedFrames(f *testing.F) [][]byte {
	f.Helper()
	entries, err := os.ReadDir(fuzzSeedDir)
	if err != nil {
		f.Fatalf("reading fuzz seed dir %s: %v", fuzzSeedDir, err)
	}
	var seeds [][]byte
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".bin" {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(fuzzSeedDir, e.Name()))
		if readErr != nil {
			f.Fatalf("reading seed %s: %v", e.Name(), readErr)
		}
		seeds = append(seeds, data)
	}
	if len(seeds) == 0 {
		f.Fatalf("no *.bin seed files found in %s", fuzzSeedDir)
	}
	return seeds
}

// FuzzBgeM3SidecarDecode drives decodeResponseFrame over arbitrary bytes.
// The decoder must never panic and must never claim success on input that
// was not a decodable JSON object: a nil error means the client is about
// to treat the payload as a sidecar's answer.
func FuzzBgeM3SidecarDecode(f *testing.F) {
	for _, seed := range loadSeedFrames(f) {
		f.Add(seed)
	}
	for _, seed := range [][]byte{
		nil, {}, {0x00}, headerFor(1), frameOf([]byte("{")),
		frameOf(bytes.Repeat([]byte("["), 4096)),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("decodeResponseFrame panicked on %q: %v", in, r)
			}
		}()
		r := bytes.NewReader(in)
		resp, err := decodeResponseFrame(r)
		if err != nil {
			if resp != nil {
				t.Fatalf("error %v returned alongside response %+v", err, resp)
			}
			return
		}
		if resp == nil {
			t.Fatal("nil response with a nil error")
		}
		// A frame the decoder accepted consumed its header plus exactly
		// its declared payload, never more: bytes past the frame belong
		// to whatever comes next on the stream.
		declared := int(binary.BigEndian.Uint32(in[:frameHeaderBytes]))
		if consumed := len(in) - r.Len(); consumed != frameHeaderBytes+declared {
			t.Fatalf("consumed %d bytes for a frame declaring %d", consumed, declared)
		}
	})
}

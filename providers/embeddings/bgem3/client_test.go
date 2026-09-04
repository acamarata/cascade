package bgem3

// Purpose: the client's unit suite — the happy path, every refusal path,
//   and the two ways a call is given up on. The conformance server and the
//   response builders these tests drive live in conformance_test.go.
// Constraints: deterministic, no network, no temp files, no sleeps in the
//   assertion path.

import (
	"context"
	"errors"
	"io"
	"math"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestNewRefusesAnUnusableConfig(t *testing.T) {
	dial := func(context.Context) (io.ReadWriteCloser, error) { return nil, errors.New("unused") }
	for name, cfg := range map[string]Config{
		"no dialer":        {Model: testModel},
		"no model id":      {Dial: dial, Model: provider.EmbedModel{Dimensions: 2}},
		"no width":         {Dial: dial, Model: provider.EmbedModel{ID: "bge-m3"}},
		"negative width":   {Dial: dial, Model: provider.EmbedModel{ID: "bge-m3", Dimensions: -1}},
		"negative timeout": {Dial: dial, Model: testModel, Timeout: -time.Second},
	} {
		cfg := cfg
		t.Run(name, func(t *testing.T) {
			c, err := New(cfg)
			if c != nil {
				t.Error("returned a client alongside the error")
			}
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Errorf("got %v, want KindInvalidInput", err)
			}
		})
	}
}

func TestEmbedEmptyBatchOpensNoConnection(t *testing.T) {
	dialed := false
	c := newTestClient(t, func(context.Context) (io.ReadWriteCloser, error) {
		dialed = true
		return nil, errors.New("must not be reached")
	}, 0)
	if got := c.Model(); !got.Equal(testModel) {
		t.Errorf("Model() = %+v, want %+v", got, testModel)
	}
	out, err := c.Embed(context.Background(), nil)
	if err != nil || len(out) != 0 {
		t.Fatalf("Embed(nil) = %v, %v; want an empty batch and no error", out, err)
	}
	if dialed {
		t.Fatal("an empty batch opened a connection")
	}
}

func TestEmbedReturnsVerifiedVectorsInOrder(t *testing.T) {
	s := newSeam(t, func(t *testing.T, req wireRequest) []byte {
		if req.Op != opEmbed || req.ProtocolVersion != ProtocolVersion {
			t.Errorf("request op/version = %q/%q", req.Op, req.ProtocolVersion)
		}
		if req.Model != testModel.ID || req.Dimensions != testModel.Dimensions {
			t.Errorf("request identity = %q/%d", req.Model, req.Dimensions)
		}
		if len(req.Inputs) != 2 || req.Inputs[0] != "alpha" || req.Inputs[1] != "beta" {
			t.Errorf("request inputs = %v, want the batch as given", req.Inputs)
		}
		return answer(t, good([]float32{0, 1}, []float32{2, 3}))
	})
	out, err := newTestClient(t, s.dial, time.Minute).
		Embed(context.Background(), inputsOf("alpha", "beta"))
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d outputs, want 2", len(out))
	}
	for i, o := range out {
		if !o.Model.Equal(testModel) || len(o.Vector) != 2 || o.Vector[0] != float32(2*i) {
			t.Errorf("output %d = %+v, want the sidecar's vector tagged with the model", i, o)
		}
	}
}

// TestEmbedRefusesEveryNonConformingAnswer is the anti-fabrication suite:
// each answer is wrong in one way, and the client must return a typed
// error and NO vectors — never a zero, truncated, or padded embedding
// standing in for a missing one.
func TestEmbedRefusesEveryNonConformingAnswer(t *testing.T) {
	pair := good([]float32{1, 2}, []float32{3, 4})
	failure := func(code string) wireResponse {
		return wireResponse{ProtocolVersion: ProtocolVersion,
			Error: &wireError{Code: code, Message: "refused"}}
	}
	for _, tc := range []struct {
		name string
		resp wireResponse
		want cascade.Kind
	}{
		{"major version mismatch", edit(pair, func(r *wireResponse) { r.ProtocolVersion = "2.0" }), cascade.KindUnsupported},
		{"absent version", edit(pair, func(r *wireResponse) { r.ProtocolVersion = "" }), cascade.KindUnsupported},
		{"sidecar error", failure("quota_exhausted"), cascade.KindQuotaExhausted},
		{"unknown error code", failure("moon_phase"), cascade.KindInternal},
		{"vectors and error together", edit(pair, func(r *wireResponse) {
			r.Error = &wireError{Code: "internal", Message: "both"}
		}), cascade.KindIntegrity},
		{"different model", edit(pair, func(r *wireResponse) { r.Model = "e5-large" }), cascade.KindIntegrity},
		{"different width", edit(pair, func(r *wireResponse) { r.Dimensions = 3 }), cascade.KindIntegrity},
		{"too few vectors", good([]float32{1, 2}), cascade.KindIntegrity},
		{"too many vectors", good([]float32{1, 2}, []float32{3, 4}, []float32{5, 6}), cascade.KindIntegrity},
		{"truncated vector", good([]float32{1, 2}, []float32{3}), cascade.KindIntegrity},
		{"padded vector", good([]float32{1, 2}, []float32{3, 4, 5}), cascade.KindIntegrity},
		{"no vectors at all", good(), cascade.KindIntegrity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := embedAgainst(t, answer(t, tc.resp))
			if out != nil {
				t.Errorf("returned %d vectors alongside the refusal", len(out))
			}
			if !cascade.HasKind(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

// TestVerifyRefusesANonFiniteComponent covers the value-level half of the
// same rule. JSON has no NaN or infinity literal, so this drives verify
// directly rather than through a response the wire cannot carry.
func TestVerifyRefusesANonFiniteComponent(t *testing.T) {
	c := newTestClient(t, newSeam(t, nil).dial, 0)
	for name, vec := range map[string][]float32{
		"nan":  {float32(math.NaN()), 1},
		"inf":  {1, float32(math.Inf(1))},
		"-inf": {float32(math.Inf(-1)), 1},
	} {
		vec := vec
		t.Run(name, func(t *testing.T) {
			out, err := c.verify(inputsOf("a"), &wireResponse{
				ProtocolVersion: ProtocolVersion, Model: testModel.ID,
				Dimensions: testModel.Dimensions, Vectors: [][]float32{vec},
			})
			if out != nil {
				t.Errorf("returned %d vectors alongside the refusal", len(out))
			}
			if !cascade.HasKind(err, cascade.KindIntegrity) {
				t.Fatalf("got %v, want KindIntegrity", err)
			}
		})
	}
}

func TestEmbedReportsAnAbsentSidecarAsUnavailable(t *testing.T) {
	out, err := newTestClient(t, func(context.Context) (io.ReadWriteCloser, error) {
		return nil, errors.New("dial unix /run/cascade-bgem3.sock: connect: no such file or directory")
	}, time.Minute).Embed(context.Background(), inputsOf("a"))
	if out != nil {
		t.Errorf("returned vectors with no sidecar present: %v", out)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("got %v, want KindUnavailable", err)
	}
}

func TestEmbedReportsAMalformedFrameAsIntegrity(t *testing.T) {
	for name, raw := range map[string][]byte{
		"truncated header":  {0x00, 0x01},
		"zero length":       {0x00, 0x00, 0x00, 0x00},
		"over the cap":      {0xFF, 0xFF, 0xFF, 0xFF, 0x7B, 0x7D},
		"truncated payload": {0x00, 0x00, 0x00, 0x40, 0x7B},
		"garbage payload":   {0x00, 0x00, 0x00, 0x04, 0x01, 0x02, 0x03, 0x04},
	} {
		frame := raw
		t.Run(name, func(t *testing.T) {
			out, err := embedAgainst(t, frame)
			if out != nil {
				t.Errorf("returned vectors from a malformed frame: %v", out)
			}
			if !cascade.HasKind(err, cascade.KindIntegrity) {
				t.Fatalf("got %v, want KindIntegrity", err)
			}
		})
	}
}

// TestEmbedAbandonsASilentSidecar covers both ways a call can be given up
// on. Each asserts the same three things: the call returns rather than
// hanging, it returns no vectors, and the connection is actually closed —
// so nothing is left half-read behind it.
func TestEmbedAbandonsASilentSidecar(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		cancels bool
		want    cascade.Kind
	}{
		{"deadline", 50 * time.Millisecond, false, cascade.KindTimeout},
		{"cancellation", time.Minute, true, cascade.KindCanceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSeam(t, func(*testing.T, wireRequest) []byte { return nil })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancels {
				defer time.AfterFunc(20*time.Millisecond, cancel).Stop()
			}
			start := time.Now()
			out, err := newTestClient(t, s.dial, tc.timeout).Embed(ctx, inputsOf("a"))
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("Embed took %s to give up; the call is not bounded", elapsed)
			}
			if out != nil {
				t.Errorf("returned vectors after giving up: %v", out)
			}
			if !cascade.HasKind(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			select {
			case <-s.closed:
			case <-time.After(5 * time.Second):
				t.Fatal("the client left its connection open after abandoning the call")
			}
		})
	}
}

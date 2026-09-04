// Purpose: the runnable godoc Example for provider.Embedder (Art.10.6) and
//   the contract tests for the Embed batch shape, driven through doubles
//   that hold the contract and doubles that break it.
// Constraints: every double here exists ONLY in this _test.go file (Art.1.1);
//   no implementation ships from this ticket.
// SPORT: pkg.provider.Embedder/ADDED (P1-E06-W2-S10-T5).

package provider_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// testEmbedModel is the embedding space every double in this file claims.
var testEmbedModel = provider.EmbedModel{ID: "test-embed-v1", Dimensions: 2}

// testEmbedder is a contract-holding Embedder: one deterministic vector per
// input, in input order, all-or-nothing, cancellation-aware. It embeds a
// text as (length, first byte) so distinct inputs get distinct vectors
// without any model.
type testEmbedder struct{}

var _ provider.Embedder = (*testEmbedder)(nil)

func (e *testEmbedder) Model() provider.EmbedModel { return testEmbedModel }

func (e *testEmbedder) Embed(ctx context.Context, inputs []provider.EmbedInput) ([]provider.EmbedOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindCanceled, err, "embed batch aborted")
	}
	out := make([]provider.EmbedOutput, 0, len(inputs))
	for _, in := range inputs {
		var first float32
		if len(in.Text) > 0 {
			first = float32(in.Text[0])
		}
		out = append(out, provider.EmbedOutput{
			Vector: []float32{float32(len(in.Text)), first},
			Model:  testEmbedModel,
		})
	}
	return out, nil
}

// ExampleEmbedder embeds a batch and checks the response against the
// embedder's own model before using it.
func ExampleEmbedder() {
	ctx := context.Background()
	var emb provider.Embedder = &testEmbedder{}

	inputs := []provider.EmbedInput{{Text: "alpha"}, {Text: "beta"}}
	outputs, err := emb.Embed(ctx, inputs)
	if err != nil {
		fmt.Println("embed error:", err)
		return
	}

	fmt.Println(emb.Model().ID, len(outputs))
	fmt.Println(emb.Model().ValidBatch(inputs, outputs))
	fmt.Println(len(outputs[0].Vector), outputs[0].Vector[0])

	// Output:
	// test-embed-v1 2
	// true
	// 2 5
}

func TestEmbed_EmptyBatchIsNotAnError(t *testing.T) {
	emb := &testEmbedder{}
	for name, inputs := range map[string][]provider.EmbedInput{
		"nil":   nil,
		"empty": {},
	} {
		out, err := emb.Embed(context.Background(), inputs)
		if err != nil {
			t.Fatalf("%s batch: unexpected error: %v", name, err)
		}
		if len(out) != 0 {
			t.Fatalf("%s batch: got %d outputs, want 0", name, len(out))
		}
	}
}

func TestEmbed_SingleElementBatchIsOrdinary(t *testing.T) {
	emb := &testEmbedder{}
	inputs := []provider.EmbedInput{{Text: "solo"}}
	out, err := emb.Embed(context.Background(), inputs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !emb.Model().ValidBatch(inputs, out) {
		t.Fatalf("one-element batch must satisfy the same contract as any other: %+v", out)
	}
}

func TestEmbed_CanceledContextReturnsTaxonomyError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out, err := (&testEmbedder{}).Embed(ctx, []provider.EmbedInput{{Text: "x"}})
	if err == nil {
		t.Fatal("want an error from a canceled context, got nil")
	}
	if out != nil {
		t.Fatalf("all-or-nothing: want a nil slice alongside the error, got %+v", out)
	}
	if !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("want a KindCanceled taxonomy error, got %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want the context cause preserved, got %v", err)
	}
}

// TestEmbed_PositionalCorrespondence drives the contract-holding double and
// a double that returns the right vectors in the wrong order. The reversing
// double passes ValidBatch, which is exactly why ordering is a stated
// contract rather than a structural check: only comparing against known
// per-input vectors catches it.
func TestEmbed_PositionalCorrespondence(t *testing.T) {
	inputs := []provider.EmbedInput{{Text: "a"}, {Text: "bb"}, {Text: "ccc"}}
	want := []float32{1, 2, 3}

	good, err := (&testEmbedder{}).Embed(context.Background(), inputs)
	if err != nil {
		t.Fatalf("conforming double: unexpected error: %v", err)
	}
	for i, w := range want {
		if good[i].Vector[0] != w {
			t.Fatalf("conforming double: outputs[%d] is not the embedding of inputs[%d]", i, i)
		}
	}

	bad, err := (&reversingEmbedder{}).Embed(context.Background(), inputs)
	if err != nil {
		t.Fatalf("reversing double: unexpected error: %v", err)
	}
	if !testEmbedModel.ValidBatch(inputs, bad) {
		t.Fatal("the reversing double is structurally well-formed; ValidBatch must not claim otherwise")
	}
	if bad[0].Vector[0] == want[0] {
		t.Fatal("the reversing double did not actually reorder, so this test proves nothing")
	}
}

// reversingEmbedder violates positional correspondence while returning the
// correct set of vectors.
type reversingEmbedder struct{}

var _ provider.Embedder = (*reversingEmbedder)(nil)

func (e *reversingEmbedder) Model() provider.EmbedModel { return testEmbedModel }

func (e *reversingEmbedder) Embed(ctx context.Context, inputs []provider.EmbedInput) ([]provider.EmbedOutput, error) {
	out, err := (&testEmbedder{}).Embed(ctx, inputs)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func TestEmbedModel_Equal(t *testing.T) {
	other := provider.EmbedModel{ID: "test-embed-v1", Dimensions: 2}
	if !testEmbedModel.Equal(other) {
		t.Fatal("identical model identities must compare equal")
	}
	if testEmbedModel.Equal(provider.EmbedModel{ID: "test-embed-v2", Dimensions: 2}) {
		t.Fatal("a different model ID is a different embedding space")
	}
	if testEmbedModel.Equal(provider.EmbedModel{ID: "test-embed-v1", Dimensions: 3}) {
		t.Fatal("the same ID at a different width is a configuration error, not a match")
	}
}

// TestEmbedModel_ValidBatch runs the structural half of the contract against
// doubles that break each clause of it.
func TestEmbedModel_ValidBatch(t *testing.T) {
	inputs := []provider.EmbedInput{{Text: "a"}, {Text: "b"}}
	ok := []provider.EmbedOutput{
		{Vector: []float32{1, 1}, Model: testEmbedModel},
		{Vector: []float32{2, 2}, Model: testEmbedModel},
	}
	otherModel := provider.EmbedModel{ID: "other-embed-v1", Dimensions: 2}

	cases := map[string]struct {
		model   provider.EmbedModel
		outputs []provider.EmbedOutput
		want    bool
	}{
		"conforming":     {testEmbedModel, ok, true},
		"dropped output": {testEmbedModel, ok[:1], false},
		"padded output":  {testEmbedModel, append(append([]provider.EmbedOutput{}, ok...), ok[0]), false},
		"foreign model": {testEmbedModel, []provider.EmbedOutput{
			ok[0], {Vector: []float32{2, 2}, Model: otherModel},
		}, false},
		"truncated vector": {testEmbedModel, []provider.EmbedOutput{
			ok[0], {Vector: []float32{2}, Model: testEmbedModel},
		}, false},
		"unset model id":  {provider.EmbedModel{Dimensions: 2}, ok, false},
		"unset dimension": {provider.EmbedModel{ID: "test-embed-v1"}, ok, false},
	}
	for name, tc := range cases {
		if got := tc.model.ValidBatch(inputs, tc.outputs); got != tc.want {
			t.Fatalf("%s: ValidBatch = %v, want %v", name, got, tc.want)
		}
	}
}

func TestEmbedModel_ValidBatch_EmptyBatchIsValid(t *testing.T) {
	if !testEmbedModel.ValidBatch(nil, nil) {
		t.Fatal("an empty batch is a valid response to an empty request")
	}
}

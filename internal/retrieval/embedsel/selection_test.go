package embedsel

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// recordingEmbedder answers every input with a fixed-width vector and
// records the batches it was handed.
type recordingEmbedder struct {
	batches [][]provider.EmbedInput
	short   bool
}

func (r *recordingEmbedder) Model() provider.EmbedModel {
	return provider.EmbedModel{ID: "m", Dimensions: 2}
}

func (r *recordingEmbedder) Embed(_ context.Context, in []provider.EmbedInput) ([]provider.EmbedOutput, error) {
	r.batches = append(r.batches, in)
	out := make([]provider.EmbedOutput, len(in))
	for i := range in {
		out[i] = provider.EmbedOutput{Vector: []float32{float32(i), 1}, Model: r.Model()}
	}
	if r.short {
		return out[:0], nil
	}
	return out, nil
}

var allTiers = []provider.SensitivityTier{
	provider.SensitivityRestricted, provider.SensitivityLocalOnly, provider.SensitivityInternal, provider.SensitivityPublic,
}

func TestSelectionAdmitsByTier(t *testing.T) {
	lane := New(&recordingEmbedder{}, SourceConductorLane, "m", 2,
		[]provider.SensitivityTier{provider.SensitivityInternal, provider.SensitivityPublic, provider.SensitivityTier(99)})
	local := New(&recordingEmbedder{}, SourceLocalSidecar, "bge-m3", 1024, allTiers)
	want := map[provider.SensitivityTier][2]bool{ // {lane, local}
		provider.SensitivityRestricted: {false, true}, provider.SensitivityLocalOnly: {false, true},
		provider.SensitivityInternal: {true, true}, provider.SensitivityPublic: {true, true},
		provider.SensitivityTier(99): {false, false},
	}
	for tier, w := range want {
		if lane.Admits(tier) != w[0] || local.Admits(tier) != w[1] {
			t.Errorf("tier %s: lane admits %t, local admits %t, want %v", tier, lane.Admits(tier), local.Admits(tier), w)
		}
	}
	if lane.Err() != nil || lane.Source() != SourceConductorLane || lane.Model() != "m" || lane.Dimensions() != 2 || !lane.Configured() {
		t.Fatalf("lane selection = %+v", lane)
	}
	personal := corpus.Corpus{ID: "p", Privacy: corpus.PrivacyPersonal}
	project := corpus.Corpus{ID: "q", Privacy: corpus.PrivacyProject}
	if lane.AdmitsCorpus(personal) || !lane.AdmitsCorpus(project) || !local.AdmitsCorpus(personal) {
		t.Fatal("AdmitsCorpus does not follow PrivacyTier and Admits")
	}
	if nilEmb := New(nil, SourceLocalSidecar, "m", 2, allTiers); nilEmb.Err() == nil || nilEmb.Admits(provider.SensitivityInternal) {
		t.Fatalf("a nil embedder was accepted: %+v", nilEmb)
	}
}

func TestSelectionNotConfiguredIsTypedUnavailable(t *testing.T) {
	cause := cascade.New(cascade.KindUnavailable, "embedding: local_socket and lane are unset")
	sel := NotConfigured(cause)
	if sel.Source() != SourceNotConfigured || sel.Configured() || sel.Err() != cause {
		t.Fatalf("NotConfigured = source %q configured %t err %v", sel.Source(), sel.Configured(), sel.Err())
	}
	for _, tier := range allTiers {
		if sel.Admits(tier) {
			t.Errorf("a not-configured selection admits %s", tier)
		}
	}
	_, err := sel.Embedder().Embed(context.Background(), []provider.EmbedInput{{Text: "x"}})
	if err != cause || err.Error() != cause.Error() {
		t.Fatalf("Embed err = %v, want the cause itself", err)
	}
	if _, err := sel.FusionEmbedder().Embed(context.Background(), []string{"x"}); err != cause {
		t.Fatalf("FusionEmbedder err = %v, want the cause itself", err)
	}
	generic := NotConfigured(nil).Err()
	if k, _ := cascade.KindOf(generic); k != cascade.KindUnavailable || generic.Error() == "" {
		t.Fatalf("NotConfigured(nil).Err() = %v, want a KindUnavailable", generic)
	}
	denied := cascade.New(cascade.KindPolicyDenied, "socket is a symlink")
	failed := Failed(SourceLocalSidecar, denied)
	if !failed.Configured() || failed.Err() != denied || failed.Admits(provider.SensitivityLocalOnly) {
		t.Fatalf("Failed = %+v", failed)
	}
}

func TestPrivacyTierMapping(t *testing.T) {
	cases := map[corpus.PrivacyClass]provider.SensitivityTier{
		corpus.PrivacyPersonal: provider.SensitivityLocalOnly,
		corpus.PrivacyGlobal:   provider.SensitivityInternal,
		corpus.PrivacyProject:  provider.SensitivityInternal,
		"":                     provider.SensitivityLocalOnly,
		"secret":               provider.SensitivityLocalOnly,
	}
	for class, want := range cases {
		if got := PrivacyTier(class); got != want {
			t.Errorf("PrivacyTier(%q) = %s, want %s", class, got, want)
		}
	}
}

func TestFusionEmbedderAdaptsVectors(t *testing.T) {
	inner := &recordingEmbedder{}
	sel := New(inner, SourceLocalSidecar, "m", 2, allTiers)
	got, err := sel.FusionEmbedder().Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 2 || got[1][0] != 1 || len(inner.batches) != 1 || inner.batches[0][1].Text != "b" {
		t.Fatalf("vectors %v, batches %v: want one batch answered in order", got, inner.batches)
	}
	inner.short = true
	_, err = sel.FusionEmbedder().Embed(context.Background(), []string{"a"})
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != cascade.KindIntegrity {
		t.Fatalf("a short reply: err %v, want KindIntegrity", err)
	}
}

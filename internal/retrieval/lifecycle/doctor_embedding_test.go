package lifecycle_test

// Purpose: the retrieval_index doctor check's embedding line: each
// selection's text, an unconfigured embedder staying StatusOK, and a
// configured-but-refused one raising a warning.

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
	"github.com/acamarata/cascade/pkg/cascade"
)

// embeddingCheck runs the doctor check over a manager on h whose
// embedding line is status and whose failure is fail.
func embeddingCheck(t *testing.T, h *harness, rebuild bool, status string, fail error) doctor.CheckResult {
	t.Helper()
	m, err := lifecycle.NewManager(lifecycle.ManagerOptions{
		CatalogPath: h.catalogPath, Store: h.store, Index: h.index, Sources: fixedSource{sources: oneSource("a.md", "# T\n\nbody")},
		TreeHash: h.hash.Get, Clock: h.clock,
		EmbeddingStatus: func() string { return status }, EmbeddingErr: func() error { return fail },
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if rebuild {
		if _, err := m.Rebuild(context.Background()); err != nil {
			t.Fatalf("Rebuild: %v", err)
		}
	}
	res, err := lifecycle.NewDoctorCheck(func(context.Context) (*lifecycle.Manager, func(), error) { return m, nil, nil }).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestRecallIndexDoctorReportsEmbedding(t *testing.T) {
	lines := []string{
		"embedding: local-sidecar bge-m3/1024 (unverified counterpart)",
		"embedding: conductor-lane emb text-embed/768 (internal and public content only)",
	}
	for _, line := range lines {
		for _, built := range []bool{false, true} {
			res := embeddingCheck(t, newHarness(t), built, line, nil)
			if res.Status != doctor.StatusOK || !strings.HasSuffix(res.Message, "; "+line) {
				t.Fatalf("built %t: %+v, want StatusOK ending in %q", built, res, line)
			}
		}
	}
	refused := cascade.New(cascade.KindPolicyDenied, "embedding: local_socket /run/e.sock is a symlink")
	res := embeddingCheck(t, newHarness(t), true, "embedding: local-sidecar refused", refused)
	if res.Status != doctor.StatusWarn || !strings.Contains(res.Detail, refused.Error()) || res.Remediation == "" {
		t.Fatalf("a refused sidecar: %+v, want a warning naming the refusal", res)
	}
}

func TestRecallIndexDoctorEmbeddingNotConfiguredIsOK(t *testing.T) {
	line := "embedding: not configured (local_socket unset: no sidecar socket path; lane unset)"
	for _, built := range []bool{false, true} {
		res := embeddingCheck(t, newHarness(t), built, line, nil)
		if res.Status != doctor.StatusOK || !strings.Contains(res.Message, line) || res.Detail != "" {
			t.Fatalf("built %t: %+v, want StatusOK carrying the not-configured line", built, res)
		}
	}
}

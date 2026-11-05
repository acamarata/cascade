package ci

// Purpose: the GitHubActionsRunner's input refusals: missing dependencies,
// a wrong executor, a repository without numeric identity, an unreadable
// workflow key and token archives that must yield no token. Each one
// dispatches nothing.

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func wantHostedKind(t *testing.T, err error, want cascade.Kind) {
	t.Helper()
	if got, ok := cascade.KindOf(err); !ok || got != want {
		t.Fatalf("kind = %v (ok=%v), want %v; err=%v", got, ok, want, err)
	}
}

func TestNewGitHubActionsRunnerRefusesMissingDeps(t *testing.T) {
	r, err := NewGitHubActionsRunner(GitHubActionsDeps{})
	if r != nil {
		t.Fatal("a runner was built without its dependencies")
	}
	wantHostedKind(t, err, cascade.KindInvalidInput)
}

func TestHostedRunRefusesWrongExecutorOrMissingCommit(t *testing.T) {
	h := newHostedRig(t)
	_, err := h.runner.Run(context.Background(), ResolvedExecutor{Kind: ExecutorLocal}, h.sj)
	wantHostedKind(t, err, cascade.KindInvalidInput)
	h.sj.Ref.CheckpointCommit = ""
	_, err = h.run(t)
	wantHostedKind(t, err, cascade.KindInvalidInput)
	if h.dispatches != 0 {
		t.Fatalf("dispatches = %d, want 0", h.dispatches)
	}
}

func TestHostedRunInputFailuresDispatchNothing(t *testing.T) {
	boom := errors.New("boom")
	for name, c := range map[string]struct {
		mutate func(*hostedRig)
		kind   cascade.Kind
	}{
		"repository error": {func(h *hostedRig) {
			h.runner.d.Repository = func(context.Context, string, string) (HostedRepository, error) {
				return HostedRepository{}, cascade.New(cascade.KindUnavailable, "down")
			}
		}, cascade.KindUnavailable},
		"no numeric identity": {func(h *hostedRig) {
			h.runner.d.Repository = func(context.Context, string, string) (HostedRepository, error) {
				return HostedRepository{ID: 0, OwnerID: 456}, nil
			}
		}, cascade.KindIntegrity},
		"workflow unreadable": {func(h *hostedRig) {
			h.runner.d.Workflow = func() (string, error) { return "", cascade.Wrap(cascade.KindInvalidInput, boom, "workflow") }
		}, cascade.KindInvalidInput},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHostedRig(t)
			c.mutate(h)
			_, err := h.run(t)
			wantHostedKind(t, err, c.kind)
			if h.dispatches != 0 {
				t.Fatalf("dispatches = %d, want 0", h.dispatches)
			}
		})
	}
}

func TestTokenFromArchiveRejectsBadArchives(t *testing.T) {
	zipOf := func(name string, body []byte) []byte {
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		f, _ := zw.Create(name)
		_, _ = f.Write(body)
		_ = zw.Close()
		return b.Bytes()
	}
	for name, archive := range map[string][]byte{
		"not a zip":      []byte("not a zip"),
		"no token file":  zipOf("other.txt", []byte("x")),
		"oversize token": zipOf(hostedTokenFile, []byte(strings.Repeat("a", maxHostedTokenBytes+1))),
	} {
		if got := tokenFromArchive(archive); got != "" {
			t.Errorf("%s: token = %q, want none", name, got)
		}
	}
	if got := tokenFromArchive(zipOf(hostedTokenFile, []byte(" tok \n"))); got != "tok" {
		t.Errorf("token = %q, want tok", got)
	}
}

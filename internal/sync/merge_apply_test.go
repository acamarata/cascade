package sync

// Purpose (this file): Engine.Merge — the one entry point, the gates it
//   runs before any merge, and the refusals that are not skips.
// SPORT: internal/sync tests (ADD) — P1-E17-W4-S38-T2.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// mergeEngine builds an engine with no store behind it: Merge touches
// neither cursors nor egress.
func mergeEngine() *Engine { return &Engine{conflicts: &ConflictJournal{}} }

// TestMergeDispatchesToTheRegisteredStrategy proves the entry point
// resolves the strategy rather than taking one from the caller — a caller
// that chose could choose an append merge over accounts metadata, which
// would keep a record the server had deleted.
func TestMergeDispatchesToTheRegisteredStrategy(t *testing.T) {
	for _, tc := range []struct {
		domain  storage.DomainID
		subkind string
		want    StrategyName
	}{
		{storage.DomainConfig, "config", StrategyServerPrimaryLWW},
		{storage.DomainConfig, "accounts", StrategyMetadataOnly},
		{storage.DomainMemory, "memory", StrategyAppendTombstone},
		{storage.DomainBlobs, "blobs", StrategyContentAddressUnion},
	} {
		t.Run(tc.subkind, func(t *testing.T) {
			got, err := mergeEngine().Merge(context.Background(), MergeRequest{
				Domain: tc.domain, Subkind: tc.subkind, PeerTier: nodes.TierController,
			})
			if err != nil {
				t.Fatalf("Merge: %v", err)
			}
			if got.Strategy != tc.want {
				t.Errorf("strategy = %q, want %q", got.Strategy, tc.want)
			}
		})
	}
}

// TestAnUnsyncedDomainIsRefusedAtTheEntryPoint covers the fail-closed rule
// where a caller would first meet it.
func TestAnUnsyncedDomainIsRefusedAtTheEntryPoint(t *testing.T) {
	got, err := mergeEngine().Merge(context.Background(), MergeRequest{
		Domain: storage.DomainAudit, Subkind: "audit", PeerTier: nodes.TierController,
	})
	if err == nil {
		t.Fatal("an unmapped domain was merged")
	}
	if got.Strategy != StrategyNone {
		t.Errorf("strategy = %q, want %q", got.Strategy, StrategyNone)
	}
	if got.Records != nil {
		t.Error("a refused merge returned records a caller could apply")
	}
}

// TestTheTierGateRunsBeforeAnyMerge is the ordering rule. Merging and then
// filtering produces the right result and the wrong journal: an operator
// would see conflicts resolved for records that were never going to be
// sent to that peer.
func TestTheTierGateRunsBeforeAnyMerge(t *testing.T) {
	e := mergeEngine()
	contested := map[string]Record{"k": rec("k", 1, 1, "n", "h")}

	got, err := e.Merge(context.Background(), MergeRequest{
		Domain: storage.DomainMemory, Subkind: "memory", PeerTier: nodes.TierWorkerTrusted,
		Server: contested, Local: map[string]Record{"k": rec("k", 2, 2, "m", "h2")},
	})
	if err == nil {
		t.Fatal("memory was merged for a worker-trusted peer that may not sync it")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPolicyDenied {
		t.Errorf("kind = %v (typed=%v), want %v", kind, ok, cascade.KindPolicyDenied)
	}
	if got.Records != nil {
		t.Error("a refused merge returned records")
	}
	if e.Conflicts().Len() != 0 {
		t.Errorf("%d conflict(s) journaled for a merge that never ran", e.Conflicts().Len())
	}
}

// TestPhaseStateWithoutAGitRunnerIsRefusedNotSkipped is the difference
// between a sync that failed and one that quietly carried nothing. The
// second leaves two machines disagreeing about what the work is.
func TestPhaseStateWithoutAGitRunnerIsRefusedNotSkipped(t *testing.T) {
	_, err := mergeEngine().Merge(context.Background(), MergeRequest{
		Domain: storage.DomainConfig, Subkind: "phase-state", PeerTier: nodes.TierController,
		Repo: "/repo", Remote: "/remote", Ref: "main",
	})
	if err == nil {
		t.Fatal("phase-state carriage reported success with no git runner")
	}
	if !strings.Contains(err.Error(), "git") {
		t.Errorf("err = %v, want it to name what is missing", err)
	}
}

// TestPhaseStateCarriesThroughTheWiredRunner proves WithGitRunner reaches
// the carriage, rather than being a setter nothing reads.
func TestPhaseStateCarriesThroughTheWiredRunner(t *testing.T) {
	git := &scriptedGit{out: map[string]string{"remote": "", "fetch": "", "rev-parse": "c1", "merge": ""}}
	got, err := mergeEngine().WithGitRunner(git).Merge(context.Background(), MergeRequest{
		Domain: storage.DomainConfig, Subkind: "phase-state", PeerTier: nodes.TierController,
		Repo: "/repo", Remote: "/remote", Ref: "main",
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !got.Carry.FastForwarded {
		t.Error("the carriage reported no fast-forward")
	}
	if len(git.ran) == 0 {
		t.Error("the wired runner was never called")
	}
}

// TestTheJournalIsReachableFromTheEngine joins the merge to the surface
// S-38.T3 reads. A journal the CLI cannot reach is one nobody sees.
func TestTheJournalIsReachableFromTheEngine(t *testing.T) {
	e := mergeEngine()
	if _, err := e.Merge(context.Background(), MergeRequest{
		Domain: storage.DomainConfig, Subkind: "config", PeerTier: nodes.TierController,
		Server: map[string]Record{"k": rec("k", 9, 0, "server", "hs")},
		Local:  map[string]Record{"k": rec("k", 8, 0, "laptop", "hl")},
	}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	entries := e.Conflicts().List()
	if len(entries) != 1 {
		t.Fatalf("%d conflict(s) reachable from the engine, want 1", len(entries))
	}
	if entries[0].Loser.Hash != "hl" {
		t.Errorf("the journal does not name the discarded write: %+v", entries[0])
	}
}

// TestAnUnadmittedBlobRefusesTheWholeMerge proves the error reaches the
// entry point rather than being swallowed into an empty result.
func TestAnUnadmittedBlobRefusesTheWholeMerge(t *testing.T) {
	_, err := mergeEngine().Merge(context.Background(), MergeRequest{
		Domain: storage.DomainBlobs, Subkind: "blobs", PeerTier: nodes.TierController,
		ServerBlobs: map[string]BlobRef{"bad": {Address: "bad"}},
	})
	if !errors.Is(err, ErrBlobNotAdmitted) {
		t.Fatalf("err = %v, want it to wrap ErrBlobNotAdmitted", err)
	}
}

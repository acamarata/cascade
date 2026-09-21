package conversation

// Purpose: TestAcceptancePrivacyModes -- S-44.T5 acceptance criterion 3.
//   Creates a public thread and a local-only thread through the REAL
//   chat.append_turn RPC path (registry.Dispatch, adapter_test.go's own
//   dispatch helper, never a to-the-side call to a handler), reads each
//   thread's stored privacy_mode back through the REAL Adapter.ThreadPrivacy
//   seam (privacy.go, S-44.T2), and routes each through the REAL
//   internal/conductor.DefaultRouter.Select against a real two-lane
//   providers registry (acceptance_harness_test.go).
// Inputs: none beyond *testing.T.
// Outputs: asserts the local-only thread's routing attempt returns
//   cascade.ErrSensitivityViolation with zero conductor.QuotaSpiller.NextLane
//   calls (FILTER 4, the stage that would first touch a candidate lane for
//   dispatch -- see conductor/router.go's own SelectExplain ordering: FILTER
//   0 runs before capability/sensitivity/health/quota), and that the public
//   thread selects a lane successfully.
//
// CONTRACT DEVIATION (recorded, not papered over): the ticket text says
// "via a real conductor RPC call". No conductor.* RPC method exists in the
// tree that invokes Router.Select at all -- internal/rpc/conductor_expand.go
// registers exactly one conductor.* method (conductor.expand, R-21.39's
// AQ-owned row) and it is unrelated to lane routing; grep -rn
// "conductor\." internal/rpc/*.go confirms no second method. Router.Select
// is invoked today only by internal/conductor/execute.go's in-process Go
// call, the same call cmd/cascade/run.go's composition root makes -- there
// is no RPC transport to be "real" through for THIS operation. Separately,
// S-44.T2's own journal (P1-E20-W5-S44-T2.md, "R-14.283, for the third
// consecutive ticket") records that NO daemon code generates an assistant
// reply yet, so no conversation-class task reaches Router.Select in
// production at all -- the join is S-89.T6's, not built yet. This test
// therefore calls the REAL Router.Select (the production enforcement
// function S-44.T2 shipped and wired as FILTER 0) directly, with a
// context built through the REAL, exported conductor.ContextWithThreadPrivacy
// seam -- carrying a ThreadPrivacy value read back from the REAL stored
// privacy_mode, not a hand-typed literal -- rather than fabricating an RPC
// transport that does not exist. This is the real enforcement function,
// not a stub of it; the gap is the missing RPC/reply-generation join, which
// is out of this ticket's files_scope (internal/conversation only) and
// already filed against S-89.T6.
// SPORT: internal.conversation/acceptance (ADDED, tests-only) (P1-E20-W5-S44-T5).

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/provider"
)

// acceptanceChatReq is a minimal real provider.ModelRequest -- the same
// shape internal/conductor/router_test.go's own chatReq() builds,
// duplicated here since chatReq is unexported to its own package.
func acceptanceChatReq() provider.ModelRequest {
	return provider.ModelRequest{
		TaskID:    "s44t5-accept",
		TaskClass: "chat",
		Inputs:    []provider.ChatMessage{{Role: "user", Content: "hello"}},
	}
}

// acceptancePrivacySeedTwoThreads seeds one local-only thread and one
// public thread through the REAL chat.append_turn RPC path -- privacy_mode
// is set on the request that CREATES the thread (privacy.go's own
// documented invariant). Split out of TestAcceptancePrivacyModes
// (Art.10.3: functions <=50 lines).
func acceptancePrivacySeedTwoThreads(t *testing.T, registry *rpc.Registry) (localThreadID, publicThreadID string) {
	t.Helper()
	localRes, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		Role: "user", PrivacyMode: "local-only",
		Segments: []appendSegmentWire{{Kind: "text", Content: "my local-only secret plan"}},
	})
	if errObj != nil {
		t.Fatalf("append_turn (local-only thread create): %+v", errObj)
	}
	publicRes, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		Role: "user", PrivacyMode: "public",
		Segments: []appendSegmentWire{{Kind: "text", Content: "public announcement"}},
	})
	if errObj != nil {
		t.Fatalf("append_turn (public thread create): %+v", errObj)
	}
	return localRes.(appendTurnResult).ThreadID, publicRes.(appendTurnResult).ThreadID
}

// acceptancePrivacyAssertStoredModes reads the modes back through the real
// Adapter.ThreadPrivacy seam, not a literal -- the routing subtests below
// carry exactly what the store holds.
func acceptancePrivacyAssertStoredModes(ctx context.Context, t *testing.T, adapter *Adapter, localThreadID, publicThreadID string) (localMode, publicMode provider.SensitivityTier) {
	t.Helper()
	localMode, err := adapter.ThreadPrivacy(ctx, localThreadID)
	if err != nil {
		t.Fatalf("ThreadPrivacy(local): %v", err)
	}
	if localMode != provider.SensitivityLocalOnly {
		t.Fatalf("stored mode for the local-only thread = %v, want SensitivityLocalOnly -- the seed itself is wrong", localMode)
	}
	publicMode, err = adapter.ThreadPrivacy(ctx, publicThreadID)
	if err != nil {
		t.Fatalf("ThreadPrivacy(public): %v", err)
	}
	if publicMode != provider.SensitivityPublic {
		t.Fatalf("stored mode for the public thread = %v, want SensitivityPublic -- the seed itself is wrong", publicMode)
	}
	return localMode, publicMode
}

// acceptancePrivacyAssertLocalRefused drives the local-only thread through
// an EXTERNAL-ONLY registry, deliberately: filterPrivacy only returns
// ErrSensitivityViolation when it refuses EVERY candidate (privacy.go),
// and every mode permits controller-local traffic -- a registry that also
// offered the local lane would route the local-only thread there instead
// of refusing it.
func acceptancePrivacyAssertLocalRefused(ctx context.Context, t *testing.T, extOnlyReg *registry.Reader, localThreadID string, localMode provider.SensitivityTier) {
	t.Helper()
	quota := &acceptanceQuotaCounter{answer: acceptanceExternalLaneName}
	router := conductor.NewRouter(extOnlyReg, quota, nil, nil)
	rctx := conductor.ContextWithThreadPrivacy(ctx, conductor.ThreadPrivacy{ThreadID: localThreadID, Mode: localMode})

	_, err := router.Select(rctx, acceptanceChatReq())
	if err == nil {
		t.Fatal("Select for a local-only thread against an external-only registry returned nil error, want ErrSensitivityViolation -- the external lane was not refused")
	}
	if !errors.Is(err, conductor.ErrSensitivityViolation) {
		t.Fatalf("Select error = %v, want errors.Is(err, conductor.ErrSensitivityViolation)", err)
	}
	if quota.calls != 0 {
		t.Fatalf("conductor.QuotaSpiller.NextLane called %d times, want 0: a refused thread reached lane dispatch", quota.calls)
	}
}

// acceptancePrivacyAssertPublicRoutes drives the public thread through a
// registry offering both lanes and asserts a successful selection.
func acceptancePrivacyAssertPublicRoutes(ctx context.Context, t *testing.T, reg *registry.Reader, publicThreadID string, publicMode provider.SensitivityTier) {
	t.Helper()
	quota := &acceptanceQuotaCounter{answer: acceptanceExternalLaneName}
	router := conductor.NewRouter(reg, quota, nil, nil)
	rctx := conductor.ContextWithThreadPrivacy(ctx, conductor.ThreadPrivacy{ThreadID: publicThreadID, Mode: publicMode})

	sel, err := router.Select(rctx, acceptanceChatReq())
	if err != nil {
		t.Fatalf("Select for a public thread against a registry offering both lanes: %v, want a successful selection -- the privacy gate refused a mode that permits every lane type", err)
	}
	if sel.LaneID == "" {
		t.Fatal("Select for a public thread returned an empty LaneID with a nil error")
	}
}

func TestAcceptancePrivacyModes(t *testing.T) {
	adapter, reg, _ := newTestAdapter(t)
	ctx := context.Background()

	localThreadID, publicThreadID := acceptancePrivacySeedTwoThreads(t, reg)
	localMode, publicMode := acceptancePrivacyAssertStoredModes(ctx, t, adapter, localThreadID, publicThreadID)

	extOnlyReg := newAcceptanceExternalOnlyRegistry(t)
	privReg := newAcceptancePrivacyRegistry(t)

	t.Run("local-only thread is refused before any lane is dispatched to", func(t *testing.T) {
		acceptancePrivacyAssertLocalRefused(ctx, t, extOnlyReg, localThreadID, localMode)
	})

	t.Run("public thread routes normally and may reach dispatch", func(t *testing.T) {
		acceptancePrivacyAssertPublicRoutes(ctx, t, privReg, publicThreadID, publicMode)
	})
}

// TestAcceptancePrivacyModes_LocalOnlyEvenAgainstControllerLocalRegistry
// is a second, narrower proof requested nowhere by the ticket text but
// needed to make the first subtest's refusal MEAN what it claims: without
// this, "no external lane call is made" could pass vacuously if
// filterPrivacy always refused everything, local lane included. This
// drives the SAME local-only thread against a registry offering ONLY the
// controller-local lane and asserts success -- the gate permits
// controller-local traffic for every mode (PrivacyAllows's own table).
func TestAcceptancePrivacyModes_LocalOnlyPermitsControllerLocalLane(t *testing.T) {
	adapter, registry, _ := newTestAdapter(t)
	ctx := context.Background()

	res, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		Role: "user", PrivacyMode: "local-only",
		Segments: []appendSegmentWire{{Kind: "text", Content: "local-only, local lane only"}},
	})
	if errObj != nil {
		t.Fatalf("append_turn: %+v", errObj)
	}
	threadID := res.(appendTurnResult).ThreadID
	mode, err := adapter.ThreadPrivacy(ctx, threadID)
	if err != nil {
		t.Fatalf("ThreadPrivacy: %v", err)
	}

	reg := newAcceptancePrivacyRegistry(t)
	quota := &acceptanceQuotaCounter{answer: acceptanceLocalLaneName}
	router := conductor.NewRouter(reg, quota, nil, nil)
	rctx := conductor.ContextWithThreadPrivacy(ctx, conductor.ThreadPrivacy{ThreadID: threadID, Mode: mode})

	sel, err := router.Select(rctx, acceptanceChatReq(), acceptanceExternalLaneName)
	if err != nil {
		t.Fatalf("Select for a local-only thread, external lane excluded, controller-local lane offered: %v, want success", err)
	}
	if sel.LaneID != acceptanceLocalLaneName {
		t.Fatalf("LaneID = %q, want %q", sel.LaneID, acceptanceLocalLaneName)
	}
	if quota.calls != 1 {
		t.Fatalf("NextLane called %d times, want exactly 1 for the one surviving candidate", quota.calls)
	}
}

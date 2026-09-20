package conversation

// Purpose (this file): the thread privacy_mode marker table and the
//   append_turn parameter that writes it — absence meaning restricted, a
//   set mode surviving a reopen, idempotent re-apply, and the refusal a
//   privacy_mode on a CONTINUATION gets.
//
// SPORT: internal/conversation:privacy (TEST) — P1-E20-W5-S44-T2.

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestPrivacyAbsenceMeansRestricted is the fail-closed default as a fact
// about the STORE, not about a branch someone could invert: a thread that
// was never marked reads back restricted because no row exists for it.
func TestPrivacyAbsenceMeansRestricted(t *testing.T) {
	store := newTestStore(t)
	got, err := store.ThreadPrivacy(context.Background(), "never-marked")
	if err != nil {
		t.Fatalf("ThreadPrivacy on an unmarked thread: %v", err)
	}
	if got != provider.SensitivityRestricted {
		t.Fatalf("ThreadPrivacy = %s, want restricted; an unmarked thread must not route as anything looser", got)
	}
}

// TestPrivacyRoundTrip pins every tier through the real sqlite table.
func TestPrivacyRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	for _, tier := range []provider.SensitivityTier{
		provider.SensitivityLocalOnly,
		provider.SensitivityRestricted,
		provider.SensitivityInternal,
		provider.SensitivityPublic,
	} {
		id := "th-" + tier.String()
		if err := store.SetThreadPrivacy(ctx, id, tier); err != nil {
			t.Fatalf("SetThreadPrivacy(%s): %v", tier, err)
		}
		got, err := store.ThreadPrivacy(ctx, id)
		if err != nil {
			t.Fatalf("ThreadPrivacy(%s): %v", tier, err)
		}
		if got != tier {
			t.Fatalf("ThreadPrivacy(%s) = %s, want %s", id, got, tier)
		}
	}
}

// TestPrivacyOverwriteIsLastWriteWins asserts a second Set replaces rather
// than accumulating rows, so a reopen cannot read a stale mode.
func TestPrivacyOverwriteIsLastWriteWins(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.SetThreadPrivacy(ctx, "th-x", provider.SensitivityPublic); err != nil {
		t.Fatalf("first SetThreadPrivacy: %v", err)
	}
	if err := store.SetThreadPrivacy(ctx, "th-x", provider.SensitivityLocalOnly); err != nil {
		t.Fatalf("second SetThreadPrivacy: %v", err)
	}
	got, err := store.ThreadPrivacy(ctx, "th-x")
	if err != nil {
		t.Fatalf("ThreadPrivacy: %v", err)
	}
	if got != provider.SensitivityLocalOnly {
		t.Fatalf("ThreadPrivacy = %s, want local-only", got)
	}
}

// TestPrivacyParseSensitivityTier covers the name decoder, including the
// §5.16 rule that anything unrecognised is restricted.
func TestPrivacyParseSensitivityTier(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want provider.SensitivityTier
	}{
		{"local-only", provider.SensitivityLocalOnly},
		{"restricted", provider.SensitivityRestricted},
		{"internal", provider.SensitivityInternal},
		{"public", provider.SensitivityPublic},
		{"", provider.SensitivityRestricted},
		{"LOCAL-ONLY", provider.SensitivityRestricted},
		{"nonsense", provider.SensitivityRestricted},
	} {
		if got := ParseSensitivityTier(tc.in); got != tc.want {
			t.Errorf("ParseSensitivityTier(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// TestPrivacyMigrationIdempotent re-applies the whole conversation schema
// to a database that already carries it and asserts the privacy table
// survives with its contents intact.
func TestPrivacyMigrationIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyConversationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("first ApplyConversationSchema: %v", err)
	}
	store := NewStore(db)
	if err := store.SetThreadPrivacy(ctx, "th-keep", provider.SensitivityLocalOnly); err != nil {
		t.Fatalf("SetThreadPrivacy: %v", err)
	}
	if err := ApplyConversationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("re-apply ApplyConversationSchema: %v", err)
	}
	got, err := store.ThreadPrivacy(ctx, "th-keep")
	if err != nil {
		t.Fatalf("ThreadPrivacy after re-apply: %v", err)
	}
	if got != provider.SensitivityLocalOnly {
		t.Fatalf("ThreadPrivacy after re-apply = %s, want local-only; the migration was not idempotent", got)
	}
}

// TestPrivacyAppendTurnCreatesWithMode is the end of the wire: the
// privacy_mode `cascade chat --local-only` sends must land on the thread
// the daemon MINTED, readable afterwards by anything that routes it.
func TestPrivacyAppendTurnCreatesWithMode(t *testing.T) {
	adapter, registry, _ := newTestAdapter(t)

	res, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		Role:        "user",
		Segments:    []appendSegmentWire{{Kind: "text", Content: "secret"}},
		PrivacyMode: "local-only",
	})
	if errObj != nil {
		t.Fatalf("chat.append_turn errored: %+v", errObj)
	}
	created, ok := res.(appendTurnResult)
	if !ok || created.ThreadID == "" {
		t.Fatalf("chat.append_turn result = %#v", res)
	}
	got, err := adapter.ThreadPrivacy(context.Background(), created.ThreadID)
	if err != nil {
		t.Fatalf("ThreadPrivacy: %v", err)
	}
	if got != provider.SensitivityLocalOnly {
		t.Fatalf("thread %s privacy = %s, want local-only", created.ThreadID, got)
	}
}

// TestPrivacyAppendTurnRefusesOnContinuation asserts a privacy_mode sent
// with an existing thread_id is REFUSED, not ignored — and that the turn
// is not committed either, so the refusal leaves nothing behind.
func TestPrivacyAppendTurnRefusesOnContinuation(t *testing.T) {
	adapter, registry, _ := newTestAdapter(t)
	ctx := context.Background()

	if _, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: "th-existing", Role: "user",
		Segments: []appendSegmentWire{{Kind: "text", Content: "first"}},
	}); errObj != nil {
		t.Fatalf("seeding the thread: %+v", errObj)
	}

	_, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: "th-existing", Role: "user",
		Segments:    []appendSegmentWire{{Kind: "text", Content: "second"}},
		PrivacyMode: "local-only",
	})
	if errObj == nil {
		t.Fatal("chat.append_turn accepted a privacy_mode on a continuation; the caller would believe the thread was privatised")
	}
	if !strings.Contains(errObj.Message, "th-existing") {
		t.Errorf("refusal %q does not name the thread that was not changed", errObj.Message)
	}
	// And the refusal is total: the mode did not land, and neither did the turn.
	got, err := adapter.ThreadPrivacy(ctx, "th-existing")
	if err != nil {
		t.Fatalf("ThreadPrivacy: %v", err)
	}
	if got != provider.SensitivityRestricted {
		t.Fatalf("thread privacy = %s, want restricted; a refused request still wrote a mode", got)
	}
	turns, err := adapter.store.ListTurns(ctx, "th-existing")
	if err != nil {
		t.Fatalf("ListTurns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("ListTurns = %d turns, want 1; the refused turn was committed anyway", len(turns))
	}
}

// TestPrivacySetThreadPrivacyRejectsInvalidTier pins the store's own
// fail-closed guard: a tier outside the four is not silently coerced.
func TestPrivacySetThreadPrivacyRejectsInvalidTier(t *testing.T) {
	store := newTestStore(t)
	err := store.SetThreadPrivacy(context.Background(), "th-bad", provider.SensitivityTier(200))
	if err == nil {
		t.Fatal("SetThreadPrivacy accepted a tier outside the §5.16 enum")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
}

// TestPrivacyFailClosedBranches covers the two refusals and the one
// coercion that only fire on bad input — the branches a happy-path suite
// leaves unexecuted and a fail-closed store most needs proven.
func TestPrivacyFailClosedBranches(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyConversationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyConversationSchema: %v", err)
	}
	store := NewStore(db)

	t.Run("an empty thread id is refused", func(t *testing.T) {
		if err := store.SetThreadPrivacy(ctx, "", provider.SensitivityPublic); err == nil {
			t.Fatal("SetThreadPrivacy accepted an empty thread id")
		}
	})

	t.Run("a stored value that is not a tier name reads as restricted", func(t *testing.T) {
		// Written past the typed setter on purpose: this is the
		// hand-edited or corrupted row, which is exactly the case where
		// reading wide would be worst.
		if _, err := db.ExecContext(ctx,
			`INSERT INTO `+tableThreadPrivacy+` (thread_id, privacy_mode) VALUES (?, ?)`,
			"th-corrupt", "definitely-not-a-tier"); err != nil {
			t.Fatalf("seeding a corrupt row: %v", err)
		}
		got, err := store.ThreadPrivacy(ctx, "th-corrupt")
		if err != nil {
			t.Fatalf("ThreadPrivacy: %v", err)
		}
		if got != provider.SensitivityRestricted {
			t.Fatalf("ThreadPrivacy on a corrupt row = %s, want restricted", got)
		}
	})
}

// TestPrivacyAppendTurnRefusesAMisspelledTier is the review's Q6: a tier
// NAME the caller got wrong must be refused, never coerced.
//
// Coercing it to restricted would WIDEN the request — restricted permits
// external lanes and local-only does not — so a caller who typed
// "local_only" would get a thread that routes off this machine and no
// indication anything went wrong. §5.16's fail-closed rule is about an
// ABSENT mode; this caller sent one.
func TestPrivacyAppendTurnRefusesAMisspelledTier(t *testing.T) {
	adapter, registry, _ := newTestAdapter(t)

	for _, bad := range []string{"local_only", "localonly", "LOCAL-ONLY", "private", "secret"} {
		t.Run(bad, func(t *testing.T) {
			_, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
				Role:        "user",
				Segments:    []appendSegmentWire{{Kind: "text", Content: "x"}},
				PrivacyMode: bad,
			})
			if errObj == nil {
				t.Fatalf("chat.append_turn accepted privacy_mode %q; the caller's thread would route wider than asked", bad)
			}
			if !strings.Contains(errObj.Message, bad) {
				t.Errorf("refusal %q does not quote the rejected value", errObj.Message)
			}
		})
	}

	// And the four real names are still accepted, so the guard did not
	// simply refuse everything.
	for _, good := range []string{"local-only", "restricted", "internal", "public"} {
		res, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
			Role:        "user",
			Segments:    []appendSegmentWire{{Kind: "text", Content: "x"}},
			PrivacyMode: good,
		})
		if errObj != nil {
			t.Fatalf("chat.append_turn refused the valid tier %q: %+v", good, errObj)
		}
		created := res.(appendTurnResult)
		got, err := adapter.ThreadPrivacy(context.Background(), created.ThreadID)
		if err != nil {
			t.Fatalf("ThreadPrivacy: %v", err)
		}
		if got.String() != good {
			t.Fatalf("thread created with %q reads back %s", good, got)
		}
	}
}

package conversation

// Purpose (this file): the thread privacy_mode read path as a former parse
//   site (row [1]) and its persisted form (row [2]) under the one closed
//   parser, provider.ParseSensitivityTier.
// Inputs: rows written straight into the marker table, past the typed
//   setter, in the f688c0b String() spelling.
// Outputs: assertions on ThreadPrivacy and on the bytes SetThreadPrivacy
//   writes back.
// Constraints: a real migrated SQLite schema; no mocks of the store.
// SPORT: internal.conversation.privacy/CHANGE (P1-SEC-19).

import (
	"context"
	"database/sql"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/provider"
)

// privacyFixtureStore returns a migrated store and its database.
func privacyFixtureStore(t *testing.T) (*sql.DB, Store) {
	t.Helper()
	db := openTestDB(t)
	if err := ApplyConversationSchema(context.Background(), db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyConversationSchema: %v", err)
	}
	return db, NewStore(db)
}

// seedPrivacyRow writes one raw privacy_mode row past the typed setter.
func seedPrivacyRow(t *testing.T, db *sql.DB, threadID, mode string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO `+tableThreadPrivacy+` (thread_id, privacy_mode) VALUES (?, ?)`, threadID, mode); err != nil {
		t.Fatalf("seed %s=%q: %v", threadID, mode, err)
	}
}

// TestFormerParseSitesFailClosed is this package's row of the former-parse-
// site table: the stored privacy_mode read. A row that is not a tier name
// (case and whitespace variants, a vocabulary-2 word) reads as local-only,
// the narrowest tier, never the old restricted coercion; an empty stored
// value reads as the restricted zero value.
func TestFormerParseSitesFailClosed(t *testing.T) {
	t.Run("conversation_ThreadPrivacy", func(t *testing.T) {
		db, store := privacyFixtureStore(t)
		ctx := context.Background()
		for i, bad := range []string{"secret", "RESTRICTED ", "Restricted", "normal", "local_only"} {
			id := "th-bad-" + string(rune('a'+i))
			seedPrivacyRow(t, db, id, bad)
			got, err := store.ThreadPrivacy(ctx, id)
			if err != nil {
				t.Fatalf("ThreadPrivacy(%q row): %v", bad, err)
			}
			if got != provider.SensitivityLocalOnly {
				t.Fatalf("a stored %q row reads as %v, want local-only", bad, got)
			}
		}
		seedPrivacyRow(t, db, "th-empty", "")
		if got, err := store.ThreadPrivacy(ctx, "th-empty"); err != nil || got != provider.SensitivityRestricted {
			t.Fatalf("an empty stored row = %v, %v; want restricted, nil", got, err)
		}
	})
}

// TestPersistedSensitivityFormsUnchanged is this package's persisted-form
// row: privacy_mode rows written in the f688c0b String() spelling read back
// as the same tier, and writing that tier again stores byte-identical text.
func TestPersistedSensitivityFormsUnchanged(t *testing.T) {
	db, store := privacyFixtureStore(t)
	ctx := context.Background()
	for _, tc := range []struct {
		stored string
		want   provider.SensitivityTier
	}{
		{"restricted", provider.SensitivityRestricted},
		{"local-only", provider.SensitivityLocalOnly},
		{"internal", provider.SensitivityInternal},
		{"public", provider.SensitivityPublic},
	} {
		id := "th-" + tc.stored
		seedPrivacyRow(t, db, id, tc.stored)
		got, err := store.ThreadPrivacy(ctx, id)
		if err != nil || got != tc.want {
			t.Fatalf("stored %q reads as %v, %v; want %v", tc.stored, got, err, tc.want)
		}
		if err := store.SetThreadPrivacy(ctx, id, got); err != nil {
			t.Fatalf("re-write %q: %v", tc.stored, err)
		}
		var raw string
		if err := db.QueryRowContext(ctx,
			`SELECT privacy_mode FROM `+tableThreadPrivacy+` WHERE thread_id = ?`, id).Scan(&raw); err != nil {
			t.Fatalf("read back %s: %v", id, err)
		}
		if raw != tc.stored {
			t.Fatalf("re-written row = %q, want byte-identical %q", raw, tc.stored)
		}
	}
}

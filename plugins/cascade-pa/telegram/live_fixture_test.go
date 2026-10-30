//go:build tgcapture

// Purpose (this file): AMD-20260922/3 (R-14.295) / T0 decision R6 — the
//   UNTAGGED proof that the four live-captured fixtures decode through the
//   REAL production decoders. This test is deliberately load-bearing on
//   its own FAILURE: while the owner has not yet run
//   TestTelegramLiveCapture (live_capture_test.go, //go:build integration),
//   this test FAILS with a named reason — never skips, never reports "no
//   tests to run" — so P1-E23-W5-S49-T4 cannot close on a vacuous result
//   (R6: "No live capture => this ticket does not close").
//
// Inputs: plugins/cascade-pa/telegram/testdata/getupdates_live_{text,pair,
//   callback_approve,callback_reject}.json — absent until the owner runs
//   the capture.
//
// Outputs: TestTelegramLiveCapturedFixturesDecode.
//
// Constraints: decodeEnvelope (wire.go) and parseApprovalData (approval.go)
//   are this package's own unexported production decoders — reached
//   directly (white-box, package telegram) rather than re-implemented,
//   which is the R2 files_scope amendment's whole reason this file lives
//   here instead of in plugins/cascade-pa (the untagged external package
//   cannot reach either unexported function).
//
// SPORT: plugins/cascade-pa/telegram
//   TestTelegramLiveCapturedFixturesDecode/TEST (P1-E23-W5-S49-T4).

package telegram

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// liveCaptureFixtures names the four AMD-20260922/3 fixtures, in the order
// TestTelegramLiveCapture (live_capture_test.go) produces them.
var liveCaptureFixtures = []string{
	"getupdates_live_text.json",
	"getupdates_live_pair.json",
	"getupdates_live_callback_approve.json",
	"getupdates_live_callback_reject.json",
}

// TestTelegramLiveCapturedFixturesDecode decodes each live-captured fixture
// through decodeEnvelope into []Update, then applies the per-kind
// production check (a plain-text Update, a "/pair " Update, or a
// CallbackQuery whose Data parses through parseApprovalData). A missing
// fixture FAILS this subtest by name, with the exact owner command to
// produce it — see this file's header for why a skip or a silent pass is
// not acceptable here.
func TestTelegramLiveCapturedFixturesDecode(t *testing.T) {
	for _, name := range liveCaptureFixtures {
		t.Run(name, func(t *testing.T) { decodeOneLiveFixture(t, name) })
	}
}

// decodeOneLiveFixture is TestTelegramLiveCapturedFixturesDecode's per-file
// body, split out for Art.10.3's 50-line function cap.
func decodeOneLiveFixture(t *testing.T, name string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("testdata/%s is absent: the owner has not yet run the live capture "+
			"(P1-E23-W5-S49-T4, R-14.295/AMD-20260922-3, T0 RULING R10). Source the vault, map its "+
			"bot-token value onto CASCADE_ACCEPT_TELEGRAM_BOT_TOKEN, set CASCADE_ACCEPT_CAPTURE=1, and "+
			"run: %s (read err: %v)", name, captureCommandHint, err)
	}
	var updates []Update
	if err := decodeEnvelope(raw, &updates); err != nil {
		t.Fatalf("testdata/%s: decodeEnvelope: %v", name, err)
	}
	if len(updates) == 0 {
		t.Fatalf("testdata/%s: decoded envelope carries zero updates", name)
	}
	u := updates[0]
	switch {
	case strings.Contains(name, "callback"):
		requireCallbackFixture(t, name, u)
	case strings.Contains(name, "_pair"):
		requireMessageFixture(t, name, u, "/pair ")
	default:
		requireMessageFixture(t, name, u, "")
	}
}

// requireMessageFixture asserts u decodes to a text Message, optionally
// requiring wantPrefix on its text (the "/pair " capture).
func requireMessageFixture(t *testing.T, name string, u Update, wantPrefix string) {
	t.Helper()
	if u.Message == nil {
		t.Fatalf("testdata/%s: decoded update carries no message", name)
	}
	if u.Message.From == nil {
		t.Fatalf("testdata/%s: decoded message carries no from", name)
	}
	if wantPrefix != "" && !strings.HasPrefix(u.Message.Text, wantPrefix) {
		t.Fatalf("testdata/%s: message text = %q, want a %q prefix", name, u.Message.Text, wantPrefix)
	}
}

// requireCallbackFixture asserts u decodes to a CallbackQuery whose Data
// parses through the REAL production parseApprovalData.
func requireCallbackFixture(t *testing.T, name string, u Update) {
	t.Helper()
	if u.CallbackQuery == nil {
		t.Fatalf("testdata/%s: decoded update carries no callback_query", name)
	}
	if err := validateCallbackQuery(u.CallbackQuery); err != nil {
		t.Fatalf("testdata/%s: validateCallbackQuery: %v", name, err)
	}
	claim, err := parseApprovalData(u.CallbackQuery.Data)
	if err != nil {
		t.Fatalf("testdata/%s: parseApprovalData(%q): %v", name, u.CallbackQuery.Data, err)
	}
	if claim.requestID == "" || claim.nonce == "" {
		t.Fatalf("testdata/%s: parsed claim = %+v, want both fields non-empty", name, claim)
	}
}

// captureCommandHint is the one definition of the owner command both this
// file's failure message and live_capture_test.go's own prompts spell out,
// so the two can never drift apart.
const captureCommandHint = "go test -tags integration -run TestTelegramLiveCapture ./plugins/cascade-pa/telegram/"

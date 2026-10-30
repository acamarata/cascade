//go:build integration

// Purpose (this file): bridge_acceptance_integration_test.go's own fixture
//   and real-component half — the REAL egress engine / detector-backed
//   scanner / conversation ThreadPrivacy store this ticket's T0 decision R5
//   requires ("production scanner + production privacy resolver ... not
//   permissive stand-ins"), plus the plumbing doubles (ChatService, clock,
//   registrar, in-memory BridgeState) that round out cascadepa.Stores. The
//   recording RoundTripper and the raw getUpdates peek live in this file's
//   sibling, bridge_acceptance_integration_peek_test.go (Art.10.3's
//   300-line cap split this in two, not by concern).
//
// Constraints: this package (outside plugins/cascade-pa/telegram) cannot
//   reach that package's unexported dispatch/Doer-construction internals
//   (R2's own reasoning for why the privacy/secret-scrub rework lives in
//   package telegram instead). It also cannot import internal/plugins's
//   NewCascadePABridgeChatHandler composition without additionally
//   requiring a live cascade daemon socket this ticket's own owner_prereq
//   never names — DISCLOSED GAP: integrationRealPrivacy below is the real
//   security-relevant seam (R5); ChatService stays the in-memory recorder
//   S-48.T2 already disclosed as its own follow-up.
//
// SPORT: plugins/cascade-pa bridge-acceptance-integration-fixtures/TEST
//   (P1-E23-W5-S49-T4).

package cascadepa_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/migrate"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// acceptTokenVar is the one credential env-ref (R1: a neutral name, mapped
// from the vault by the INVOKING SHELL, never the vault's own variable
// name in any tracked file). T0 RULING R10 removes the channel env-ref
// entirely: the counterpart is the owner's own bot over the owner's DIRECT
// MESSAGES, established by the real pairing handshake
// (bridge_acceptance_integration_test.go), never a pre-known chat id.
const acceptTokenVar = "CASCADE_ACCEPT_TELEGRAM_BOT_TOKEN"

// integrationCredentials reads the one env-ref, or skips (an owner
// prerequisite absent, never a defect).
func integrationCredentials(t *testing.T) (token string) {
	t.Helper()
	token = os.Getenv(acceptTokenVar)
	if token == "" {
		t.Skipf("%s not present in the environment; map the vault's bot-token value onto it "+
			"before running -tags=integration (owner prerequisite, not a defect)", acceptTokenVar)
	}
	return token
}

// redact replaces every occurrence of token in s before it can reach a log.
func redact(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "[REDACTED-BOT-TOKEN]")
}

// integrationEgressGate adapts a real *egress.Engine to telegram.EgressGate
// — R5's "production scanner" clause, egress half.

// integrationScanner adapts a real *secrets.Detector to telegram.SecretScanner
// — R5's "production scanner" clause, detector half.
type integrationScanner struct{ detector *secrets.Detector }

func (s integrationScanner) Scan(content []byte) telegram.SecretScanOutcome {
	hits := s.detector.ScanCertain(content)
	if len(hits) == 0 {
		return telegram.SecretScanOutcome{}
	}
	return telegram.SecretScanOutcome{Detected: true, Class: string(hits[0].Class)}
}

// integrationRealPrivacy adapts a real conversation.Store to
// telegram.ThreadPrivacyResolver — R5's "production privacy resolver"
// clause; see this file's header for the disclosed ChatService gap.
type integrationRealPrivacy struct{ store conversation.Store }

func (p integrationRealPrivacy) ThreadPrivacy(
	ctx context.Context, threadID string,
) (cascadepa.SensitivityTier, error) {
	tier, err := p.store.ThreadPrivacy(ctx, threadID)
	if err != nil {
		return cascadepa.TierLocalOnly, err
	}
	return tier, nil
}

// newIntegrationFirewall builds the real detector+engine pair over a
// throwaway file vault under t.TempDir() — no keychain, no network beyond
// the Bot API this test already talks to.
func newIntegrationFirewall(t *testing.T) (telegram.EgressGate, telegram.SecretScanner) {
	t.Helper()
	cfg := secrets.Config{
		Service: "cascade-pa-bridge-integration-test", Dir: t.TempDir(), ForceFileVault: true,
		Runner: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("test: no platform keychain helper in integration acceptance test")
		},
	}
	custody, err := secrets.SelectCustody(cfg)
	mustNot(t, "SelectCustody", err)
	broker, err := secrets.NewBroker(custody, nil)
	mustNot(t, "NewBroker", err)
	vault, err := secrets.NewEgressVault(broker)
	mustNot(t, "NewEgressVault", err)
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	mustNot(t, "NewDetector", err)
	engine, err := egress.NewEngine(egress.DefaultRegistry(), vault, detector)
	mustNot(t, "NewEngine", err)
	return plugins.NewBridgeEgressGate(engine), integrationScanner{detector: detector}
}

// newIntegrationConversationStore opens a real, throwaway sqlite
// conversation store -- the SAME store type production's composition root
// wraps for ThreadPrivacy resolution. Round-2 (R10) split this out of the
// prior newIntegrationPrivacy: the bridged thread id is now
// "bridge-telegram:<owner's own user id>", known only AFTER the real
// pairing handshake binds a sender, so no tier can be marked at
// construction time the way the old fixed-CHANNEL_ID version did.
func newIntegrationConversationStore(t *testing.T) conversation.Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	mustNot(t, "sql.Open", err)
	t.Cleanup(func() { _ = db.Close() })
	clock := fixedIntegrationClock{}
	mustNot(t, "ApplyConversationSchema", conversation.ApplyConversationSchema(
		context.Background(), db, migrate.SQLiteEmitter{}, clock, dbPath, filepath.Join(dir, "backups")))
	return conversation.NewStore(db)
}

// markIntegrationThreadPrivacy marks threadID at tier on store (the
// operator's own local `cascade chat --thread ... --private`-equivalent
// action) so the happy path's own thread is admitted by the REAL store,
// never a hardcoded TierPublic stand-in. ParseSensitivityTier is the REAL
// production name->tier mapping (internal/conversation/privacy.go), reused
// rather than re-derived so this fixture can never drift from the store's
// own fail-closed rules.
func markIntegrationThreadPrivacy(t *testing.T, store conversation.Store, threadID string, tier cascadepa.SensitivityTier) {
	t.Helper()
	mustNot(t, "SetThreadPrivacy", store.SetThreadPrivacy(context.Background(), threadID, tier))
}

// mustNot fails the test with a labeled message if err is non-nil.
func mustNot(t *testing.T, step string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", step, err)
	}
}

// integrationChatService is the "cascade CLI would now see this" recorder
// — see this file's header for why this half stays a double.
type integrationChatService struct {
	mu     sync.Mutex
	calls  []struct{ threadID, role, text string }
	minted []string
}

// CreateThread is chat.create_thread's seam: it mints one thread id per call
// and records the privacy mode it was asked for.
func (c *integrationChatService) CreateThread(_ context.Context, privacyMode string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.minted = append(c.minted, privacyMode)
	return "owner-window-thread-" + strconv.Itoa(len(c.minted)), nil
}

// integrationNoLegacy answers that no legacy bridge thread exists, so an
// unmapped owner DM only ever reaches a thread chat.create_thread minted.
type integrationNoLegacy struct{}

func (integrationNoLegacy) ThreadExists(context.Context, string) (bool, error) { return false, nil }

func (c *integrationChatService) AppendTurn(_ context.Context, threadID, role, text string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, struct{ threadID, role, text string }{threadID, role, text})
	return "turn-" + strconv.Itoa(len(c.calls)), nil
}

// integrationDivergence discards refusal events (the happy path emits
// none; a non-nil sink is still required — telegram.divergenceOrDiscard's
// own nil-safe default would otherwise silently substitute).
type integrationDivergence struct{}

func (integrationDivergence) EmitRefused(context.Context, telegram.RefusalEvent) {}

// fixedIntegrationClock is a PairClock/migrate.Clock pinned to one fixed
// instant — CR-C's own finding (AC1: "no injected clock, no deterministic
// IDs"): every INTERNAL timestamp this test's stores/store schema produce
// now comes from here, never time.Now(). The outbound MARKER string still
// needs run-to-run uniqueness (to find THIS run's own message in a shared
// live bot's history), which acceptMarker (the sibling peek file) derives
// from a random id, not from this clock.
type fixedIntegrationClock struct{}

func (fixedIntegrationClock) Now() time.Time {
	return time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
}

// integrationRegistrar/integrationState round out cascadepa.Stores.
type integrationRegistrar struct{}

func (integrationRegistrar) RegisterPairedDevice(context.Context, string, string, time.Time) (string, error) {
	return "node-integration", nil
}

type integrationState struct {
	mu   sync.Mutex
	rows map[string]cascadepa.SubjectState
}

func newIntegrationState() *integrationState {
	return &integrationState{rows: map[string]cascadepa.SubjectState{}}
}

func (s *integrationState) Load(_ context.Context, subject string) (cascadepa.SubjectState, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.rows[subject]
	return st, ok, nil
}

func (s *integrationState) Save(_ context.Context, st cascadepa.SubjectState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st.Version++
	s.rows[st.Subject] = st
	return nil
}

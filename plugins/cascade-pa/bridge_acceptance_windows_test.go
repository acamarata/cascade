//go:build windows

// Purpose (this file): acceptance criterion 4 (P1-E23-W5-S49-T4, task 5):
//   the epic-wide Windows tier-2 refusal (R-16.60a), asserted against the
//   REAL cross-package composition this ticket's story depends on -- a
//   genuine telegram.TelegramBridge (the S-48.T2 cascadepa.ChatBridge
//   implementation) built through telegram.NewModule's one real front
//   door, not a reimplementation of either type.
//
// Inputs: none beyond the build's GOOS. A synthetic bot token and an
//   in-memory BridgeState -- no vault, no network, no real credential.
//
// Outputs: TestPABridge_WindowsTierTwoRefusal, TestPABridge_WindowsTierTwoRefusal_Send
//   (T0 decision R8: Send is asserted at this ticket's own zero-seam
//   acceptance boundary now that fix15, 347db92, makes production Send
//   refuse on this platform -- it did not before).
//
// Constraints:
//   - BUILD-TAGGED, NOT RUNTIME-GUARDED: this file's own name ends in
//     "_windows_test.go", which the Go toolchain already treats as an
//     implicit GOOS=windows constraint on top of the explicit tag above
//     (confirmed against `go list -f '{{.XTestGoFiles}}'`: the file
//     compiles into NEITHER platform's test binary without one). That
//     matches plugins/cascade-pa/telegram/module_windows_test.go and
//     chat_windows_test.go's own precedent exactly -- both windows-only by
//     build tag, no runtime.GOOS branch inside the test body -- so this
//     file follows the same convention rather than inventing a
//     cross-platform skip that would be unreachable dead code on every
//     platform where it could even run. This ticket's own checks list
//     names the local, compile-only assert as `GOOS=windows GOARCH=amd64
//     go vet ./plugins/cascade-pa/...`; native execution happens in the
//     windows/amd64 CI lane (Art.5.3).
//   - EXTERNAL TEST PACKAGE (cascadepa_test), NOT cascadepa: this file
//     imports plugins/cascade-pa/telegram, which itself imports
//     plugins/cascade-pa (cascadepa) -- an internal cascadepa test file
//     importing telegram would cycle. cascadepa_test is a separate
//     compiled package that both cascadepa and telegram may be imported
//     into without cycling, which is exactly the tool this ticket's own
//     "zero-seam bridge" story needs: proving the TWO packages compose,
//     from outside either one.
//   - NO NETWORK, EVEN THOUGH telegram.NewModule ONLY OFFERS A REAL
//     TRANSPORT: Start() on windows/amd64 refuses BEFORE ever touching the
//     transport (module_windows.go's build-tagged short-circuit, proven
//     already by chat_windows_test.go's identical assertion at the
//     TelegramBridge layer) -- so a nil *http.Client here is never
//     dereferenced. This file passes an untyped nil for that parameter and
//     therefore never needs to import "net/http" itself.
//   - IDENTITY AND MESSAGE TEXT, NOT errors.Is: pkg/cascade's Is compares
//     Kind only (see chat_windows_test.go's own header), so this test
//     checks cascade.HasKind AND the literal string.
//
// SPORT: plugins/cascade-pa (external) TestPABridge_WindowsTierTwoRefusal/TEST
//   (P1-E23-W5-S49-T4).

package cascadepa_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// windowsAcceptClock is a PairClock pinned to one instant.
type windowsAcceptClock struct{ at time.Time }

func (c windowsAcceptClock) Now() time.Time { return c.at }

// windowsAcceptRegistrar is a DeviceRegistrar this file never expects to be
// called (Start refuses before any pairing lookup on windows/amd64).
type windowsAcceptRegistrar struct{}

func (windowsAcceptRegistrar) RegisterPairedDevice(
	context.Context, string, string, time.Time,
) (string, error) {
	return "", nil
}

// windowsAcceptState is a minimal in-memory cascadepa.BridgeState.
type windowsAcceptState struct {
	mu   sync.Mutex
	rows map[string]cascadepa.SubjectState
}

func newWindowsAcceptState() *windowsAcceptState {
	return &windowsAcceptState{rows: map[string]cascadepa.SubjectState{}}
}

func (s *windowsAcceptState) Load(
	_ context.Context, subject string,
) (cascadepa.SubjectState, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.rows[subject]
	return st, ok, nil
}

func (s *windowsAcceptState) Save(_ context.Context, st cascadepa.SubjectState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st.Version++
	s.rows[st.Subject] = st
	return nil
}

// newWindowsAcceptBridge builds a real telegram.TelegramBridge over a real
// telegram.NewModule -- the SAME front door production composition uses --
// with a synthetic token, an in-memory state store, and a nil transport
// client (see this file's header for why that is safe here).
func newWindowsAcceptBridge(t *testing.T) *telegram.TelegramBridge {
	t.Helper()
	clock := windowsAcceptClock{at: time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)}
	stores := cascadepa.NewStores(clock, windowsAcceptRegistrar{}, newWindowsAcceptState(), []byte(
		"0123456789ABCDEF0123456789ABCDEF")) // 32 synthetic key bytes; never a real credential
	const syntheticToken = "111111:SYNTHETIC-WINDOWS-ACCEPTANCE-TOKEN-NOT-REAL"
	module := telegram.NewModule(syntheticToken, nil, nil,
		cascadepa.ElevationOrRefuseAll(nil), stores, nil, nil, nil)
	return telegram.NewTelegramBridge(module, stores.Binding, telegram.SubjectFromToken(syntheticToken),
		telegram.BridgeSeams{})
}

// wantWindowsRefusalText is R-16.60a's contract string, shared by both
// tests below so Start and Send are asserted against the identical text.
const wantWindowsRefusalText = "bridge requires the daemon (Windows tier-2)"

// TestPABridge_WindowsTierTwoRefusal is acceptance criterion 4: on
// windows/amd64 the bridge surface refuses with the epic-wide typed error
// "bridge requires the daemon (Windows tier-2)" (R-16.60a) before ever
// touching the transport.
func TestPABridge_WindowsTierTwoRefusal(t *testing.T) {
	bridge := newWindowsAcceptBridge(t)

	err := bridge.Start(context.Background())
	if err == nil {
		t.Fatal("Start succeeded on windows tier-2")
	}
	assertWindowsRefusal(t, "Start", err)
}

// TestPABridge_WindowsTierTwoRefusal_Send is R8/fix15's own proof, asserted
// again at THIS ticket's zero-seam acceptance boundary (fix15's own
// package-telegram proof is chat_windows_test.go's
// TestBridgeWindowsTierTwoRefusal_Send; this is the SAME contract checked
// from outside both packages, over the real cross-package composition):
// Send is a second network-reaching entry point exactly like Start, and
// must refuse at the SAME platform gate before ever touching the
// transport — before fix15 (347db92), Send skipped platformBridgeRefusal
// entirely and dialed the Bot API on this platform. Receive itself never
// refuses (ChatBridge.Receive only returns the channel Start's poll loop
// would have fed; on windows/amd64 that loop never runs at all, so Receive
// simply never produces anything -- there is no separate refusal to
// assert there without fabricating one).
func TestPABridge_WindowsTierTwoRefusal_Send(t *testing.T) {
	bridge := newWindowsAcceptBridge(t)

	err := bridge.Send(context.Background(), "bridge-telegram:1", []byte("x"))
	if err == nil {
		t.Fatal("Send succeeded on windows tier-2")
	}
	assertWindowsRefusal(t, "Send", err)
}

// assertWindowsRefusal requires the exact contract text on a KindUnsupported
// cascade error (identity and message, never errors.Is alone).
func assertWindowsRefusal(t *testing.T, verb string, err error) {
	t.Helper()
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != cascade.KindUnsupported {
		t.Fatalf("%s returned %v, want a KindUnsupported cascade error", verb, err)
	}
	if ce.Msg != wantWindowsRefusalText || ce.Err != nil {
		t.Fatalf("%s refusal text = %q, want exactly %q", verb, ce.Msg, wantWindowsRefusalText)
	}
}

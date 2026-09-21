package telegram

// Purpose (this file): the fakes every test in this package drives the real
//   production types through. There is no net/http here and no socket
//   anywhere: Art.7.2's default-unit-lane gate forbids importing "net"/
//   "net/http" in an untagged _test.go, and the transport seams (Doer,
//   poster) exist precisely so the decision code can be exercised without
//   one.
//
// Constraints: the in-memory BridgeState below is a TEST FAKE and the only
//   in-memory implementation that exists — production has none, because an
//   in-process map cannot answer "does the pairing survive a restart?".
//
// SPORT: plugins/cascade-pa/telegram test-fakes/TEST (P1-E23-W5-S48-T1).

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// The sentinels tests use to drive the fail-closed branches.
var (
	errTestStoreDown  = errors.New("test: the bridge state store is unreachable")
	errTestGateClosed = errors.New("test: the egress gate refused")
)

// syntheticToken is the only "token" this package's tests ever handle. It is
// obviously not a real Telegram credential and is never sent anywhere.
const syntheticToken = "111111:SYNTHETIC-TEST-TOKEN-NOT-REAL"

func t0() time.Time { return time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC) }

// fixedTestClock is a PairClock pinned to one instant.
type fixedTestClock struct{ at time.Time }

func (c fixedTestClock) Now() time.Time { return c.at }

// advancingClock moves forward on demand so a TTL boundary is crossed
// deterministically instead of by waiting.
type advancingClock struct{ at time.Time }

func (c *advancingClock) Now() time.Time          { return c.at }
func (c *advancingClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// testPairKey is the derived pairing-code key every store in these tests is
// built with, from the real derivation over syntheticToken.
func testPairKey(t *testing.T) []byte {
	t.Helper()
	key, err := cascadepa.DerivePairCodeKey(syntheticToken)
	if err != nil {
		t.Fatalf("DerivePairCodeKey: %v", err)
	}
	return key
}

// fixedEntropy is GenerateCode's deterministic entropy source.
func fixedEntropy() io.Reader { return bytes.NewReader([]byte{0x11, 0x22, 0x33, 0x44, 0x55}) }

// memBridgeState is the in-memory BridgeState fake. loadErr/saveErr let a
// test drive the fail-closed branches an unreachable store produces.
type memBridgeState struct {
	mu       sync.Mutex
	rows     map[string]cascadepa.SubjectState
	loadErr  error
	saveErr  error
	saveCall int
}

func newMemState() *memBridgeState {
	return &memBridgeState{rows: map[string]cascadepa.SubjectState{}}
}

func (m *memBridgeState) Load(_ context.Context, subject string) (cascadepa.SubjectState, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadErr != nil {
		return cascadepa.SubjectState{}, false, m.loadErr
	}
	st, ok := m.rows[subject]
	return st, ok, nil
}

// Save emulates the host store's COMPARE-AND-SWAP refusal (see
// internal/bridge/cas.go): a write carrying a stale Version is refused, not
// applied. A fake that accepted every write would make the retry and
// single-use properties untestable.
func (m *memBridgeState) Save(_ context.Context, st cascadepa.SubjectState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	cur, exists := m.rows[st.Subject]
	if exists != (st.Version != 0) || (exists && cur.Version != st.Version) {
		return cascadepa.ErrStateConflict
	}
	st.Version++
	m.saveCall++
	m.rows[st.Subject] = st
	return nil
}

// okRegistrar is a DeviceRegistrar that records every paired device.
type okRegistrar struct {
	mu   sync.Mutex
	seen []string
}

func (r *okRegistrar) RegisterPairedDevice(_ context.Context, subject, senderID string, _ time.Time) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, subject+"/"+senderID)
	return "node-" + senderID, nil
}

// failingRegistrar refuses, so a test can prove Bind fails rather than
// reporting a pairing the device registry never recorded.
type failingRegistrar struct{}

func (failingRegistrar) RegisterPairedDevice(context.Context, string, string, time.Time) (string, error) {
	return "", errors.New("test registrar: refusing to register")
}

// tableElevation is an ElevationPolicy over an explicit verb set — the shape
// the host's real §5.14 table has, without importing internal/.
type tableElevation struct{ elevated map[string]bool }

func (e tableElevation) IsElevatedVerb(verb string) bool { return e.elevated[verb] }

// nodeVerbPolicy classifies the node verbs as elevated and nothing else, so a
// test can tell "the policy decided" from "a hardcoded list decided".
func nodeVerbPolicy() cascadepa.ElevationPolicy {
	return tableElevation{elevated: map[string]bool{
		"node.enroll": true, "node.upgrade": true, "node.remove": true,
	}}
}

// permissivePolicy classifies nothing as elevated. A refusal that still
// happens under it is a refusal some hardcoded list produced.
func permissivePolicy() cascadepa.ElevationPolicy { return tableElevation{elevated: map[string]bool{}} }

// recordedCall is one Doer invocation.
type recordedCall struct {
	method string
	params any
}

// fakeDoer answers a scripted queue of getUpdates envelopes and records every
// call. Outbound calls always succeed, so a refusal test asserts on WHAT was
// sent rather than on priming a reply.
type fakeDoer struct {
	mu     sync.Mutex
	calls  []recordedCall
	queue  [][]byte
	errs   []error
	outErr error
}

func (f *fakeDoer) push(raw []byte, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, raw)
	f.errs = append(f.errs, err)
}

func (f *fakeDoer) Do(_ context.Context, method string, params, out any) error {
	f.mu.Lock()
	f.calls = append(f.calls, recordedCall{method: method, params: params})
	if method != MethodGetUpdates {
		err := f.outErr
		f.mu.Unlock()
		return err
	}
	if len(f.queue) == 0 {
		f.mu.Unlock()
		return errors.New("fakeDoer: no more queued getUpdates responses")
	}
	raw, err := f.queue[0], f.errs[0]
	f.queue, f.errs = f.queue[1:], f.errs[1:]
	f.mu.Unlock()
	if err != nil {
		return err
	}
	return decodeEnvelope(raw, out)
}

// methods returns every method name the doer was asked for.
func (f *fakeDoer) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.method)
	}
	return out
}

// sentTexts returns the text of every outbound sendMessage/answerCallbackQuery
// body, in order — what a Telegram user would actually have received.
func (f *fakeDoer) sentTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		switch p := c.params.(type) {
		case sendMessageParams:
			out = append(out, p.Text)
		case answerCallbackQueryParams:
			out = append(out, p.Text)
		}
	}
	return out
}

// lastSent returns the final outbound text, failing the test when nothing was
// sent (an absent reply is a different defect from a wrong one).
func lastSent(t *testing.T, doer *fakeDoer) string {
	t.Helper()
	sent := doer.sentTexts()
	if len(sent) == 0 {
		t.Fatal("nothing was sent; expected a reply")
	}
	return sent[len(sent)-1]
}

// tierGate is the EgressGate stand-in shaped like the REAL bridge class: it
// admits only internal/public, and it rewrites the one "stored secret" it
// knows about, so a caller that stopped passing the tier or the content
// through fails visibly rather than silently.
type tierGate struct {
	mu       sync.Mutex
	tiers    []cascadepa.SensitivityTier
	contents []string
	refuse   error
}

// secretValue is the fake "stored secret" tierGate substitutes.
const secretValue = "SYNTHETIC-STORED-SECRET-0001"

// redactedValue is what the substitution pass leaves in its place.
const redactedValue = "[redacted]"

func (g *tierGate) Guard(
	_ context.Context, tier cascadepa.SensitivityTier, content []byte,
) ([]byte, error) {
	g.mu.Lock()
	g.tiers = append(g.tiers, tier)
	g.contents = append(g.contents, string(content))
	g.mu.Unlock()
	if g.refuse != nil {
		return nil, g.refuse
	}
	if tier != cascadepa.TierInternal && tier != cascadepa.TierPublic {
		return nil, errors.New("test gate: tier " + string(tier) + " is not admitted on the bridge class")
	}
	return []byte(strings.ReplaceAll(string(content), secretValue, redactedValue)), nil
}

func (g *tierGate) observedTiers() []cascadepa.SensitivityTier {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]cascadepa.SensitivityTier, len(g.tiers))
	copy(out, g.tiers)
	return out
}

// instantSleep records requested backoff durations and returns immediately.
func instantSleep() (fn func(ctx context.Context, d time.Duration) bool, durations *[]time.Duration) {
	var ds []time.Duration
	var mu sync.Mutex
	return func(ctx context.Context, d time.Duration) bool {
		mu.Lock()
		ds = append(ds, d)
		mu.Unlock()
		return ctx.Err() == nil
	}, &ds
}

// mustReadTestdata reads one fixture.
func mustReadTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata/%s: %v", name, err)
	}
	return b
}

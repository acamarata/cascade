// Purpose: the fakes and fixture loader the lane-outcome tests share: a
//
//	recording lane store that satisfies the widened RegistryLookup, a
//	scripted Transport that replays fixture responses and remembers the
//	request headers it was sent, and a harness that builds a real driver
//	through a real Resolver.
//
// Inputs: fixtures under testdata/lane_outcome and per-test scripts.
// Outputs: none.
// Constraints: no network and no net/http import; the clock is fixed.
//
// SPORT: internal/providers/dispatch lane_outcome/ADD (P1-WID-11).

package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// UpsertLane lets the shared fakeLookup satisfy the widened RegistryLookup;
// the older Resolve tests never write a lane.
func (f *fakeLookup) UpsertLane(context.Context, registry.LaneRecord) error { return nil }

// laneStore is an in-memory lane table that counts and can fail writes.
type laneStore struct {
	mu        sync.Mutex
	lanes     []registry.LaneRecord
	providers map[string]registry.ProviderRecord
	upserts   int
	upsertErr error
	listErr   error
}

var _ RegistryLookup = (*laneStore)(nil)

func (s *laneStore) ListLanes(context.Context) ([]registry.LaneRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	return append([]registry.LaneRecord(nil), s.lanes...), nil
}

func (s *laneStore) GetProvider(_ context.Context, name string) (registry.ProviderRecord, error) {
	return s.providers[name], nil
}

func (s *laneStore) UpsertLane(_ context.Context, rec registry.LaneRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upserts++
	if s.upsertErr != nil {
		return s.upsertErr
	}
	for i := range s.lanes {
		if s.lanes[i].LaneName == rec.LaneName {
			s.lanes[i] = rec
			return nil
		}
	}
	s.lanes = append(s.lanes, rec)
	return nil
}

// lane returns the stored row for the harness lane and the upsert count.
func (s *laneStore) lane(t *testing.T) (registry.LaneRecord, int) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.lanes) != 1 {
		t.Fatalf("lane store holds %d lanes, want 1", len(s.lanes))
	}
	return s.lanes[0], s.upserts
}

// scriptedResponse is one Transport answer.
type scriptedResponse struct {
	status  int
	headers map[string][]string
	body    string
	err     error
}

// scriptTransport replays responses in order (the last one repeats) and
// remembers the request headers of every call.
type scriptTransport struct {
	mu        sync.Mutex
	responses []scriptedResponse
	calls     int
	sent      []map[string]string
}

func (s *scriptTransport) Send(_ context.Context, _, _ string, headers map[string]string, _ []byte) (int, map[string][]string, io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.responses[min(s.calls, len(s.responses)-1)]
	s.calls++
	s.sent = append(s.sent, headers)
	if r.err != nil {
		return 0, nil, nil, r.err
	}
	return r.status, r.headers, io.NopCloser(bytes.NewReader([]byte(r.body))), nil
}

// fixtureResponse loads testdata/lane_outcome/<name> as a scripted response.
func fixtureResponse(t *testing.T, name string) scriptedResponse {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "lane_outcome", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var fx struct {
		Provenance string `json:"provenance"`
		Status     int    `json:"status"`
		Body       string `json:"body"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil || fx.Provenance == "" || fx.Status == 0 || fx.Body == "" {
		t.Fatalf("fixture %s is unusable (provenance %q, status %d, err %v)", name, fx.Provenance, fx.Status, err)
	}
	return scriptedResponse{status: fx.Status, body: fx.Body}
}

// harness is one driver behind one real Resolver over a laneStore.
type harness struct {
	store *laneStore
	tr    *scriptTransport
	res   *Resolver
	logs  *bytes.Buffer
	clock *runtime.FixedClock
	creds *fakeCredentials
	rec   registry.ProviderRecord
}

// laneHarnessNow is the fixed instant every harness clock starts at.
var laneHarnessNow = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

// newHarness builds a Resolver for one provider of kind driver whose lane
// starts as start, sending through the scripted responses.
func newHarness(t *testing.T, driver registry.DriverKind, start registry.LaneRecord, secret string, script ...scriptedResponse) *harness {
	t.Helper()
	rec := keyProvider("lane-provider", driver)
	start.LaneName, start.ProviderName = "lane-1", rec.Name
	if start.State == "" {
		start.State = registry.LaneStateAvailable
	}
	if start.Capacity == "" {
		start.Capacity = registry.CapacityAPICredit
	}
	h := &harness{
		store: &laneStore{lanes: []registry.LaneRecord{start}, providers: map[string]registry.ProviderRecord{rec.Name: rec}},
		tr:    &scriptTransport{responses: script},
		logs:  &bytes.Buffer{},
		clock: runtime.NewFixedClock(laneHarnessNow),
		creds: &fakeCredentials{values: map[string]string{rec.AuthRef.String(): secret}},
		rec:   rec,
	}
	res, err := NewResolver(h.store, h.creds, h.clock, h.tr)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	res.log = slog.New(slog.NewTextHandler(h.logs, nil))
	h.res = res
	return h
}

// resolve returns the decorated provider for the harness lane.
func (h *harness) resolve(t *testing.T) provider.ModelProvider {
	t.Helper()
	p, err := h.res.Resolve(context.Background(), provider.Selection{LaneID: "lane-1", Model: "test-model"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return p
}

// chat runs one Chat on a fresh Resolve and returns its error.
func (h *harness) chat(t *testing.T) error {
	t.Helper()
	_, err := h.resolve(t).Chat(context.Background(), provider.ChatRequest{
		Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}},
	})
	return err
}

// errAlwaysFails is the transport-level failure the no-response cases use.
var errAlwaysFails = errors.New("test: connection refused")

const anthropicOK = `{"id":"m1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`

// refusingCredentials is a CredentialSource that always refuses with err,
// the shape of a missing standing grant.
type refusingCredentials struct{ err error }

func (r refusingCredentials) Resolve(context.Context, string) (string, error) { return "", r.err }

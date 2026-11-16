// Purpose: holds the recorded conductor wiring transcript fixture
// (testdata/conductor_wiring_transcript.json) to the real in-process
// handshake: RegisterConductorExecuteHandler on a fresh rpc.Registry, then
// conductor.execute dispatched through it. The fixture is loaded into the
// transcript struct the conductor integration tests read, checked to
// round-trip, and compared frame by frame with a fresh run.
// Inputs: testdata/conductor_wiring_transcript.json.
// Outputs: the fixture, rewritten only under CASCADE_TESTKIT_UPDATE_GOLDEN=1
// outside CI.
// Constraints: no network, no daemon process, no credentials.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// conductorWiringFrame is one JSON-RPC frame of the wiring handshake: a
// request (method and params) or the reply to the request before it
// (result or error).
type conductorWiringFrame struct {
	Direction string           `json:"direction"`
	Method    string           `json:"method,omitempty"`
	Params    json.RawMessage  `json:"params,omitempty"`
	Result    json.RawMessage  `json:"result,omitempty"`
	Error     *rpc.ErrorObject `json:"error,omitempty"`
}

// conductorWiringTranscript is the request and reply frames of the
// conductor wiring handshake, in order.
type conductorWiringTranscript struct {
	Frames []conductorWiringFrame `json:"frames"`
}

const conductorWiringTranscriptPath = "testdata/conductor_wiring_transcript.json"

// transcriptLanes is a lane registry with no lanes.
type transcriptLanes struct{}

func (transcriptLanes) GetProvider(context.Context, string) (provider.ProviderInfo, error) {
	return provider.ProviderInfo{}, conductor.ErrNoLane
}
func (transcriptLanes) ListProviders(context.Context) ([]provider.ProviderInfo, error) {
	return nil, nil
}
func (transcriptLanes) ListLanes(context.Context) ([]provider.LaneInfo, error) { return nil, nil }
func (transcriptLanes) ListPool(context.Context, string) ([]provider.LaneInfo, error) {
	return nil, nil
}
func (transcriptLanes) GetByModel(context.Context, string) ([]provider.ProviderInfo, error) {
	return nil, nil
}

// transcriptQuota never has a spill lane.
type transcriptQuota struct{}

func (transcriptQuota) NextLane(context.Context, []conductor.LaneID) (conductor.LaneID, error) {
	return "", conductor.ErrNoLane
}

// transcriptResolver satisfies the executor's resolver requirement; the
// handshake refuses before any provider is resolved.
type transcriptResolver struct{}

func (transcriptResolver) Resolve(context.Context, provider.Selection) (provider.ModelProvider, error) {
	return nil, cascade.New(cascade.KindUnavailable, "transcript resolver: no provider")
}

// recordConductorWiring drives the real handshake once and returns its
// frames: a well-formed execute (refused by the unready security pipeline)
// and a malformed one (refused at decode).
func recordConductorWiring(ctx context.Context, t *testing.T) conductorWiringTranscript {
	t.Helper()
	clock := runtime.NewSystemClock()
	reg := rpc.NewRegistry()
	if err := daemon.RegisterConductorExecuteHandler(reg, daemon.NewManifest(nil, clock), transcriptLanes{}, transcriptQuota{},
		transcriptResolver{}, audit.New(storetest.NewMemStore(), clock, nil), clock, daemon.ConductorSecurity{},
		daemon.ConductorAccounting{}, daemon.ConductorFanOut{}, conductor.WithLaneTierCap(transcriptCap)); err != nil {
		t.Fatalf("RegisterConductorExecuteHandler: %v", err)
	}
	requests := []json.RawMessage{
		json.RawMessage(`{"task_id":"t1","task_class":"chat","sensitivity":"public","inputs":[{"role":"user","content":"hello"}]}`),
		json.RawMessage(`{"sensitivity":3}`),
	}
	var tr conductorWiringTranscript
	for _, params := range requests {
		tr.Frames = append(tr.Frames, conductorWiringFrame{Direction: "request", Method: daemon.ConductorExecuteMethod, Params: params})
		res, errObj := reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: daemon.ConductorExecuteMethod, Params: params})
		reply := conductorWiringFrame{Direction: "reply", Error: errObj}
		if errObj == nil {
			raw, err := json.Marshal(res)
			if err != nil {
				t.Fatalf("marshal result: %v", err)
			}
			reply.Result = raw
		}
		tr.Frames = append(tr.Frames, reply)
	}
	return tr
}

func marshalConductorWiring(t *testing.T, tr conductorWiringTranscript) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(tr, "", "  ")
	if err != nil {
		t.Fatalf("marshal transcript: %v", err)
	}
	return append(raw, '\n')
}

// loadConductorWiringTranscript parses the fixture strictly: an unknown
// field is an error, so a format change fails here first.
func loadConductorWiringTranscript(t *testing.T, path string) (conductorWiringTranscript, []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s (regenerate with CASCADE_TESTKIT_UPDATE_GOLDEN=1): %v", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var tr conductorWiringTranscript
	if err := dec.Decode(&tr); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return tr, raw
}

func TestConductorWiringTranscriptLoads(t *testing.T) {
	fresh := marshalConductorWiring(t, recordConductorWiring(context.Background(), t))
	if testkit.UpdateRequested() && os.Getenv("CI") == "" {
		if err := os.WriteFile(conductorWiringTranscriptPath, fresh, 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
	tr, raw := loadConductorWiringTranscript(t, conductorWiringTranscriptPath)
	if len(tr.Frames) != 4 {
		t.Fatalf("fixture has %d frames, want 4 (two request/reply pairs)", len(tr.Frames))
	}
	for i, f := range tr.Frames {
		want := "request"
		if i%2 == 1 {
			want = "reply"
		}
		if f.Direction != want {
			t.Fatalf("frame %d direction %q, want %q", i, f.Direction, want)
		}
		if want == "request" && f.Method != daemon.ConductorExecuteMethod {
			t.Fatalf("frame %d method %q, want %q", i, f.Method, daemon.ConductorExecuteMethod)
		}
		if want == "reply" && f.Error == nil && len(f.Result) == 0 {
			t.Fatalf("frame %d is a reply with neither result nor error", i)
		}
	}
	if again := marshalConductorWiring(t, tr); !bytes.Equal(again, raw) {
		t.Fatalf("fixture does not round-trip through the transcript struct\n--- got ---\n%s", again)
	}
	if !bytes.Equal(fresh, raw) {
		t.Fatalf("fixture differs from a fresh in-process handshake; regenerate with CASCADE_TESTKIT_UPDATE_GOLDEN=1\n--- got ---\n%s", fresh)
	}
}

// transcriptCap is the lane tier cap the recording registers with: every lane
// is local-only, so the cap never widens what the handshake can reach.
func transcriptCap(context.Context, conductor.LaneID, conductor.LaneType) (provider.SensitivityTier, string, error) {
	return provider.SensitivityLocalOnly, "transcript-fixture", nil
}

//go:build integration

// Purpose: Art.2's real-transport proof for the dispatch seam — a ticket
//   dispatched through pkg/provider's REAL Client over a REAL unix socket
//   and a REAL http.Client, with the envelope asserted on the far side.
//   Nothing in the path under test is a fake: the request assembly, the
//   §5.18 mapping, the sensitivity stamp and the JSON encoding are all the
//   shipping code.
//
// CONTRACT DEVIATION (recorded, not papered over). S-30.T2 asks for this
//   test "using the real D/S-07.T3 Go client SDK". That SDK is
//   internal/client, which plugins/** may not import (02-TARGET-STRUCTURE,
//   depguard); and pews.Ticket lives under plugins/pbd/internal/, which
//   Go's own internal-package rule keeps out of cmd/cascade. No single
//   package in the tree can import both, so the test cannot be written as
//   the contract words it. What it CAN do — and does — is exercise the
//   same one-method RPCCaller seam the real SDK satisfies, over a real
//   socket. Recorded in the ticket journal.
//
// Tagged `integration`: it imports net/http, which Art.7.2's unit lane
//   forbids.
// SPORT: plugins/pbd TestDispatchRealConductor (P1-E14-W3-S30-T2).

package pbd

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

// socketCaller is a real JSON-RPC 2.0 transport over a unix socket. It is
// the production RPCCaller shape (one Do method) backed by a real
// http.Client, not a stand-in for one.
type socketCaller struct {
	client *http.Client
}

func (c socketCaller) Do(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": method, "params": params, "id": 1,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/rpc", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return err
	}
	if envelope.Error != nil {
		return errFromServer(envelope.Error.Message)
	}
	if out == nil || len(envelope.Result) == 0 {
		return nil
	}
	return json.Unmarshal(envelope.Result, out)
}

// errFromServer renders a server-reported error.
type errFromServer string

func (e errFromServer) Error() string { return string(e) }

// realSocketDoor serves conductor.execute and job.cancel over a real unix
// socket and records what each received.
func realSocketDoor(t *testing.T, seen *provider.ModelRequest, cancelled *string) provider.RPCCaller {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		var call struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch call.Method {
		case "conductor.execute":
			_ = json.Unmarshal(call.Params, seen)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": 1,
				"result": provider.ModelResponse{JobID: "job-real", Output: "ran"},
			})
		case "job.cancel":
			var p map[string]string
			_ = json.Unmarshal(call.Params, &p)
			*cancelled = p["job_id"] + p["id"]
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]bool{"ok": true}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": 1,
				"error": map[string]any{"code": -32601, "message": "no such method " + call.Method},
			})
		}
	})

	// A short base dir: a unix socket path is capped near 104 bytes.
	dir, err := os.MkdirTemp("/tmp", "pbd")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	return socketCaller{client: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}}
}

// TestDispatchRealConductor asserts on what the DOOR received after a real
// round trip — the envelope the Conductor would route on — rather than on
// the dispatcher's own return value.
func TestDispatchRealConductor(t *testing.T) {
	var seen provider.ModelRequest
	var cancelled string
	caller := realSocketDoor(t, &seen, &cancelled)

	ticket := &pews.Ticket{
		ID:         "P1-E14-W3-S30-T2",
		Title:      "Wire PBD dispatch seam",
		ModelClass: pews.ModelClassArbiter,
		Tasks:      []string{"dispatch the ticket"},
		SpecRefs:   []string{"06 §5.18"},
	}
	got, err := NewConductorDispatcher(caller).Dispatch(context.Background(), ticket)
	if err != nil {
		t.Fatalf("Dispatch over a real socket: %v", err)
	}
	if got.JobID != "job-real" || got.Output != "ran" {
		t.Errorf("result = %+v", got)
	}
	if seen.TaskClass != "arbitrate" {
		t.Errorf("the door received task_class %q, want arbiter → arbitrate", seen.TaskClass)
	}
	if seen.TaskID != ticket.ID {
		t.Errorf("the door received task_id %q, want the ticket id", seen.TaskID)
	}
	if seen.Sensitivity != provider.SensitivityRestricted {
		t.Errorf("the door received sensitivity %v, want restricted", seen.Sensitivity)
	}
	if len(seen.Inputs) != 1 || seen.Inputs[0].Content == "" {
		t.Errorf("the door received inputs %+v, want the ticket's work as one turn", seen.Inputs)
	}
}

// TestDispatchRealConductor_UnmappableNeverCrossesTheSocket proves the
// refusal is client-side: a ticket the mapping cannot classify costs no
// round trip at all.
func TestDispatchRealConductor_UnmappableNeverCrossesTheSocket(t *testing.T) {
	var seen provider.ModelRequest
	var cancelled string
	caller := realSocketDoor(t, &seen, &cancelled)

	// Tasks are present deliberately: without them the empty-task refusal
	// would fire first and this test would pass without ever reaching the
	// mapping it claims to exercise.
	_, err := NewConductorDispatcher(caller).Dispatch(context.Background(), &pews.Ticket{
		ID: "P1-X", Title: "x", ModelClass: "oracle", Tasks: []string{"do it"},
	})
	if err == nil {
		t.Fatal("a ticket with an unmappable model_class crossed the socket")
	}
	if seen.TaskID != "" {
		t.Errorf("the door was reached with %+v", seen)
	}
}

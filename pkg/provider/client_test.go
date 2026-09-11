package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// recordedCall captures one Do invocation a recordingRPCCaller observed.
type recordedCall struct {
	method string
	params any
}

// recordingRPCCaller is the RECORDING RPCCaller test fake: it records every
// Do call it receives and returns a canned (result, error) pair per method,
// so a test can assert both what Client sent and what it did with what came
// back.
type recordingRPCCaller struct {
	calls   []recordedCall
	results map[string]any
	errs    map[string]error
}

func newRecordingRPCCaller() *recordingRPCCaller {
	return &recordingRPCCaller{
		results: make(map[string]any),
		errs:    make(map[string]error),
	}
}

func (r *recordingRPCCaller) Do(_ context.Context, method string, params, out any) error {
	r.calls = append(r.calls, recordedCall{method: method, params: params})
	if err := r.errs[method]; err != nil {
		return err
	}
	result, ok := r.results[method]
	if !ok || out == nil {
		return nil
	}
	// Round-trip through JSON, mirroring the real internal/client.Client's
	// own decode step, so out ends up populated exactly as a real RPC
	// response would populate it.
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func TestClientModelExecuteWrapper(t *testing.T) {
	caller := newRecordingRPCCaller()
	wantResp := provider.ModelResponse{
		JobID:     "job-42",
		Selection: provider.Selection{LaneID: "lane-a", Provider: "anthropic", Model: "m1"},
		Output:    "hello",
		Usage:     provider.Usage{InputTokens: 2, OutputTokens: 1},
	}
	caller.results["conductor.execute"] = wantResp

	client := provider.NewClient(caller)
	req := provider.ModelRequest{TaskID: "t-1", TaskClass: "chat"}

	resp, err := client.ModelExecute(context.Background(), req)
	if err != nil {
		t.Fatalf("ModelExecute() error = %v", err)
	}
	// reflect.DeepEqual, not !=: ModelResponse now carries the slice
	// fields Legs (R-21.214) and Selection.ReasonFlags (T0 unblock,
	// P1-E11-W3-S22-T2), so the struct is no longer comparable with ==.
	if !reflect.DeepEqual(resp, wantResp) {
		t.Fatalf("ModelExecute() = %+v, want %+v", resp, wantResp)
	}
	if len(caller.calls) != 1 {
		t.Fatalf("recorded %d calls, want 1", len(caller.calls))
	}
	// ModelExecute must dial the daemon's real, registered door -
	// "conductor.execute" - never the dead "model.execute" surface
	// nothing in the tree ever registered (T0-OPEN-FOLLOWUPS.md, R-16.80
	// addendum).
	if caller.calls[0].method != "conductor.execute" {
		t.Fatalf("method = %q, want conductor.execute", caller.calls[0].method)
	}
	// caller.calls[0].params holds pkg/provider's unexported wire-params
	// struct (modelExecuteWireParams) - this test lives in package
	// provider_test and cannot name that type, so it proves the wire
	// shape by round-tripping through encoding/json exactly as the real
	// internal/client.Client would put it on the wire, and asserting the
	// resulting JSON fields.
	raw, err := json.Marshal(caller.calls[0].params)
	if err != nil {
		t.Fatalf("json.Marshal(params) error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal error = %v", err)
	}
	if decoded["task_id"] != req.TaskID {
		t.Errorf(`wire "task_id" = %v, want %q`, decoded["task_id"], req.TaskID)
	}
	if decoded["task_class"] != req.TaskClass {
		t.Errorf(`wire "task_class" = %v, want %q`, decoded["task_class"], req.TaskClass)
	}
	// SensitivityRestricted is ModelRequest.Sensitivity's zero value; its
	// wire name is "restricted", never a raw numeric encoding - the real
	// door (internal/daemon/conductor_execute_params.go) decodes
	// sensitivity as a string name, not SensitivityTier's int-backed
	// encoding.
	if decoded["sensitivity"] != "restricted" {
		t.Errorf(`wire "sensitivity" = %v, want "restricted"`, decoded["sensitivity"])
	}
	if _, ok := decoded["fan_out"]; ok {
		t.Errorf(`wire "fan_out" present for a zero FanOut, want omitted`)
	}
}

// TestClientModelExecuteWrapper_NonDefaultSensitivitySerializesAsName
// asserts a ModelRequest with a non-default (non-zero) Sensitivity tier
// serializes the wire "sensitivity" field as that tier's String() name -
// the exact contract internal/daemon/conductor_execute_params.go's
// parseSensitivityTier decodes - never SensitivityTier's raw uint8
// encoding, which parseSensitivityTier would reject with
// KindInvalidInput.
func TestClientModelExecuteWrapper_NonDefaultSensitivitySerializesAsName(t *testing.T) {
	caller := newRecordingRPCCaller()
	client := provider.NewClient(caller)
	req := provider.ModelRequest{
		TaskID:      "t-2",
		TaskClass:   "code",
		Sensitivity: provider.SensitivityPublic,
	}

	if _, err := client.ModelExecute(context.Background(), req); err != nil {
		t.Fatalf("ModelExecute() error = %v", err)
	}

	// Round-trip through encoding/json, exactly as the real
	// internal/client.Client would put it on the wire, and confirm the
	// "sensitivity" field lands as the tier's string name.
	raw, err := json.Marshal(caller.calls[0].params)
	if err != nil {
		t.Fatalf("json.Marshal(params) error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal error = %v", err)
	}
	if decoded["sensitivity"] != provider.SensitivityPublic.String() {
		t.Fatalf(`wire "sensitivity" = %v, want %q`, decoded["sensitivity"], provider.SensitivityPublic.String())
	}
}

// TestClientModelExecuteWrapperMapsTaxonomy asserts a plain (non-taxonomy)
// error from the RPCCaller is mapped to KindInternal, while an error that
// already carries a taxonomy Kind passes through unchanged.
func TestClientModelExecuteWrapperMapsTaxonomy(t *testing.T) {
	t.Run("plain error maps to KindInternal", func(t *testing.T) {
		caller := newRecordingRPCCaller()
		caller.errs["conductor.execute"] = errors.New("boom")
		client := provider.NewClient(caller)

		_, err := client.ModelExecute(context.Background(), provider.ModelRequest{})
		if err == nil {
			t.Fatal("expected an error")
		}
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInternal {
			t.Fatalf("kind = %v, %v, want KindInternal, true", kind, ok)
		}
	})

	t.Run("taxonomy error passes through unchanged", func(t *testing.T) {
		caller := newRecordingRPCCaller()
		wantErr := cascade.New(cascade.KindUnavailable, "daemon unreachable")
		caller.errs["conductor.execute"] = wantErr
		client := provider.NewClient(caller)

		_, err := client.ModelExecute(context.Background(), provider.ModelRequest{})
		if !errors.Is(err, wantErr) {
			t.Fatalf("err = %v, want %v", err, wantErr)
		}
	})
}

// TestClientJobCancelIdempotent asserts a repeated JobCancel for the same
// JobID is a no-op returning the same result, and that Client itself holds
// no dedupe state - it forwards every call through Do.
func TestClientJobCancelIdempotent(t *testing.T) {
	caller := newRecordingRPCCaller()
	client := provider.NewClient(caller)
	jobID := provider.JobID("job-7")

	err1 := client.JobCancel(context.Background(), jobID)
	err2 := client.JobCancel(context.Background(), jobID)

	if err1 != nil || err2 != nil {
		t.Fatalf("JobCancel() errors = %v, %v, want nil, nil", err1, err2)
	}
	if len(caller.calls) != 2 {
		t.Fatalf("recorded %d calls, want 2 (Client forwards every call; idempotency is the server's)", len(caller.calls))
	}
	for i, call := range caller.calls {
		if call.method != "job.cancel" {
			t.Fatalf("call %d method = %q, want job.cancel", i, call.method)
		}
	}
	if caller.calls[0].params != caller.calls[1].params {
		t.Fatalf("call params differ across repeats: %+v vs %+v", caller.calls[0].params, caller.calls[1].params)
	}
}

func TestClientJobCancelMapsTaxonomy(t *testing.T) {
	caller := newRecordingRPCCaller()
	caller.errs["job.cancel"] = errors.New("boom")
	client := provider.NewClient(caller)

	err := client.JobCancel(context.Background(), provider.JobID("job-1"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInternal {
		t.Fatalf("kind = %v, %v, want KindInternal, true", kind, ok)
	}
}

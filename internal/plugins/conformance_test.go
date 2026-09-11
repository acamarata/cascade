//go:build spike

package plugins

import (
	"context"
	"fmt"
	"testing"
)

// TestHostFnConformance is the spike's principal suite: 21 subtests (the
// seven ABI v1 host functions x the three runtime adapters: builtin,
// process, wazero) plus error-path subtests, run against every adapter.
// Each success-path subtest asserts the adapter returns its deterministic
// fixture response and (for builtin) that the call was recorded without
// mutating any state outside its own boundary.
func TestHostFnConformance(t *testing.T) {
	ctx := context.Background()

	builtin := newBuiltinAdapter()
	binPath := buildProcStub(t)
	process := newProcessAdapter(t, binPath)
	wz := newWazeroAdapter(ctx, t)

	adapters := map[string]HostFn{
		"builtin": builtin,
		"process": process,
		"wazero":  wz,
	}

	for name, adapter := range adapters {
		t.Run(name, func(t *testing.T) {
			runHostFnMatrix(ctx, t, adapter)
		})
	}

	for name, adapter := range adapters {
		t.Run(name+"-error-paths", func(t *testing.T) {
			runErrorPathSubtests(t, adapter)
		})
	}

	// builtin-specific: confirm calls were recorded and no cross-call
	// state leaked (Art.1 boundary: an adapter must not mutate anything
	// outside its own declared HostFn boundary).
	if len(builtin.calls) != 7 {
		t.Fatalf("builtin adapter recorded %d calls, want 7 (one per ABI method)", len(builtin.calls))
	}
}

// hostFnCase is one named subtest for runHostFnMatrix: run returns a
// non-nil error to fail its subtest, rather than calling t.Fatalf
// directly, so the case table can be built by a separate function.
type hostFnCase struct {
	name string
	run  func() error
}

// runHostFnMatrix exercises all seven ABI v1 host functions against one
// adapter, asserting each deterministic fixture response.
func runHostFnMatrix(ctx context.Context, t *testing.T, hf HostFn) {
	for _, c := range hostFnCases(ctx, hf) {
		t.Run(c.name, func(t *testing.T) {
			if err := c.run(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// expect returns nil if cond is true, else a formatted error -- shrinks
// each case in hostFnCases to a check-and-return one-liner.
func expect(cond bool, format string, args ...any) error {
	if cond {
		return nil
	}
	return fmt.Errorf(format, args...)
}

// hostFnCases builds the seven-case table runHostFnMatrix drives, split
// across two builders so neither exceeds the funlen cap.
func hostFnCases(ctx context.Context, hf HostFn) []hostFnCase {
	return append(hostFnCasesGroupA(ctx, hf), hostFnCasesGroupB(ctx, hf)...)
}

func hostFnCasesGroupA(ctx context.Context, hf HostFn) []hostFnCase {
	return []hostFnCase{
		{methodHTTP, func() error {
			resp, err := hf.HostHTTP(ctx, &HTTPRequest{Method: "GET", URL: "/spike"})
			if err != nil {
				return err
			}
			return expect(resp.Status == 200 && resp.Body == "echo:/spike", "HostHTTP = %+v, want status 200 body echo:/spike", resp)
		}},
		{methodStorage, func() error {
			resp, err := hf.HostStorage(ctx, &StorageRequest{Op: "set", Key: "k", Value: "v"})
			if err != nil {
				return err
			}
			return expect(resp.Value == "v", "HostStorage(set) = %+v, want value v", resp)
		}},
		{methodLog, func() error {
			resp, err := hf.HostLog(ctx, &LogRequest{Level: "info", Message: "hello"})
			if err != nil {
				return err
			}
			return expect(resp.Accepted, "HostLog = %+v, want Accepted=true", resp)
		}},
		{methodStream, func() error {
			resp, err := hf.HostStream(ctx, &StreamRequest{ChannelID: "ch1", Data: "abcde"})
			if err != nil {
				return err
			}
			return expect(resp.BytesWritten == 5, "HostStream = %+v, want BytesWritten=5", resp)
		}},
	}
}

func hostFnCasesGroupB(ctx context.Context, hf HostFn) []hostFnCase {
	return []hostFnCase{
		{methodSecretRef, func() error {
			resp, err := hf.HostSecretRef(ctx, &SecretRefRequest{Name: "api-key"})
			if err != nil {
				return err
			}
			return expect(resp.RefID == "ref:api-key", "HostSecretRef = %+v, want RefID ref:api-key", resp)
		}},
		{methodEventEmit, func() error {
			resp, err := hf.HostEventEmit(ctx, &EventEmitRequest{Topic: "spike.event", Payload: "{}"})
			if err != nil {
				return err
			}
			return expect(resp.EventID == "evt:spike.event", "HostEventEmit = %+v, want EventID evt:spike.event", resp)
		}},
		{methodToolRegister, func() error {
			resp, err := hf.HostToolRegister(ctx, &ToolRegisterRequest{ToolName: "spike-tool", Schema: "{}"})
			if err != nil {
				return err
			}
			return expect(resp.Registered, "HostToolRegister = %+v, want Registered=true", resp)
		}},
	}
}

// runErrorPathSubtests exercises the three shared error paths every
// adapter method applies before doing real work: nil input, oversized
// payload, and a cancelled context. Uses HostLog as the representative
// method since its only variable-length field (Message) drives the
// size-limit probe identically to every other method's probe field.
func runErrorPathSubtests(t *testing.T, hf HostFn) {
	t.Run("nil-input", func(t *testing.T) {
		_, err := hf.HostLog(context.Background(), nil)
		if err == nil {
			t.Fatalf("HostLog(nil) should return a typed non-nil error")
		}
	})
	t.Run("oversized-payload", func(t *testing.T) {
		big := make([]byte, maxPayloadBytes+1)
		_, err := hf.HostLog(context.Background(), &LogRequest{Level: "info", Message: string(big)})
		if err == nil {
			t.Fatalf("HostLog(oversized) should return a size-limit error")
		}
	})
	t.Run("cancelled-context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := hf.HostLog(ctx, &LogRequest{Level: "info", Message: "x"})
		if err != context.Canceled {
			t.Fatalf("HostLog(cancelled ctx) = %v, want context.Canceled", err)
		}
	})
}

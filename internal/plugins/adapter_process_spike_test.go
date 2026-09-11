//go:build spike

package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// processAdapter is a subprocess HostFn implementation: it spawns the
// compiled proc_stub binary once and speaks one JSON envelope/result
// pair per line over its stdin/stdout, per host-fn call.
type processAdapter struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
	mu     sync.Mutex
}

func newProcessAdapter(t *testing.T, binPath string) *processAdapter {
	t.Helper()
	cmd := exec.Command(binPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("processAdapter: StdinPipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("processAdapter: StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("processAdapter: start %s: %v", binPath, err)
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	pa := &processAdapter{cmd: cmd, stdin: stdin, stdout: scanner}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	})
	return pa
}

// call marshals req, sends it as an envelope for method, and returns the
// raw response payload (or an error decoded from the result envelope).
func (p *processAdapter) call(method string, req any) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	env := envelope{Method: method, Payload: payload}
	line, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}
	if _, err := p.stdin.Write(append(line, '\n')); err != nil {
		return nil, fmt.Errorf("write to subprocess: %w", err)
	}
	if !p.stdout.Scan() {
		return nil, fmt.Errorf("read from subprocess: %w", p.stdout.Err())
	}
	var res result
	if err := json.Unmarshal(p.stdout.Bytes(), &res); err != nil {
		return nil, fmt.Errorf("decode result: %w", err)
	}
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	return res.Payload, nil
}

func (p *processAdapter) HostHTTP(ctx context.Context, req *HTTPRequest) (*HTTPResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.URL) })); err != nil {
		return nil, err
	}
	payload, err := p.call(methodHTTP, req)
	if err != nil {
		return nil, err
	}
	var resp HTTPResponse
	return &resp, json.Unmarshal(payload, &resp)
}

func (p *processAdapter) HostStorage(ctx context.Context, req *StorageRequest) (*StorageResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.Value) })); err != nil {
		return nil, err
	}
	payload, err := p.call(methodStorage, req)
	if err != nil {
		return nil, err
	}
	var resp StorageResponse
	return &resp, json.Unmarshal(payload, &resp)
}

func (p *processAdapter) HostLog(ctx context.Context, req *LogRequest) (*LogResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.Message) })); err != nil {
		return nil, err
	}
	payload, err := p.call(methodLog, req)
	if err != nil {
		return nil, err
	}
	var resp LogResponse
	return &resp, json.Unmarshal(payload, &resp)
}

func (p *processAdapter) HostStream(ctx context.Context, req *StreamRequest) (*StreamResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.Data) })); err != nil {
		return nil, err
	}
	payload, err := p.call(methodStream, req)
	if err != nil {
		return nil, err
	}
	var resp StreamResponse
	return &resp, json.Unmarshal(payload, &resp)
}

func (p *processAdapter) HostSecretRef(ctx context.Context, req *SecretRefRequest) (*SecretRefResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.Name) })); err != nil {
		return nil, err
	}
	payload, err := p.call(methodSecretRef, req)
	if err != nil {
		return nil, err
	}
	var resp SecretRefResponse
	return &resp, json.Unmarshal(payload, &resp)
}

func (p *processAdapter) HostEventEmit(ctx context.Context, req *EventEmitRequest) (*EventEmitResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.Payload) })); err != nil {
		return nil, err
	}
	payload, err := p.call(methodEventEmit, req)
	if err != nil {
		return nil, err
	}
	var resp EventEmitResponse
	return &resp, json.Unmarshal(payload, &resp)
}

func (p *processAdapter) HostToolRegister(ctx context.Context, req *ToolRegisterRequest) (*ToolRegisterResponse, error) {
	if err := checkCommon(ctx, req == nil, boolLen(req != nil, func() int { return len(req.Schema) })); err != nil {
		return nil, err
	}
	payload, err := p.call(methodToolRegister, req)
	if err != nil {
		return nil, err
	}
	var resp ToolRegisterResponse
	return &resp, json.Unmarshal(payload, &resp)
}

// buildProcStub compiles the proc_stub subprocess for the host platform
// into t.TempDir(), returning its path. Windows binaries get the .exe
// suffix explicitly, so the process adapter runs the real subprocess
// path on every platform rather than refusing (Art.5).
func buildProcStub(t *testing.T) string {
	t.Helper()
	name := "proc_stub"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binPath := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", binPath, "./testdata/conformance/proc_stub")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build proc_stub: %v: %s", err, out)
	}
	return binPath
}

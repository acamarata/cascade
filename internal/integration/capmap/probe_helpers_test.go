//go:build capmap

package capmap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
)

// probeTimeout bounds every child process and HTTP call a probe makes, so a
// hung daemon fails the probe instead of hanging the run.
const probeTimeout = 60 * time.Second

// browserOrigin is the header value a browser would send. The daemon's
// local request guard must refuse any request that carries it.
const browserOrigin = "http://browser.invalid"

// cli runs the built cascade binary inside one isolated home. Every probe
// builds its own, so no probe sees another's stored state.
type cli struct {
	t    *testing.T
	bin  string
	home string
	env  []string
}

// result is one finished cascade invocation.
type result struct {
	stdout, stderr string
	code           int
}

// envelope is the versioned --json shape every command emits.
type envelope struct {
	Version int             `json:"version"`
	OK      bool            `json:"ok"`
	Data    json.RawMessage `json:"data"`
	Error   *struct {
		Kind    string `json:"kind"`
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// newCLI builds the binary once (shared) and makes a fresh isolated home.
func newCLI(t *testing.T) *cli {
	t.Helper()
	bin := cascadeBinary(t)
	home, env := isolatedHome(t)
	return &cli{t: t, bin: bin, home: home, env: env}
}

// run executes the binary with args and optional stdin. A process that
// cannot start or times out fails the probe; a non-zero exit is returned.
func (c *cli) run(stdin string, args ...string) result {
	c.t.Helper()
	return c.runIn(c.home, stdin, args...)
}

// runIn is run with an explicit working directory.
func (c *cli) runIn(dir, stdin string, args ...string) result {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, args...)
	cmd.Dir, cmd.Env = dir, c.env
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return result{so.String(), se.String(), 0}
	case errors.As(err, &exit):
		return result{so.String(), se.String(), exit.ExitCode()}
	default:
		c.t.Fatalf("cascade %v: %v", args, err)
		return result{}
	}
}

// mustOK runs args and fails the probe on a non-zero exit.
func (c *cli) mustOK(stdin string, args ...string) result {
	c.t.Helper()
	r := c.run(stdin, args...)
	if r.code != 0 {
		c.t.Fatalf("cascade %v: exit %d\nstdout: %s\nstderr: %s", args, r.code, r.stdout, r.stderr)
	}
	return r
}

// decodeEnvelope decodes the stdout of a --json run.
func decodeEnvelope(t *testing.T, r result) envelope {
	t.Helper()
	var e envelope
	if err := json.Unmarshal([]byte(r.stdout), &e); err != nil {
		t.Fatalf("stdout is not one JSON envelope: %v\nstdout: %s\nstderr: %s", err, r.stdout, r.stderr)
	}
	return e
}

// statusData is the part of `status --json` the probes read.
type statusData struct {
	Health string `json:"health"`
	Daemon struct {
		PID        int    `json:"pid"`
		SocketPath string `json:"socket_path"`
	} `json:"daemon"`
	Subsystems []struct {
		Name   string `json:"name"`
		State  string `json:"state"`
		Detail string `json:"detail"`
	} `json:"subsystems"`
}

// status returns the live daemon status, or ok=false when it is not up.
func (c *cli) status() (statusData, bool) {
	c.t.Helper()
	r := c.run("", "status", "--json")
	e := decodeEnvelope(c.t, r)
	var s statusData
	if !e.OK {
		return s, false
	}
	if err := json.Unmarshal(e.Data, &s); err != nil {
		c.t.Fatalf("status data: %v", err)
	}
	return s, true
}

// startDaemon starts the daemon and waits until status answers. The daemon
// is stopped when the probe ends, before the isolated home is removed.
func (c *cli) startDaemon() statusData {
	c.t.Helper()
	c.mustOK("", "daemon", "start")
	c.t.Cleanup(func() { c.run("", "daemon", "stop") })
	deadline := time.Now().Add(probeTimeout)
	for time.Now().Before(deadline) {
		if s, ok := c.status(); ok && s.Daemon.PID > 0 {
			return s
		}
		time.Sleep(200 * time.Millisecond)
	}
	c.t.Fatal("daemon did not answer status within the probe timeout")
	return statusData{}
}

// stopDaemon stops the daemon and fails the probe if status still answers.
func (c *cli) stopDaemon() {
	c.t.Helper()
	c.mustOK("", "daemon", "stop")
	if _, up := c.status(); up {
		c.t.Fatal("status still answers after daemon stop")
	}
}

// rpcPost sends one JSON-RPC call to the daemon socket as a raw HTTP/1.1
// request, so the probe controls every header the daemon's guard reads. It
// dials with the product's own unix dialer and speaks the protocol by hand,
// which keeps this file free of the net and net/http imports the default
// test lane forbids. headers are added to the request. It returns the HTTP
// status and the decoded body.
func rpcPost(t *testing.T, sock, method string, params any, headers map[string]string) (int, []byte) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	conn, err := client.UnixDialer(ctx, sock)
	if err != nil {
		t.Fatalf("rpc %s: dial %s: %v", method, sock, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(probeTimeout))
	var req strings.Builder
	req.WriteString("POST /rpc HTTP/1.1\r\nHost: unix\r\nContent-Type: application/json\r\nConnection: close\r\n")
	for k, v := range headers {
		req.WriteString(k + ": " + v + "\r\n")
	}
	req.WriteString("Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n")
	req.Write(body)
	if _, err := io.WriteString(conn, req.String()); err != nil {
		t.Fatalf("rpc %s: write: %v", method, err)
	}
	raw, err := io.ReadAll(conn)
	if err != nil && len(raw) == 0 {
		t.Fatalf("rpc %s: read: %v", method, err)
	}
	return parseHTTPResponse(t, raw)
}

// parseHTTPResponse splits a Connection: close HTTP/1.1 response into its
// status code and body, decoding a chunked body.
func parseHTTPResponse(t *testing.T, raw []byte) (int, []byte) {
	t.Helper()
	head, rest, ok := strings.Cut(string(raw), "\r\n\r\n")
	fields := strings.Fields(head)
	if !ok || len(fields) < 2 {
		t.Fatalf("not an HTTP response: %q", raw)
	}
	code, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatalf("bad status line %q: %v", fields[0], err)
	}
	if !strings.Contains(strings.ToLower(head), "transfer-encoding: chunked") {
		return code, []byte(rest)
	}
	var out strings.Builder
	for rest != "" {
		sizeLine, tail, _ := strings.Cut(rest, "\r\n")
		n, err := strconv.ParseInt(strings.TrimSpace(sizeLine), 16, 32)
		if err != nil || n == 0 || int(n) > len(tail) {
			break
		}
		out.WriteString(tail[:n])
		rest = strings.TrimPrefix(tail[n:], "\r\n")
	}
	return code, []byte(out.String())
}

// rpcResult is a decoded JSON-RPC response body.
type rpcResult struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// mustRPC calls method as the owner and fails on an HTTP or RPC error.
func mustRPC(t *testing.T, sock, method string, params any) json.RawMessage {
	t.Helper()
	status, body := rpcPost(t, sock, method, params, nil)
	var r rpcResult
	if status != 200 || json.Unmarshal(body, &r) != nil || r.Error != nil {
		t.Fatalf("rpc %s: http %d body %s", method, status, body)
	}
	return r.Result
}

// mustRefuseBrowser sends method with a browser Origin and fails unless the
// daemon refuses with HTTP 403 and a body that never echoes the request.
func mustRefuseBrowser(t *testing.T, sock, method string, params any) {
	t.Helper()
	status, body := rpcPost(t, sock, method, params, map[string]string{"Origin": browserOrigin})
	if status != 403 {
		t.Fatalf("browser-shaped %s: http %d, want 403; body %s", method, status, body)
	}
	if strings.Contains(string(body), browserOrigin) || strings.Contains(string(body), "result") {
		t.Fatalf("refusal body echoes the request or carries a result: %s", body)
	}
}

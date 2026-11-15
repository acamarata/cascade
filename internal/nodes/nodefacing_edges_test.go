package nodes

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
)

// rawNodeFacing writes one raw HTTP/1.1 request over a DialAs pipe and
// returns the reply's status code.
func rawNodeFacing(t *testing.T, f *NodeFacing, method, path string, body []byte) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := f.DialAs("node-edge")(ctx)
	if err != nil {
		t.Fatalf("DialAs: %v", err)
	}
	defer func() { _ = conn.Close() }()
	var req bytes.Buffer
	fmt.Fprintf(&req, "%s %s HTTP/1.1\r\nHost: node-facing\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", method, path, len(body))
	req.Write(body)
	go func() { _, _ = conn.Write(req.Bytes()) }()
	line, err := bufio.NewReader(conn).ReadString('\n')
	fields := strings.Fields(line)
	if err != nil || len(fields) < 2 {
		t.Fatalf("status line %q: %v", line, err)
	}
	status, _ := strconv.Atoi(fields[1])
	return status
}

// TestNodeFacingRefusesMalformedRequests: a wrong path or verb is not found,
// an oversized body is refused before dispatch, and unparseable JSON comes
// back as a JSON-RPC error with status 200.
func TestNodeFacingRefusesMalformedRequests(t *testing.T) {
	f := NewNodeFacing(NodeFacingDeps{Clock: fixedClock{}})
	if got := rawNodeFacing(t, f, "GET", rpc.RPCPath, nil); got != 404 {
		t.Fatalf("GET %s = %d, want 404", rpc.RPCPath, got)
	}
	if got := rawNodeFacing(t, f, "POST", "/elsewhere", nil); got != 404 {
		t.Fatalf("POST /elsewhere = %d, want 404", got)
	}
	if got := rawNodeFacing(t, f, "POST", rpc.RPCPath, bytes.Repeat([]byte("x"), maxNodeFacingBodyBytes+1)); got != 413 {
		t.Fatalf("oversized body = %d, want 413", got)
	}
	if got := rawNodeFacing(t, f, "POST", rpc.RPCPath, []byte("{not json")); got != 200 {
		t.Fatalf("unparseable body = %d, want 200 with a JSON-RPC parse error", got)
	}
}

// TestNodeFacingPipeListenerIdentity: the in-process listener names itself
// and closes without error.
func TestNodeFacingPipeListenerIdentity(t *testing.T) {
	l := pipeListener{conns: NewNodeFacing(NodeFacingDeps{Clock: fixedClock{}}).conns}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if a := l.Addr(); a.Network() != "pipe" || a.String() != "node-facing" {
		t.Fatalf("addr = %s/%s, want pipe/node-facing", a.Network(), a.String())
	}
}

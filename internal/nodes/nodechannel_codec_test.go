// Purpose: the node channel's request/reply codec edges: unencodable
// params, malformed and oversized replies, error envelopes without a known
// kind, and a nil result target.

package nodes

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestEncodeNodeRequestRefusesUnencodableParams(t *testing.T) {
	if _, err := encodeNodeRequest("node.claim", make(chan int)); !errors.Is(err, cascade.New(cascade.KindInvalidInput, "")) {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
}

func TestDecodeNodeReplyEdges(t *testing.T) {
	if err := decodeNodeReply([]byte("{not json"), nil); !errors.Is(err, cascade.New(cascade.KindIntegrity, "")) {
		t.Fatalf("malformed: err = %v, want KindIntegrity", err)
	}
	if err := decodeNodeReply([]byte(`{"result":{"a":1}}`), nil); err != nil {
		t.Fatalf("nil target: err = %v, want nil", err)
	}
	var n int
	if err := decodeNodeReply([]byte(`{"result":"x"}`), &n); !errors.Is(err, cascade.New(cascade.KindIntegrity, "")) {
		t.Fatalf("wrong result type: err = %v, want KindIntegrity", err)
	}
}

func TestRPCErrorToCascadeUnknownCodes(t *testing.T) {
	err := rpcErrorToCascade(&rpc.ErrorObject{Code: -32601, Message: "method not found: node.nope"})
	if !errors.Is(err, cascade.New(cascade.KindNotFound, "")) || !strings.Contains(err.Error(), "node.nope") {
		t.Fatalf("method not found: err = %v, want KindNotFound naming the method", err)
	}
	err = rpcErrorToCascade(&rpc.ErrorObject{Code: 12345, Message: "odd failure"})
	if !errors.Is(err, cascade.New(cascade.KindInternal, "")) {
		t.Fatalf("unknown code: err = %v, want KindInternal", err)
	}
}

// cannedConn answers every request with a fixed response and records writes.
type cannedConn struct {
	io.Reader
	bytes.Buffer
}

func (c *cannedConn) Write(p []byte) (int, error) { return c.Buffer.Write(p) }
func (c *cannedConn) Read(p []byte) (int, error)  { return c.Reader.Read(p) }
func (c *cannedConn) Close() error                { return nil }

func TestExchangeNodeRequestRefusesOversizedReply(t *testing.T) {
	head := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"
	conn := &cannedConn{Reader: io.MultiReader(strings.NewReader(head),
		bytes.NewReader(bytes.Repeat([]byte("x"), maxNodeReplyBytes+1)))}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	setConnDeadline(ctx, conn)
	_, err := exchangeNodeRequest(ctx, conn, []byte(`{}`))
	if !errors.Is(err, cascade.New(cascade.KindIntegrity, "")) {
		t.Fatalf("err = %v, want KindIntegrity for an oversized reply", err)
	}
}

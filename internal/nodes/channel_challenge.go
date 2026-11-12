// Purpose: authenticate each controller.sock stream before releasing it to
// node traffic, and answer its challenge with the installation signer.
// Inputs: the pinned controller key, channel node identity and injected clock.
// Outputs: a verified stream or a closed connection and a typed refusal.
// Constraints: only the hello crosses an unauthenticated stream; the controller
// signs the context's channel identity, never a node id supplied in params.

package nodes

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
)

var _ = rpc.DeclareAccess("node.channel.hello", rpc.AccessNode)

const controllerHelloTimeout = 5 * time.Second

var (
	errControllerHelloPin = cascade.New(cascade.KindPermissionDenied,
		"nodes: controller channel requires a pinned controller key")
	errControllerHelloSignature = cascade.New(cascade.KindPermissionDenied,
		"nodes: controller channel hello does not verify against the pinned controller key")
	errControllerHelloTimeout = cascade.New(cascade.KindUnavailable,
		"nodes: controller channel hello exceeded five seconds")
	errControllerHelloCanceled = cascade.New(cascade.KindCanceled,
		"nodes: controller channel hello canceled")
	errControllerHelloIdentity = cascade.New(cascade.KindPermissionDenied,
		"nodes: controller hello requires an authenticated channel identity")
)

type controllerHelloParams struct {
	Nonce []byte `json:"nonce"`
}

type controllerHelloResult struct {
	Signature []byte `json:"signature"`
}

func controllerHelloMessage(nodeID string, nonce []byte) ([]byte, error) {
	if nodeID == "" || strings.ContainsRune(nodeID, '\x1f') || len(nonce) != 32 {
		return nil, cascade.New(cascade.KindInvalidInput, "nodes: controller hello requires a channel node id and 32-byte nonce")
	}
	return []byte("cascade.node.channel.v1\x1f" + nodeID + "\x1f" + hex.EncodeToString(nonce)), nil
}

// VerifyControllerHello verifies the controller's signature over the domain,
// channel node id and fresh 32-byte nonce. A missing pin is PermissionDenied.
func VerifyControllerHello(pinned ed25519.PublicKey, nodeID string, nonce, sig []byte) error {
	if len(pinned) != ed25519.PublicKeySize {
		return errControllerHelloPin
	}
	msg, err := controllerHelloMessage(nodeID, nonce)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pinned, msg, sig) {
		return errControllerHelloSignature
	}
	return nil
}

// controllerHelloHandler belongs exclusively to the node-facing registry.
// NodeFacing supplies ChannelIdentityFrom as identity; s is the installation
// envelope signer, not the controller's per-node NodeKeystore.
func controllerHelloHandler(s envelope.Signer, identity func(context.Context) (string, bool)) rpc.HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		if s == nil || identity == nil {
			return nil, errControllerHelloPin
		}
		nodeID, ok := identity(ctx)
		if !ok || nodeID == "" {
			return nil, errControllerHelloIdentity
		}
		var params controllerHelloParams
		// A claimed node_id is deliberately ignored. Only identity(ctx) binds
		// the signature to the authenticated channel that asked for it.
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, cascade.Wrap(cascade.KindInvalidInput, err, "nodes: decode controller hello")
		}
		msg, err := controllerHelloMessage(nodeID, params.Nonce)
		if err != nil {
			return nil, err
		}
		sig, err := s.Sign(ctx, msg)
		if err != nil {
			return nil, err
		}
		if err := VerifyControllerHello(s.PublicKey(), nodeID, params.Nonce, sig); err != nil {
			return nil, err
		}
		return controllerHelloResult{Signature: sig}, nil
	}
}

// authenticateControllerChannel takes ownership of a newly dialed stream and
// releases it only after hello verifies. expiry is an injected timer channel;
// nil uses a production timer. Clock also fences replies racing the timer.
func authenticateControllerChannel(ctx context.Context, conn Conn, pin ed25519.PublicKey, nodeID string, clock runtime.Clock, expiry <-chan time.Time) (Conn, error) {
	if clock == nil || conn == nil {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, cascade.New(cascade.KindInvalidInput, "nodes: controller hello requires a connection and clock")
	}
	if len(pin) != ed25519.PublicKeySize {
		_ = conn.Close()
		return nil, errControllerHelloPin
	}
	if ctx.Err() != nil {
		_ = conn.Close()
		return nil, errControllerHelloCanceled
	}
	deadline := clock.Now().Add(controllerHelloTimeout)
	if expiry == nil {
		timer := time.NewTimer(controllerHelloTimeout)
		defer timer.Stop()
		expiry = timer.C
	}
	r := bufio.NewReader(conn)
	done := make(chan error, 1)
	go func() { done <- exchangeControllerHello(ctx, conn, r, pin, nodeID) }()
	var err error
	select {
	case err = <-done:
		if clock.Now().After(deadline) {
			err = errControllerHelloTimeout
		} else if ctx.Err() != nil {
			err = errControllerHelloCanceled
		}
	case <-expiry:
		_ = conn.Close()
		<-done
		err = errControllerHelloTimeout
	case <-ctx.Done():
		_ = conn.Close()
		<-done
		err = errControllerHelloCanceled
	}
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return controllerHelloConn{Conn: conn, reader: r}, nil
}

type controllerHelloConn struct {
	Conn
	reader *bufio.Reader
}

func (c controllerHelloConn) Read(b []byte) (int, error) { return c.reader.Read(b) }

func exchangeControllerHello(ctx context.Context, conn Conn, r *bufio.Reader, pin ed25519.PublicKey, nodeID string) error {
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: controller hello nonce")
	}
	if _, err := controllerHelloMessage(nodeID, nonce); err != nil {
		return err
	}
	params, _ := json.Marshal(controllerHelloParams{Nonce: nonce})
	body, _ := json.Marshal(rpc.Request{JSONRPC: "2.0", Method: "node.channel.hello", Params: params, ID: json.RawMessage("1")})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix"+RPCPath, bytes.NewReader(body))
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "nodes: build controller hello")
	}
	req.Header.Set("Content-Type", "application/json")
	if err := req.Write(conn); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: send controller hello")
	}
	resp, err := http.ReadResponse(r, req)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: read controller hello")
	}
	// Do not drain an untrusted body on failure: the caller closes the stream.
	var reply struct {
		JSONRPC string                `json:"jsonrpc"`
		ID      json.RawMessage       `json:"id"`
		Result  controllerHelloResult `json:"result"`
		Error   *rpc.ErrorObject      `json:"error"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &reply) != nil {
		return errControllerHelloSignature
	}
	if resp.StatusCode != http.StatusOK || resp.Close || reply.JSONRPC != "2.0" || string(reply.ID) != "1" || reply.Error != nil {
		return errControllerHelloSignature
	}
	if err := resp.Body.Close(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: close controller hello response")
	}
	return VerifyControllerHello(pin, nodeID, nonce, reply.Result.Signature)
}

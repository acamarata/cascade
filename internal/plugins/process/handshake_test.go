package process

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// handshakeResponder answers cascade.hello with a fixed protocol version
// and captures any cascade.version_mismatch notification it receives.
type handshakeResponder struct {
	version     string
	sawMismatch chan VersionMismatchMsg
	toClient    io.Writer
	fromClient  io.Reader
}

func newHandshakeResponder(t *testing.T, version string, toClient io.Writer, fromClient io.Reader) *handshakeResponder {
	t.Helper()
	h := &handshakeResponder{version: version, sawMismatch: make(chan VersionMismatchMsg, 1), toClient: toClient, fromClient: fromClient}
	go h.run()
	return h
}

func (h *handshakeResponder) run() {
	scanner := bufio.NewScanner(h.fromClient)
	for scanner.Scan() {
		line := scanner.Bytes()
		var probe struct {
			ID     *uint64 `json:"id"`
			Method string  `json:"method"`
		}
		if json.Unmarshal(line, &probe) != nil {
			continue
		}
		if probe.Method == "cascade.version_mismatch" {
			var n Notification
			_ = json.Unmarshal(line, &n)
			var msg VersionMismatchMsg
			_ = json.Unmarshal(n.Params, &msg)
			h.sawMismatch <- msg
			continue
		}
		if probe.ID == nil {
			continue
		}
		ack := HelloAckMsg{ProtocolVersion: h.version, ManifestHash: "deadbeef"}
		result, _ := json.Marshal(ack)
		resp := Response{JSONRPC: "2.0", ID: *probe.ID, Result: result}
		data, _ := json.Marshal(resp)
		data = append(data, '\n')
		if _, err := h.toClient.Write(data); err != nil {
			return
		}
	}
}

func newHandshakeTransport(t *testing.T, version string) *Transport {
	t.Helper()
	clientReads, pluginWrites := io.Pipe()
	pluginReads, clientWrites := io.Pipe()
	newHandshakeResponder(t, version, pluginWrites, pluginReads)
	return NewTransport(clientWrites, clientReads, 2*time.Second)
}

func TestPerformHandshakeSuccess(t *testing.T) {
	tr := newHandshakeTransport(t, "1.0.0")
	ack, err := performHandshake(context.Background(), tr, "demo", "1.0.0")
	if err != nil {
		t.Fatalf("performHandshake: %v", err)
	}
	if ack.ProtocolVersion != "1.0.0" || ack.ManifestHash != "deadbeef" {
		t.Fatalf("unexpected ack: %+v", ack)
	}
}

func TestPerformHandshakeVersionMismatch(t *testing.T) {
	clientReads, pluginWrites := io.Pipe()
	pluginReads, clientWrites := io.Pipe()
	responder := newHandshakeResponder(t, "0.9.0", pluginWrites, pluginReads)
	tr := NewTransport(clientWrites, clientReads, 2*time.Second)

	_, err := performHandshake(context.Background(), tr, "demo", "1.0.0")
	if err == nil {
		t.Fatal("expected a version-mismatch error")
	}
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("errors.Is(err, ErrVersionMismatch) = false, got %v", err)
	}
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("expected KindUnsupported, got %v", err)
	}
	select {
	case msg := <-responder.sawMismatch:
		if msg.PluginVersion != "0.9.0" || msg.MinProtocolVersion != "1.0.0" {
			t.Fatalf("unexpected mismatch notification: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected a cascade.version_mismatch notification, none arrived")
	}
}

func TestVersionLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.0.0", "1.0.0", false},
		{"0.9.0", "1.0.0", true},
		{"1.0.0", "0.9.0", false},
		{"1.2.3", "1.2.4", true},
		{"2.0.0", "1.9.9", false},
		{"not-a-version", "1.0.0", true},
		{"1.0.0", "not-a-version", false},
	}
	for _, tc := range cases {
		if got := versionLess(tc.a, tc.b); got != tc.want {
			t.Errorf("versionLess(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestParseSemverRejectsMalformed(t *testing.T) {
	for _, s := range []string{"", "1", "1.2", "1.2.3.4", "a.b.c", "1..3"} {
		if _, ok := parseSemver(s); ok {
			t.Errorf("parseSemver(%q) unexpectedly succeeded", s)
		}
	}
	if v, ok := parseSemver("1.2.3"); !ok || v != [3]int{1, 2, 3} {
		t.Errorf("parseSemver(1.2.3) = %v, %v", v, ok)
	}
}

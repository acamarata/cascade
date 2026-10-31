package process

import (
	"bytes"
	"encoding/json"
	"github.com/acamarata/cascade/pkg/cascade"
	"io"
	"strings"
	"testing"
)

func TestTransportDeliversPluginRequestWithID(t *testing.T) {
	tr := NewTransport(io.Discard, strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":\"plugin-1\",\"method\":\"host_log\",\"params\":{}}\n"), 0)
	r := <-tr.HostRequests()
	if string(r.ID) != `"plugin-1"` || r.Method != "host_log" {
		t.Fatalf("request = %+v", r)
	}
	<-tr.Done()
}

func TestTransportReplyWritesResponseFrame(t *testing.T) {
	for _, err := range []error{nil, cascade.ErrCapabilityDenied} {
		var out bytes.Buffer
		tr := &Transport{w: &out}
		if e := tr.Reply(json.RawMessage(`"plugin-1"`), "ok", err); e != nil {
			t.Fatal(e)
		}
		var frame struct {
			ID     json.RawMessage
			Result string
			Error  *RPCError
		}
		if e := json.Unmarshal(out.Bytes(), &frame); e != nil {
			t.Fatal(e)
		}
		if string(frame.ID) != `"plugin-1"` {
			t.Fatal("uncorrelated reply")
		}
		if err == nil && (frame.Result != "ok" || frame.Error != nil) {
			t.Fatal("missing result")
		}
		if err != nil && (frame.Error == nil || frame.Error.Code != cascade.RPCCodeCapabilityDenied) {
			t.Fatal("missing taxonomy error")
		}
	}
}

// FuzzProcessTransport fuzzes the newline-delimited JSON-RPC frame
// decoder (Transport.routeFrame) with corpus seeded from the handshake
// fixtures. It asserts only that no malformed input panics: routeFrame
// silently drops anything it cannot decode as either a Response or a
// Notification, by design (§5 rule 7).
func FuzzProcessTransport(f *testing.F) {
	f.Add([]byte(`{"jsonrpc":"2.0","id":1,"method":"cascade.hello","params":{"min_protocol_version":"1.0.0"}}`))
	f.Add([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocol_version":"1.0.0","manifest_hash":"deadbeef"}}`))
	f.Add([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`))
	f.Add([]byte(`not json at all`))
	f.Add([]byte(``))
	f.Add([]byte(`{`))
	f.Add([]byte(`{"id":null,"method":""}`))

	f.Fuzz(func(_ *testing.T, data []byte) {
		tr := &Transport{pending: make(map[uint64]*pendingCall), notifications: make(chan Notification, 1), hostRequests: make(chan HostRequest, 1)}
		tr.routeFrame(data)
		select {
		case <-tr.notifications:
		default:
		}
	})
}

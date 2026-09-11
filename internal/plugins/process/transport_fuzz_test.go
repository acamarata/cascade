package process

import "testing"

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
		tr := &Transport{pending: make(map[uint64]*pendingCall), notifications: make(chan Notification, 1)}
		tr.routeFrame(data)
		select {
		case <-tr.notifications:
		default:
		}
	})
}

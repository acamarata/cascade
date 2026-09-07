//go:build linux

// Purpose: a minimal in-process org.freedesktop.Secret.Service peer used to
//
//	drive the REAL wire protocol custody_linux.go speaks, without a session
//	bus. The backend under test talks to it over an in-memory duplex pipe
//	using the genuine godbus client, so every Call()/Store() production
//	code makes runs for real; only the SASL handshake and method bodies
//	are scripted. Built on two io.Pipe()s rather than net.Pipe(): the
//	default unit lane forbids importing "net" at all (Art.7.2), even for a
//	loopback pipe that never opens a real socket.
//
// Inputs: the bool/slice fields a test sets on a fakeSecretService before
//
//	its first connect() call.
//
// Outputs: a connect func matching secretServiceCustody.connect's shape.
//
// Constraints: implements only the methods custody_linux.go calls; an
//
//	unrecognised call gets a typed D-Bus error reply, never a hang, so a
//	gap here shows up as a test failure and not a stuck goroutine.
//
// SPORT: internal/secrets Custody/TEST (linux secret-service fake peer,
//
//	R-14 CI coverage gap).

package secrets

import (
	"bufio"
	"encoding/binary"
	"io"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"
)

// duplexPipe is one end of an in-memory, full-duplex byte stream built
// from two io.Pipe()s, so dbus.NewConn has a genuine io.ReadWriteCloser
// without importing "net" (forbidden in the default unit lane, Art.7.2).
type duplexPipe struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (d *duplexPipe) Read(p []byte) (int, error)  { return d.r.Read(p) }
func (d *duplexPipe) Write(p []byte) (int, error) { return d.w.Write(p) }
func (d *duplexPipe) Close() error {
	_ = d.r.Close()
	return d.w.Close()
}

// newDuplexPipe returns two connected ends: whatever is written to one is
// read from the other, in both directions.
func newDuplexPipe() (a, b *duplexPipe) {
	ar, bw := io.Pipe()
	br, aw := io.Pipe()
	return &duplexPipe{r: ar, w: aw}, &duplexPipe{r: br, w: bw}
}

// fakeSecretItem is one stored item in the fake collection.
type fakeSecretItem struct {
	path   dbus.ObjectPath
	attrs  map[string]string
	value  []byte
	locked bool
}

// fakeSecretService is the scripted peer. Configure its fields before the
// first connect() call for a given test; each call opens its own session
// over its own duplexPipe, exactly like the real backend's open().
type fakeSecretService struct {
	mu     sync.Mutex
	items  []*fakeSecretItem
	nextID int

	unowned         bool // GetNameOwner reports no owner (Available = false)
	locked          bool // Unlock leaves the default collection locked
	failOpenSession bool
	failSearch      bool
	failCreate      bool
	failGetSecret   bool
	failDelete      bool
	failPropGet     bool
	corruptAttrs    bool
}

func newFakeSecretService() *fakeSecretService {
	return &fakeSecretService{}
}

// connect builds a connect func with the same shape as dbus.SessionBus:
// each call dials a fresh pipe, starts this service serving one end, and
// authenticates the other end as a real *dbus.Conn.
func (f *fakeSecretService) connect(t *testing.T) func() (*dbus.Conn, error) {
	t.Helper()
	return func() (*dbus.Conn, error) {
		server, client := newDuplexPipe()
		go f.serve(server)
		conn, err := dbus.NewConn(client)
		if err != nil {
			_ = client.Close()
			return nil, err
		}
		if err := conn.Auth([]dbus.Auth{dbus.AuthAnonymous()}); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	}
}

// serve speaks the SASL handshake then the binary message loop for one
// connection, replying to every method call until the pipe closes.
func (f *fakeSecretService) serve(rw *duplexPipe) {
	defer func() { _ = rw.Close() }()
	r := bufio.NewReader(rw)
	if !f.handshake(r, rw) {
		return
	}
	for {
		msg, err := dbus.DecodeMessage(r)
		if err != nil {
			return
		}
		if msg.Type != dbus.TypeMethodCall {
			continue
		}
		reply := f.dispatch(msg)
		reply.Headers[dbus.FieldReplySerial] = dbus.MakeVariant(msg.Serial())
		if err := reply.EncodeTo(rw, binary.LittleEndian); err != nil {
			return
		}
	}
}

// handshake speaks just enough SASL ANONYMOUS for godbus's client-side
// Auth: a leading NUL byte, a mechanism probe, one AUTH round trip and
// BEGIN. Line content is not otherwise validated; a malformed handshake
// from this trusted, in-process peer is not a scenario under test.
func (f *fakeSecretService) handshake(r *bufio.Reader, w io.Writer) bool {
	if _, err := r.ReadByte(); err != nil {
		return false
	}
	if _, err := r.ReadString('\n'); err != nil { // "AUTH"
		return false
	}
	if _, err := w.Write([]byte("REJECTED ANONYMOUS\r\n")); err != nil {
		return false
	}
	if _, err := r.ReadString('\n'); err != nil { // "AUTH ANONYMOUS"
		return false
	}
	if _, err := w.Write([]byte("OK 0123456789abcdef0123456789abcdef\r\n")); err != nil {
		return false
	}
	_, err := r.ReadString('\n') // "BEGIN"
	return err == nil
}

func (f *fakeSecretService) itemAt(p dbus.ObjectPath) *fakeSecretItem {
	for _, it := range f.items {
		if it.path == p {
			return it
		}
	}
	return nil
}

func (f *fakeSecretService) removeItem(p dbus.ObjectPath) {
	out := f.items[:0]
	for _, it := range f.items {
		if it.path != p {
			out = append(out, it)
		}
	}
	f.items = out
}

// attrsMatch reports whether have carries every key/value pair in want,
// the same subset semantics org.freedesktop.Secret.Service.SearchItems
// documents.
func attrsMatch(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

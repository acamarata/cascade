package sync

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestTransferBytesRoundTrip(t *testing.T) {
	payload := bytes.Repeat([]byte("cascade sync payload "), 500)
	var wire bytes.Buffer
	if err := TransferBytes(context.Background(), &wire, 1, payload, 128, 0); err != nil {
		t.Fatalf("TransferBytes: %v", err)
	}
	got, received, err := ReceiveBytes(context.Background(), &wire, 1, 0)
	if err != nil {
		t.Fatalf("ReceiveBytes: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("round trip mismatch: got %d bytes, want %d", len(got), len(payload))
	}
	if received == 0 {
		t.Fatal("received chunk count must be > 0")
	}
}

func TestTransferBytesEmptyPayload(t *testing.T) {
	var wire bytes.Buffer
	if err := TransferBytes(context.Background(), &wire, 1, nil, 128, 0); err != nil {
		t.Fatalf("TransferBytes: %v", err)
	}
	got, _, err := ReceiveBytes(context.Background(), &wire, 1, 0)
	if err != nil {
		t.Fatalf("ReceiveBytes: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d bytes, want 0", len(got))
	}
}

// dropAfterN simulates a mid-transfer tunnel drop: it forwards writes to
// an underlying buffer for the first n chunks and then reports EOF
// (io.ErrClosedPipe) forever after, as a dropped connection would.
type dropAfterN struct {
	buf     *bytes.Buffer
	n       int
	written int
}

func (d *dropAfterN) Write(p []byte) (int, error) {
	if d.written >= d.n {
		return 0, io.ErrClosedPipe
	}
	d.written++
	return d.buf.Write(p)
}

func TestTunnelDropMidTransferResumesFromLastAcked(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 1000)
	chunkSize := 100 // 10 chunks total
	wire := &bytes.Buffer{}
	dropper := &dropAfterN{buf: wire, n: 4}

	err := TransferBytes(context.Background(), dropper, 9, payload, chunkSize, 0)
	if err == nil {
		t.Fatal("expected the simulated drop to surface an error")
	}

	// The receiver sees exactly the 4 chunks that made it onto the wire
	// before the drop, and reports having durably received 4 — the
	// resume point for the sender's next attempt.
	got1, received1, rerr := ReceiveBytes(context.Background(), wire, 9, 0)
	if rerr == nil {
		t.Fatal("expected ReceiveBytes to report the stream ended early (fewer chunks than Total)")
	}
	if received1 != 4 {
		t.Fatalf("received1 = %d, want 4", received1)
	}
	if len(got1) != 4*chunkSize {
		t.Fatalf("got1 = %d bytes, want %d", len(got1), 4*chunkSize)
	}

	// Resume: the sender continues from seq 4, the receiver continues
	// expecting seq 4, over a FRESH wire buffer representing the
	// reconnected tunnel.
	wire2 := &bytes.Buffer{}
	if err := TransferBytes(context.Background(), wire2, 9, payload, chunkSize, received1); err != nil {
		t.Fatalf("resumed TransferBytes: %v", err)
	}
	got2, received2, err := ReceiveBytes(context.Background(), wire2, 9, received1)
	if err != nil {
		t.Fatalf("resumed ReceiveBytes: %v", err)
	}
	if received2 != 10 {
		t.Fatalf("received2 = %d, want 10 (total chunks)", received2)
	}
	full := append(append([]byte{}, got1...), got2...)
	if !bytes.Equal(full, payload) {
		t.Fatal("resumed transfer did not reassemble to the original payload")
	}
}

func TestReceiveStreamRejectsReorderedChunk(t *testing.T) {
	var wire bytes.Buffer
	_ = Encode(&wire, Chunk{StreamID: 5, Seq: 1, Total: 2, Payload: []byte("second")})
	_, err := ReceiveStream(context.Background(), &wire, 5, 0, func(uint64, uint64, []byte) error { return nil })
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("reordered chunk: want KindIntegrity, got %v", err)
	}
}

func TestReceiveStreamRejectsWrongStreamID(t *testing.T) {
	var wire bytes.Buffer
	_ = Encode(&wire, Chunk{StreamID: 99, Seq: 0, Total: 1, Payload: []byte("x")})
	_, err := ReceiveStream(context.Background(), &wire, 1, 0, func(uint64, uint64, []byte) error { return nil })
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("wrong stream id: want KindIntegrity, got %v", err)
	}
}

func TestReceiveStreamRejectsTruncatedChunk(t *testing.T) {
	var wire bytes.Buffer
	_ = Encode(&wire, Chunk{StreamID: 1, Seq: 0, Total: 2, Payload: []byte("full chunk")})
	truncated := bytes.NewReader(wire.Bytes()[:wire.Len()-3])
	_, err := ReceiveStream(context.Background(), truncated, 1, 0, func(uint64, uint64, []byte) error { return nil })
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("truncated chunk: want KindIntegrity, got %v", err)
	}
}

func TestSendStreamCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var wire bytes.Buffer
	err := TransferBytes(ctx, &wire, 1, []byte("hello"), 1, 0)
	if !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("canceled ctx: want KindCanceled, got %v", err)
	}
}

func TestTransferBytesDefaultChunkSize(t *testing.T) {
	var wire bytes.Buffer
	payload := []byte("small payload")
	if err := TransferBytes(context.Background(), &wire, 1, payload, 0, 0); err != nil {
		t.Fatalf("TransferBytes with chunkSize<=0: %v", err)
	}
	got, _, err := ReceiveBytes(context.Background(), &wire, 1, 0)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("round trip with default chunk size failed: %v, %q", err, got)
	}
}

func TestSendStreamSourceErrorPropagates(t *testing.T) {
	var wire bytes.Buffer
	wantErr := cascade.New(cascade.KindInternal, "source failed")
	err := SendStream(context.Background(), &wire, 1, 2, 0, func(uint64) ([]byte, error) { return nil, wantErr })
	if err != wantErr {
		t.Fatalf("SendStream source error: got %v, want %v", err, wantErr)
	}
}

func TestReceiveStreamSinkErrorStopsAtThatChunk(t *testing.T) {
	var wire bytes.Buffer
	_ = Encode(&wire, Chunk{StreamID: 1, Seq: 0, Total: 2, Payload: []byte("a")})
	_ = Encode(&wire, Chunk{StreamID: 1, Seq: 1, Total: 2, Payload: []byte("b")})
	wantErr := cascade.New(cascade.KindInternal, "sink failed")
	received, err := ReceiveStream(context.Background(), &wire, 1, 0, func(seq, _ uint64, _ []byte) error {
		if seq == 0 {
			return wantErr
		}
		return nil
	})
	if err != wantErr {
		t.Fatalf("ReceiveStream sink error: got %v, want %v", err, wantErr)
	}
	if received != 0 {
		t.Fatalf("received = %d, want 0 (the failing chunk is not counted as received)", received)
	}
}

func TestReceiveStreamCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var wire bytes.Buffer
	_ = Encode(&wire, Chunk{StreamID: 1, Seq: 0, Total: 1, Payload: []byte("a")})
	_, err := ReceiveStream(ctx, &wire, 1, 0, func(uint64, uint64, []byte) error { return nil })
	if !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("canceled ctx: want KindCanceled, got %v", err)
	}
}

func TestSendStreamResumeBeyondTotalRefused(t *testing.T) {
	var wire bytes.Buffer
	err := SendStream(context.Background(), &wire, 1, 3, 10, func(uint64) ([]byte, error) { return nil, nil })
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("resume beyond total: want KindInvalidInput, got %v", err)
	}
}

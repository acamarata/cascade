package registryfetch

// Purpose: unit tests for readHTTPResponse (doer.go), the response-body
//   draining logic split out of realDoer.Do specifically so it could be
//   tested here with a plain io.Reader — no net/http import, no socket.
//   Internal test package (not registryfetch_test) because
//   readHTTPResponse is unexported.
// SPORT: internal/plugins/registryfetch (ADD) — P1-E24-W5-S50-T1.

import (
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestReadHTTPResponse_OK(t *testing.T) {
	resp, err := readHTTPResponse(200, strings.NewReader("hello"), "https://example.com/x")
	if err != nil {
		t.Fatalf("readHTTPResponse: %v", err)
	}
	if resp.StatusCode != 200 || string(resp.Body) != "hello" {
		t.Fatalf("readHTTPResponse = %+v, want StatusCode=200 Body=hello", resp)
	}
}

// failingReader always errors, proving readHTTPResponse wraps a read
// failure as KindUnavailable rather than swallowing it.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestReadHTTPResponse_ReadError(t *testing.T) {
	_, err := readHTTPResponse(200, failingReader{}, "https://example.com/x")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("readHTTPResponse read error = %v, want KindUnavailable", err)
	}
}

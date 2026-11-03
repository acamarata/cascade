// Purpose: default-lane coverage of NewHTTPTransport's non-nil branch and
// refuseRedirect without importing net/http (the no-network unit scan
// forbids that import). A real *http.Client is obtained through generic
// inference on NewHTTPTransport's own signature and reflection.
package transport

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// sigOf infers NewHTTPTransport's parameter and result types so the test
// can build a zero value of the parameter type without naming it.
func sigOf[A, R any](_ func(A) R) A {
	var zero A
	return zero
}

// newClientValue returns a fresh, non-nil *http.Client typed as A.
func newClientValue[A any](t *testing.T, nilPtr A) A {
	t.Helper()
	v := reflect.New(reflect.TypeOf(nilPtr).Elem())
	return v.Interface().(A)
}

func TestRefuseRedirect_ReturnsUseLastResponse(t *testing.T) {
	err := refuseRedirect(nil, nil)
	if err == nil {
		t.Fatal("refuseRedirect returned nil, want http.ErrUseLastResponse")
	}
	if !strings.Contains(err.Error(), "use last response") {
		t.Fatalf("refuseRedirect error = %q, want the use-last-response sentinel", err)
	}
}

func TestNewHTTPTransport_CopiesClientAndRefusesRedirects(t *testing.T) {
	client := newClientValue(t, sigOf(NewHTTPTransport))
	if client.CheckRedirect != nil {
		t.Fatal("fresh client already has a CheckRedirect")
	}
	nt, ok := NewHTTPTransport(client).(netTransport)
	if !ok {
		t.Fatal("NewHTTPTransport did not return a netTransport")
	}
	if nt.client == nil || nt.client == client {
		t.Fatalf("transport client = %p, want a distinct non-nil copy of %p", nt.client, client)
	}
	if nt.client.CheckRedirect == nil {
		t.Fatal("transport client has no CheckRedirect")
	}
	if err := nt.client.CheckRedirect(nil, nil); err == nil {
		t.Fatal("transport CheckRedirect allowed the redirect")
	}
	if client.CheckRedirect != nil {
		t.Fatal("NewHTTPTransport mutated the caller's client")
	}
}

func TestNetTransportSend_ClientErrorIsReturnedWithoutDialing(t *testing.T) {
	tr := NewHTTPTransport(newClientValue(t, sigOf(NewHTTPTransport)))
	// An unregistered scheme fails inside Client.Do before any dial.
	status, hdr, body, err := tr.Send(context.Background(), "GET", "nosuchscheme://host/x", nil, nil)
	if err == nil {
		t.Fatal("Send over an unsupported scheme returned nil error")
	}
	if status != 0 || hdr != nil || body != nil {
		t.Fatalf("Send = %d %v %v, want zero values on error", status, hdr, body)
	}
	if !strings.Contains(err.Error(), "nosuchscheme") {
		t.Fatalf("error %q does not name the scheme", err)
	}
}

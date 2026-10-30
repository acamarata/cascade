package intake

import (
	"context"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestReauthGatewayReverifies checks fresh source resolution and write-after-verify.
func TestReauthGatewayReverifies(t *testing.T) {
	deps, custody, doer, req, _ := gatewaySetup(t)
	req.NoVerify = true
	res, err := AddGateway(context.Background(), deps, req)
	if err != nil {
		t.Fatal(err)
	}
	before := res.Record
	doer.calls = nil
	calls := 0
	src := req.Source
	getenv := src.Getenv
	src.Getenv = func(k string) string { calls++; return getenv(k) }
	res, err = ReauthGateway(context.Background(), deps, before, req.Account, src)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(doer.calls) != 1 || res.Record.VerifySkipped {
		t.Fatal("reauth did not re-verify")
	}
	doer.responses[req.Account.BaseURL+"/v1/messages"] = HTTPResponse{Status: 401}
	_, err = ReauthGateway(context.Background(), deps, res.Record, req.Account, src)
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatal(err)
	}
	after, err := deps.Registry.GetProvider(context.Background(), req.Name)
	if err != nil || !reflect.DeepEqual(after, res.Record) {
		t.Fatal("failed verify changed record")
	}
	bad := before
	bad.AuthRef = "provider.x.key"
	_, err = ReauthGateway(context.Background(), deps, bad, req.Account, src)
	if !cascade.HasKind(err, cascade.KindInvalidInput) || calls != 2 {
		t.Fatal("malformed ref reached source")
	}
	if len(custody.getCall) != 0 || len(custody.setCall) != 0 {
		t.Fatal("reauth accessed custody")
	}
}

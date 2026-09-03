package rpc

import (
	"encoding/json"
	"testing"
)

// specFixture is a wire example lifted directly from the JSON-RPC 2.0 spec
// (jsonrpc.org, "rpc call with positional parameters"), satisfying this
// ticket's AC: "at least one test exercises a spec-sourced wire fixture."
const specFixture = `{"jsonrpc": "2.0", "method": "subtract", "params": [42, 23], "id": 1}`

func TestParse_SpecFixture(t *testing.T) {
	req, errObj := Parse([]byte(specFixture))
	if errObj != nil {
		t.Fatalf("Parse(spec fixture) failed: %+v", errObj)
	}
	if req.Method != "subtract" {
		t.Errorf("Method = %q, want subtract", req.Method)
	}
	if req.IsNotification() {
		t.Error("spec fixture carries an id; must not be a notification")
	}
	var id int
	if err := json.Unmarshal(req.ID, &id); err != nil || id != 1 {
		t.Errorf("ID = %s, want 1", req.ID)
	}
}

func TestParse_Notification(t *testing.T) {
	req, errObj := Parse([]byte(`{"jsonrpc":"2.0","method":"update","params":[1,2,3]}`))
	if errObj != nil {
		t.Fatalf("Parse(notification) failed: %+v", errObj)
	}
	if !req.IsNotification() {
		t.Error("request with no id key must be a notification")
	}
}

func TestParse_NullIDIsNotANotification(t *testing.T) {
	req, errObj := Parse([]byte(`{"jsonrpc":"2.0","method":"m","id":null}`))
	if errObj != nil {
		t.Fatalf("Parse failed: %+v", errObj)
	}
	if req.IsNotification() {
		t.Error("explicit null id key present must NOT be treated as a notification")
	}
}

func TestParse_MalformedJSON(t *testing.T) {
	_, errObj := Parse([]byte(`{not json`))
	if errObj == nil {
		t.Fatal("expected a parse error")
	}
	if errObj.Code != codeParseError {
		t.Errorf("Code = %d, want %d", errObj.Code, codeParseError)
	}
}

func TestParse_EmptyBody(t *testing.T) {
	_, errObj := Parse(nil)
	if errObj == nil || errObj.Code != codeParseError {
		t.Fatalf("expected codeParseError for empty body, got %+v", errObj)
	}
}

func TestParse_MissingJSONRPCVersion(t *testing.T) {
	_, errObj := Parse([]byte(`{"method":"m","id":1}`))
	if errObj == nil || errObj.Code != codeInvalidRequest {
		t.Fatalf("expected codeInvalidRequest, got %+v", errObj)
	}
}

func TestParse_WrongJSONRPCVersion(t *testing.T) {
	_, errObj := Parse([]byte(`{"jsonrpc":"1.0","method":"m","id":1}`))
	if errObj == nil || errObj.Code != codeInvalidRequest {
		t.Fatalf("expected codeInvalidRequest, got %+v", errObj)
	}
}

func TestParse_MissingMethod(t *testing.T) {
	_, errObj := Parse([]byte(`{"jsonrpc":"2.0","id":1}`))
	if errObj == nil || errObj.Code != codeInvalidRequest {
		t.Fatalf("expected codeInvalidRequest, got %+v", errObj)
	}
}

func TestParse_EmptyMethod(t *testing.T) {
	_, errObj := Parse([]byte(`{"jsonrpc":"2.0","method":"","id":1}`))
	if errObj == nil || errObj.Code != codeInvalidRequest {
		t.Fatalf("expected codeInvalidRequest, got %+v", errObj)
	}
}

func TestParse_BatchRejected(t *testing.T) {
	_, errObj := Parse([]byte(`[{"jsonrpc":"2.0","method":"a","id":1}]`))
	if errObj == nil || errObj.Code != codeInvalidRequest {
		t.Fatalf("expected batch rejection at codeInvalidRequest, got %+v", errObj)
	}
}

func TestParse_ClientVersionField(t *testing.T) {
	req, errObj := Parse([]byte(`{"jsonrpc":"2.0","method":"m","id":1,"client_version":"2.0.0"}`))
	if errObj != nil {
		t.Fatalf("Parse failed: %+v", errObj)
	}
	if req.ClientVersion != "2.0.0" {
		t.Errorf("ClientVersion = %q, want 2.0.0", req.ClientVersion)
	}
}

func TestParse_NeverPanicsOnGarbage(t *testing.T) {
	inputs := [][]byte{
		nil,
		{},
		[]byte("\x00\x01\x02"),
		[]byte(`{`),
		[]byte(`}`),
		[]byte(`null`),
		[]byte(`true`),
		[]byte(`42`),
		[]byte(`"a string"`),
		[]byte(`{"jsonrpc":2.0,"method":"m"}`),
		[]byte(`{"jsonrpc":"2.0","method":123}`),
		[]byte(`{"jsonrpc":"2.0","method":"m","id":{"nested":{"deep":true}}}`),
	}
	for i, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("input %d: Parse panicked: %v", i, r)
				}
			}()
			_, _ = Parse(in)
		}()
	}
}

func TestErrorObject_Error(t *testing.T) {
	var nilErr *ErrorObject
	if nilErr.Error() != "" {
		t.Errorf("nil *ErrorObject.Error() = %q, want empty", nilErr.Error())
	}
	eo := &ErrorObject{Message: "boom"}
	if eo.Error() != "boom" {
		t.Errorf("Error() = %q, want boom", eo.Error())
	}
}

func TestParamsHash_Deterministic(t *testing.T) {
	r1 := &Request{Params: json.RawMessage(`{"a":1}`)}
	r2 := &Request{Params: json.RawMessage(`{"a":1}`)}
	r3 := &Request{Params: json.RawMessage(`{"a":2}`)}
	if r1.ParamsHash() != r2.ParamsHash() {
		t.Error("identical params must hash identically")
	}
	if r1.ParamsHash() == r3.ParamsHash() {
		t.Error("different params must hash differently")
	}
}

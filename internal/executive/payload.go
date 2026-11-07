package executive

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// MaxPayloadBytes is the largest payload body ValidatePayload accepts.
const MaxPayloadBytes = 16384

// PayloadV1 is the only payload schema version this build understands.
const PayloadV1 PayloadVersion = 1

// Payload validation errors. Their text never carries the scanned bytes.
var (
	// ErrInvalidExecPayload reports a payload body that is oversized,
	// malformed, not exactly one object, or missing a required field.
	ErrInvalidExecPayload = errors.New("invalid exec payload")
	// ErrExecPayloadVersion reports an unknown kind or an unsupported version.
	ErrExecPayloadVersion = errors.New("unsupported exec payload kind or version")
)

// groupPayloads maps each kind group to its v1 body type.
var groupPayloads = map[KindGroup]reflect.Type{
	GroupProject:    reflect.TypeOf(ProjectPayload{}),
	GroupTask:       reflect.TypeOf(TaskPayload{}),
	GroupAgent:      reflect.TypeOf(AgentPayload{}),
	GroupArtifact:   reflect.TypeOf(ArtifactPayload{}),
	GroupDecision:   reflect.TypeOf(DecisionPayload{}),
	GroupVerify:     reflect.TypeOf(VerifyPayload{}),
	GroupQuota:      reflect.TypeOf(QuotaPayload{}),
	GroupWorkspace:  reflect.TypeOf(WorkspacePayload{}),
	GroupState:      reflect.TypeOf(StatePayload{}),
	GroupExecutive:  reflect.TypeOf(ExecutivePayload{}),
	GroupSafety:     reflect.TypeOf(SafetyPayload{}),
	GroupAcceptance: reflect.TypeOf(ProjectPayload{}),
}

// kindPayloads holds the kinds whose body differs from their group's.
var kindPayloads = map[Kind]reflect.Type{
	KindTaskFailed:           reflect.TypeOf(TaskFailedPayload{}),
	KindQuotaLow:             reflect.TypeOf(QuotaLowPayload{}),
	KindPrivacyPolicyBlocked: reflect.TypeOf(PrivacyBlockedPayload{}),
}

// payloadTable maps every AllKinds kind to its v1 body type.
var payloadTable = buildPayloadTable()

func buildPayloadTable() map[Kind]reflect.Type {
	table := make(map[Kind]reflect.Type, len(AllKinds))
	for _, g := range kindGroups {
		for _, k := range g.kinds {
			if t, ok := kindPayloads[k]; ok {
				table[k] = t
				continue
			}
			table[k] = groupPayloads[g.group]
		}
	}
	return table
}

// ValidatePayload checks raw against the closed v1 body of kind before it
// is journaled. Checks run in order: size, kind and version, duplicate
// keys, credential keys at every depth, then a strict decode that demands
// exactly one JSON object, no unknown field and every required field set.
func ValidatePayload(kind Kind, version PayloadVersion, raw []byte) error {
	if len(raw) > MaxPayloadBytes {
		return fmt.Errorf("%w: %d bytes exceeds %d", ErrInvalidExecPayload, len(raw), MaxPayloadBytes)
	}
	if _, err := ParseExecKind(string(kind)); err != nil {
		return fmt.Errorf("%w: kind is not a closed exec kind", ErrExecPayloadVersion)
	}
	if version != PayloadV1 {
		return fmt.Errorf("%w: version %d, want %d", ErrExecPayloadVersion, version, PayloadV1)
	}
	if err := checkDuplicateKeys(raw); err != nil {
		return fmt.Errorf("%w: duplicate or malformed key", ErrInvalidExecPayload)
	}
	if err := CheckPayloadKeys(raw); err != nil {
		return err
	}
	return strictDecode(payloadTable[kind], raw)
}

// strictDecode decodes raw as exactly one object of typ and checks that
// every field is set. Decoder error text is dropped because it can quote
// the input.
func strictDecode(typ reflect.Type, raw []byte) error {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("%w: payload is not a JSON object", ErrInvalidExecPayload)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	v := reflect.New(typ)
	if err := dec.Decode(v.Interface()); err != nil {
		return fmt.Errorf("%w: payload does not match %s", ErrInvalidExecPayload, typ.Name())
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: data after the payload object", ErrInvalidExecPayload)
	}
	return checkRequired(v.Elem())
}

// checkRequired enforces that every field of the decoded body is set.
func checkRequired(v reflect.Value) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := v.Field(i)
		missing := false
		switch f.Kind() { //nolint:exhaustive // only the field kinds the payload schema declares are checked for presence; every other reflect.Kind is never required
		case reflect.String:
			missing = f.String() == ""
		case reflect.Int64:
			missing = f.Int() < 1
		case reflect.Pointer:
			missing = f.IsNil()
		}
		if missing {
			return fmt.Errorf("%w: %s requires %s", ErrInvalidExecPayload, t.Name(), jsonName(t.Field(i)))
		}
	}
	return nil
}

// jsonName returns the JSON name of a struct field, falling back to its Go name.
func jsonName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "" {
		return f.Name
	}
	return name
}

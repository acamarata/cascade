package pews

import (
	"bytes"

	"gopkg.in/yaml.v3"

	"github.com/acamarata/cascade/pkg/cascade"
)

// normativeFields lists the 17 contract fields in 06-FORGE-SPEC.md §1's
// normative order. It is the single source both DecodeTicket's key check
// and Ticket's field declaration order agree with.
var normativeFields = []string{
	"id", "title", "short_desc", "full_desc", "branch", "weight",
	"model_class", "depends_on", "tasks", "checks", "acceptance_criteria",
	"files_scope", "spec_refs", "cr_level", "qa_level", "sport_updates",
	"docs_updates",
}

// extraFlagFields lists the Forge-declared extra flags and no others.
//
// security_class was MISSING here until the W3 hardening gate ran the
// tagged artifact against cascade's own planning tree and every pbd
// command failed on it (`unknown ticket field "security_class"`). The flag
// is ratified — R-14.215 item 5 restored it on three AN tickets after a
// sweep stripped it, and rules that any future removal must cite a ruling
// — so the schema was the side that was wrong. Nothing caught it because
// the only test that reads the real tree is skipped unless
// CASCADE_PBD_DOGFOOD_SRC is set, and it never was.
var extraFlagFields = []string{"subtickets", "journals", "owner_prereq", "gate_only", "external_contract", "amendment_note", "security_class"}

// filesScopeFields lists the three keys files_scope may carry.
var filesScopeFields = []string{"add", "change", "delete"}

// FilesScope is the files_scope field (06 §1 field 12): the ADD/CHANGE/
// DELETE shape a ticket declares its file-level effect through. Each list
// is optional and, when present, keeps the order the ticket wrote it in.
type FilesScope struct {
	Add    []string `yaml:"add"`
	Change []string `yaml:"change"`
	Delete []string `yaml:"delete"`
}

// Phase is the PEWS phase-level record: exactly one field, Draft, per
// R-21.276. A draft phase (phase.yaml sets draft: true) accepts ticket
// edits without weight/model_class validation and cannot be built;
// store.go's Load excludes its tickets from an active read unless the
// caller passes LoadOptions.IncludeDrafts. A missing phase.yaml means
// Phase{} (Draft false) — see draft.go's loadPhaseRecord/DecodePhase.
type Phase struct {
	// Draft marks the phase a draft (default false); no directory or
	// marker-file convention carries draft-ness (R-21.276).
	Draft bool `yaml:"draft"`
}

// Ticket is the PEWS ticket-schema v2 contract: the 17 normative fields in
// 06 §1 order, followed by the declared extra flags and no others. A
// pointer field (Journals, OwnerPrereq, GateOnly, ExternalContract,
// AmendmentNote, SecurityClass) is nil when the YAML omitted the key;
// Subtickets uses a nil slice.
type Ticket struct {
	ID                 string     `yaml:"id"`
	Title              string     `yaml:"title"`
	ShortDesc          string     `yaml:"short_desc"`
	FullDesc           string     `yaml:"full_desc"`
	Branch             string     `yaml:"branch"`
	Weight             Weight     `yaml:"weight"`
	ModelClass         ModelClass `yaml:"model_class"`
	DependsOn          []string   `yaml:"depends_on"`
	Tasks              []string   `yaml:"tasks"`
	Checks             []string   `yaml:"checks"`
	AcceptanceCriteria []string   `yaml:"acceptance_criteria"`
	FilesScope         FilesScope `yaml:"files_scope"`
	SpecRefs           []string   `yaml:"spec_refs"`
	CRLevel            CRLevel    `yaml:"cr_level"`
	QALevel            QALevel    `yaml:"qa_level"`
	SportUpdates       []string   `yaml:"sport_updates"`
	DocsUpdates        []string   `yaml:"docs_updates"`

	// Extra flags (06 §1 "Extra flags"). Declared here and nowhere else.
	Subtickets       []string `yaml:"subtickets,omitempty"`
	Journals         *bool    `yaml:"journals,omitempty"`
	OwnerPrereq      *string  `yaml:"owner_prereq,omitempty"`
	GateOnly         *bool    `yaml:"gate_only,omitempty"`
	ExternalContract *bool    `yaml:"external_contract,omitempty"`

	// AmendmentNote records a binding post-authoring scope change; free text.
	AmendmentNote *string `yaml:"amendment_note,omitempty"`

	// SecurityClass marks a ticket whose work is security-critical
	// (R-14.215 item 5). Nil when the YAML omitted the key.
	SecurityClass *bool `yaml:"security_class,omitempty"`
}

// DecodeTicket parses data as a PEWS ticket-schema v2 document. It never
// panics: any malformed input — an unparseable document, a non-mapping
// root, an unknown or missing field, a value of the wrong shape, or a value
// outside a locked set — returns a *cascade.Error of kind
// cascade.KindInvalidInput and a zero Ticket.
func DecodeTicket(data []byte) (t Ticket, err error) {
	defer func() {
		if r := recover(); r != nil {
			t, err = Ticket{}, cascade.Newf(cascade.KindInvalidInput, "ticket YAML decode panicked: %v", r)
		}
	}()

	var doc yaml.Node
	if uerr := yaml.Unmarshal(data, &doc); uerr != nil {
		return Ticket{}, cascade.Wrap(cascade.KindInvalidInput, uerr, "malformed ticket YAML")
	}
	root, rerr := mappingRoot(&doc)
	if rerr != nil {
		return Ticket{}, rerr
	}
	if kerr := checkKeys(root); kerr != nil {
		return Ticket{}, kerr
	}

	var raw Ticket
	if derr := root.Decode(&raw); derr != nil {
		return Ticket{}, cascade.Wrap(cascade.KindInvalidInput, derr, "malformed ticket YAML value")
	}
	if verr := validateEnums(raw); verr != nil {
		return Ticket{}, verr
	}
	return raw, nil
}

// EncodeTicket serializes t as a PEWS ticket-schema v2 YAML document, with
// its fields in 06 §1's normative order followed by any set extra flags.
func EncodeTicket(t Ticket) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if eerr := enc.Encode(t); eerr != nil {
		return nil, cascade.Wrap(cascade.KindInternal, eerr, "failed to encode ticket YAML")
	}
	if cerr := enc.Close(); cerr != nil {
		return nil, cascade.Wrap(cascade.KindInternal, cerr, "failed to close ticket YAML encoder")
	}
	return buf.Bytes(), nil
}

// mappingRoot descends a decoded document node to its root mapping,
// rejecting an empty document or a non-mapping root.
func mappingRoot(doc *yaml.Node) (*yaml.Node, error) {
	n := doc
	for n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return nil, cascade.New(cascade.KindInvalidInput, "empty ticket YAML document")
		}
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return nil, cascade.New(cascade.KindInvalidInput, "ticket YAML root must be a mapping")
	}
	return n, nil
}

// checkKeys validates root's top-level keys against the normative and
// extra-flag field sets, requires every normative field present, and
// recurses into files_scope's own key set when present.
func checkKeys(root *yaml.Node) error {
	seen := map[string]bool{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, val := root.Content[i].Value, root.Content[i+1]
		if !inSet(key, normativeFields) && !inSet(key, extraFlagFields) {
			return cascade.Newf(cascade.KindInvalidInput, "unknown ticket field %q", key)
		}
		if key == "files_scope" {
			if ferr := checkFilesScopeKeys(val); ferr != nil {
				return ferr
			}
		}
		seen[key] = true
	}
	for _, f := range normativeFields {
		if !seen[f] {
			return cascade.Newf(cascade.KindInvalidInput, "missing required ticket field %q", f)
		}
	}
	return nil
}

// checkFilesScopeKeys validates that a files_scope value is a mapping whose
// keys are a subset of add/change/delete.
func checkFilesScopeKeys(val *yaml.Node) error {
	if val.Kind != yaml.MappingNode {
		return cascade.New(cascade.KindInvalidInput, "files_scope must be a mapping")
	}
	for i := 0; i+1 < len(val.Content); i += 2 {
		key := val.Content[i].Value
		if !inSet(key, filesScopeFields) {
			return cascade.Newf(cascade.KindInvalidInput, "unknown files_scope field %q", key)
		}
	}
	return nil
}

// validateEnums checks t's four locked-value-set fields.
func validateEnums(t Ticket) error {
	switch {
	case !t.Weight.Valid():
		return cascade.Newf(cascade.KindInvalidInput, "invalid weight %q", t.Weight)
	case !t.ModelClass.Valid():
		return cascade.Newf(cascade.KindInvalidInput, "invalid model_class %q", t.ModelClass)
	case !t.CRLevel.Valid():
		return cascade.Newf(cascade.KindInvalidInput, "invalid cr_level %q", t.CRLevel)
	case !t.QALevel.Valid():
		return cascade.Newf(cascade.KindInvalidInput, "invalid qa_level %q", t.QALevel)
	}
	return nil
}

// inSet moved to draft.go (same package) for line-budget room.

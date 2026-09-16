// Purpose (this file): the four locked ENUM domains a PEWS ticket carries —
//
//	weight, model_class, qa_level and cr_level — with their closed value
//	sets and validity rules.
//
// Why they live beside schema.go rather than in it: schema.go owns the
//
//	ticket's field set and its YAML codec, and the two grew past Art.10.3's
//	300-line cap together. The split is by subject, not by line count: an
//	enum's closed set changes for a different reason than the field list
//	does.
//
// Constraints: every set here is CLOSED. A value outside it is invalid
//
//	input, never a tolerated unknown — DecodeTicket refuses the document
//	rather than admitting a ticket whose weight or model_class nothing
//	downstream can route.
//
// SPORT: plugins/pbd/internal/pews:enums (ADD) — P1-E14-W3-S30-T5 (Art.10.3).

package pews

import "strings"

// Weight is a ticket's sizing class (06 §1 field 6). The zero value is
// intentionally invalid; use Valid to test membership.
type Weight string

// WeightXS, WeightS, WeightM, WeightL, and WeightXL are the five locked
// weight values, in the order 06 §1 lists them, smallest to largest.
const (
	WeightXS Weight = "XS"
	WeightS  Weight = "S"
	WeightM  Weight = "M"
	WeightL  Weight = "L"
	WeightXL Weight = "XL"
)

var weightValues = []Weight{WeightXS, WeightS, WeightM, WeightL, WeightXL}

// Valid reports whether w is one of the five locked weight values.
func (w Weight) Valid() bool { return inSet(w, weightValues) }

// ModelClass is a ticket's execution-time model tier (06 §1 field 7, §4).
type ModelClass string

// The five model-class values 06 §4 defines: mech (mechanical sweeps/
// config/goldens), build (standard implementation), heavy (concurrency/
// storage/firewall/sync), review (CR-B), and arbiter (CR-C or gate tickets).
const (
	ModelClassMech    ModelClass = "mech"
	ModelClassBuild   ModelClass = "build"
	ModelClassHeavy   ModelClass = "heavy"
	ModelClassReview  ModelClass = "review"
	ModelClassArbiter ModelClass = "arbiter"
)

var modelClassValues = []ModelClass{
	ModelClassMech, ModelClassBuild, ModelClassHeavy, ModelClassReview, ModelClassArbiter,
}

// Valid reports whether m is one of the five locked model-class values.
func (m ModelClass) Valid() bool { return inSet(m, modelClassValues) }

// QALevel is a ticket's QA depth (06 §1 field 15).
type QALevel string

// The three QA-level values 06 §1 defines: QA-A (unit default), QA-B
// (integration at a cross-epic seam), and QA-C (e2e on an acceptance ticket).
const (
	QALevelA QALevel = "QA-A"
	QALevelB QALevel = "QA-B"
	QALevelC QALevel = "QA-C"
)

var qaLevelValues = []QALevel{QALevelA, QALevelB, QALevelC}

// Valid reports whether q is one of the three locked QA-level values.
func (q QALevel) Valid() bool { return inSet(q, qaLevelValues) }

// CRLevel is a ticket's code-review depth (06 §1 field 14): a non-empty,
// '+'-joined, strictly increasing combination of CR-A, CR-B, and CR-C, e.g.
// "CR-B" or "CR-A+CR-B+CR-C". The join order always follows CR-A, CR-B,
// CR-C; Valid rejects any other order, a repeated token, or an unknown one.
type CRLevel string

// CRLevelA, CRLevelB, and CRLevelC are the three CR-level tokens a CRLevel
// combines, in join order.
const (
	CRLevelA CRLevel = "CR-A"
	CRLevelB CRLevel = "CR-B"
	CRLevelC CRLevel = "CR-C"
)

var crLevelTokens = []CRLevel{CRLevelA, CRLevelB, CRLevelC}

// Valid reports whether c is a well-formed CRLevel combination.
func (c CRLevel) Valid() bool {
	if c == "" {
		return false
	}
	last := -1
	for _, part := range strings.Split(string(c), "+") {
		idx := crLevelIndex(CRLevel(part))
		if idx < 0 || idx <= last {
			return false
		}
		last = idx
	}
	return true
}

func crLevelIndex(c CRLevel) int {
	for i, t := range crLevelTokens {
		if t == c {
			return i
		}
	}
	return -1
}

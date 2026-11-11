package jobs

// EvidenceKind is the closed, eight-member evidence vocabulary. The zero
// value is deliberately not a member (R-16.37 no-permissive-zero rule).
type EvidenceKind string

const (
	// EvidenceBuild is a passing build's evidence row.
	EvidenceBuild EvidenceKind = "build"
	// EvidenceLint is a passing lint run's evidence row.
	EvidenceLint EvidenceKind = "lint"
	// EvidenceTests is a passing test run's evidence row.
	EvidenceTests EvidenceKind = "tests"
	// EvidenceReview is a passing code review's evidence row.
	EvidenceReview EvidenceKind = "review"
	// EvidenceAdversarial is a passing adversarial review's evidence row.
	EvidenceAdversarial EvidenceKind = "adversarial"
	// EvidenceCIAttestation is a real CI attestation's evidence row.
	EvidenceCIAttestation EvidenceKind = "ci_attestation"
	// EvidenceHumanApproval is a Critical-class human approval's row.
	EvidenceHumanApproval EvidenceKind = "human_approval"
	// EvidenceQA is a passing independent QA run's evidence row (daemon-produced).
	EvidenceQA EvidenceKind = "qa"
)

// Valid reports whether k is one of the eight closed members.
func (k EvidenceKind) Valid() bool {
	switch k {
	case EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview,
		EvidenceAdversarial, EvidenceCIAttestation, EvidenceHumanApproval, EvidenceQA:
		return true
	}
	return false
}

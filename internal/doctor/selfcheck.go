package doctor

import "context"

// Purpose: DoctorSelfCheck — the toy check the framework ships with
//
//	(ticket contract task 8), proving a CheckRegistry can register and
//	run one check end-to-end without depending on any real subsystem.
//
// Constraints: intentionally trivial — it verifies only that Run
//
//	receives a live, not-yet-expired context and that Fix's idempotent
//	"already correct" shape works, both exercised by
//	selfcheck_test.go and runner_test.go's happy-path/idempotency
//	tests.
//
// SPORT: placeholder: doctor/framework (ADD).

// doctorSelfCheck implements Check as the framework's own smoke-test
// check.
type doctorSelfCheck struct{}

// NewDoctorSelfCheck returns the framework-shipped self-check.
func NewDoctorSelfCheck() Check { return doctorSelfCheck{} }

func (doctorSelfCheck) Name() string { return "doctor_selfcheck" }

func (doctorSelfCheck) Describe() string {
	return "verifies the doctor check registry can register and run a check end-to-end"
}

func (doctorSelfCheck) Metadata() CheckMeta {
	return CheckMeta{FirstRun: true, Fixable: true}
}

// Run reports StatusOK once it observes a live context; a context that
// is already done by the time Run is invoked is treated as an
// unverifiable subject (Art.1) rather than silently reported OK.
func (doctorSelfCheck) Run(ctx context.Context) (CheckResult, error) {
	if err := ctx.Err(); err != nil {
		return CheckResult{Status: StatusError, Message: "context already done before self-check could run", Detail: err.Error()}, nil
	}
	return CheckResult{Status: StatusOK, Message: "doctor framework operational: registry, runner, and this check all ran"}, nil
}

// Fix always reports the idempotent "already correct" shape: the
// self-check has nothing to remediate, by design, so it demonstrates the
// FixResult{Applied:false, Delta:""} convention every real fixable check
// must also produce on a second, already-correct run.
func (doctorSelfCheck) Fix(context.Context) (FixResult, error) {
	return FixResult{Applied: false, Delta: ""}, nil
}

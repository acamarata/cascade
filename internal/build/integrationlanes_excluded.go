// Purpose: the integration packages no lane runs, each with the reason (split
// from integrationlanes_table.go to keep that file within the 300-line cap).
// Inputs: none. Outputs: IntegrationExcluded, read by CheckLaneCoverage.
// Constraints: a new entry needs a reasoned message naming the local command.
// SPORT: internal/build (P1-CI-17).

package build

// IntegrationExcluded lists integration-constrained packages no lane runs,
// each with its reason (the local command to run instead).
var IntegrationExcluded = map[string]string{
	"./internal/jobs": "TestAcceptancePath2KillResume and TestAcceptancePath4RPCSpotCheck fail under the integration tag with 'job.show/job.list over the daemon socket: -32007 caller not permitted'; fix them, then add a lane row. Run: go test -tags integration ./internal/jobs",
}

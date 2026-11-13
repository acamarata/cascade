// Package write provides control-plane confirmation logic.
package write

// Tick represents a single observation from the supervisor.
type Tick struct {
	SupervisorAlive bool
	StopMarker      bool
	Load1           float64
}

// Finding represents a confirmed diagnostic finding.
type Finding struct {
	Code string
}

// Confirm evaluates consecutive ticks and returns findings when
// two-tick confirmation thresholds are met.
func Confirm(ticks []Tick) []Finding {
	var findings []Finding

	supDeadCount := 0
	loadHighCount := 0

	for _, t := range ticks {
		if !t.SupervisorAlive && !t.StopMarker {
			supDeadCount++
		} else {
			supDeadCount = 0
		}

		if t.Load1 > 60 {
			loadHighCount++
		} else {
			loadHighCount = 0
		}

		if supDeadCount >= 2 {
			findings = append(findings, Finding{Code: "D1079"})
			supDeadCount = 0
		}

		if loadHighCount >= 2 {
			findings = append(findings, Finding{Code: "D1084"})
			loadHighCount = 0
		}
	}

	return findings
}

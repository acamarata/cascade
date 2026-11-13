package write

// ChangeResult is the result reported for a configuration change request.
type ChangeResult struct {
	// Status is "applied" when every requested key can take effect live.
	// Status is "needs_restart" when at least one requested key requires a
	// restart or is not registered.
	Status string
}

// restartRequiredKeys is the static table of configuration keys whose
// changes take effect only after a restart.
var restartRequiredKeys = map[string]bool{
	"orch.listen_addr": true,
	"orch.db_path":     true,
	"orch.max_workers": true,
}

// liveKeys is the static table of configuration keys whose changes take
// effect without a restart.
var liveKeys = map[string]bool{
	"orch.log_level": true,
}

// ApplyChange reports the change status for the supplied configuration keys.
// It returns "needs_restart" if any key requires a restart or is unknown.
// It returns "applied" only when every key is registered as live-applied.
// The function only reports status; it never performs or schedules a restart.
func ApplyChange(keys []string) ChangeResult {
	for _, key := range keys {
		if restartRequiredKeys[key] {
			return ChangeResult{Status: "needs_restart"}
		}
		if !liveKeys[key] {
			return ChangeResult{Status: "needs_restart"}
		}
	}
	return ChangeResult{Status: "applied"}
}

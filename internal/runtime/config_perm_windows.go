//go:build windows

package runtime

// Purpose: the windows half of the config.toml permission classifier.
// Inputs: the opened config file and its target (unused).
// Outputs: a not_checked finding.
// Constraints: NTFS ACLs are not modelled by unix mode bits, so no verdict
//   is given and Load never refuses or warns on windows. A future ACL check
//   replaces this.
// SPORT: runtime/config (ADD, P1-CORE-16).

import "os"

func classifyOpenConfig(*os.File, configTarget) (ConfigPermFinding, error) {
	return ConfigPermFinding{
		Level:  ConfigPermNotChecked,
		Reason: "config permissions are not checked on windows: NTFS ACLs are not modelled by unix mode bits",
	}, nil
}

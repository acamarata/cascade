package secrets

import "testing"

// TestFileVaultEnvIgnoredWithoutDevkeys: the QA switch forces the file
// vault only in a devkeys build. A release build with
// CASCADE_TEST_FILE_VAULT=1 selects exactly the custody it selects without
// it. The build bit is a parameter (the composition root passes the one
// devkeys constant, and cmd/cascade's tests prove it does), so both cases
// run in every build.
func TestFileVaultEnvIgnoredWithoutDevkeys(t *testing.T) {
	on := func(key string) string {
		if key == testFileVaultEnv {
			return "1"
		}
		return ""
	}
	for _, tc := range []struct {
		name    string
		devkeys bool
		getenv  func(string) string
		want    bool
	}{
		{"release build, switch set", false, on, false},
		{"release build, switch unset", false, func(string) string { return "" }, false},
		{"devkeys build, switch set", true, on, true},
		{"devkeys build, switch unset", true, func(string) string { return "" }, false},
		{"devkeys build, switch not exactly 1", true, func(string) string { return "true" }, false},
		{"devkeys build, no reader", true, nil, false},
	} {
		if got := QAFileVaultForced(tc.devkeys, tc.getenv); got != tc.want {
			t.Errorf("%s: QAFileVaultForced = %v, want %v", tc.name, got, tc.want)
		}
	}
}

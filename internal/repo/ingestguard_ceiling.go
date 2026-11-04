// Purpose: the init-walk file-count ceiling (P1-NODE-13a). A root whose
// candidate count exceeds the ceiling is refused KindPolicyDenied naming
// both numbers; the default applies when no ceiling is configured.

package repo

import (
	"github.com/acamarata/cascade/pkg/cascade"
)

// DefaultFileCountCeiling is the file-count ceiling used when no
// [roots].max_files value or confirmation raises it.
const DefaultFileCountCeiling = 200000

// EnforceCeiling returns a KindPolicyDenied error naming count and ceiling
// when count exceeds ceiling, and nil otherwise.
func EnforceCeiling(count, ceiling int) error {
	if count <= ceiling {
		return nil
	}
	return cascade.Newf(cascade.KindPolicyDenied, "file count %d exceeds ceiling %d", count, ceiling)
}

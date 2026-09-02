// Package other imports deadpkg and calls only UsedFunc — proving
// cross-package usage detection.
package other

import "example.com/fixture/internal/deadpkg"

func CallIt() int {
	return deadpkg.UsedFunc()
}

/* extra.c: an orphan, cgo-less C source file with no .go file in this
 * directory. `go list ./...` still visits this directory (any directory
 * under a module root is a candidate package) and fails with "C source
 * files not allowed when not using cgo or SWIG" -- a real, unrelated
 * package elsewhere in the tree that does not build. See
 * affected_go_test.go's TestAffectedTargets_UnrelatedBrokenPackageForcesFull
 * (D1 REWORK round 2 fix, confirm review case 3).
 */
int unused(void) { return 0; }

// Package capmap holds the capability-map harness: the test code that
// checks docs/capability-map.md against the public capability list in
// internal/build/testdata/capability-ids.txt.
//
// Everything that does work lives in _test.go files behind the capmap build
// tag, so the package ships no product code and exports nothing. The tag is
// opt-in: run the harness with
//
//	go test -tags capmap -count=1 ./internal/integration/capmap/
//
// The harness has four parts, all unexported:
//
//   - evaluate checks the table against the capability list and the set of
//     probes that passed in the same run;
//   - the probe registry maps a probe name to a test function that probe
//     files fill from init;
//   - TestCapmapTable_Evaluates runs every verified row's probe as a subtest
//     and then evaluates the committed table with the probes that passed;
//   - a helper builds the cascade binary once per test binary into a temp
//     directory and runs it with an isolated home directory.
package capmap

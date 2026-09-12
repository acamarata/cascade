package main

// main is intentionally inert: this plugin is catalogued "off"
// (02-TARGET-STRUCTURE.md §First-party plugin catalog v1), so no host-ABI
// wiring runs at process start. The real, tested raw→canonical→derived
// pipeline lives in domain.go, exercised by domain_test.go and by the
// manifest/WASM-compile checks in plugins/examples/integration_test.go.
func main() {}

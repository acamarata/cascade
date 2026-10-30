package plugins

// Purpose: install a config-value secret scanner for this test binary.
// Production installs the process detector adapter at cascade's init
// (cmd/cascade/config_secret_scanner.go); without one every config write
// refuses (P1-SEC-30). This scanner detects nothing, so these wiring tests
// see the same LooksLikeSecret screen they were written against.

import "github.com/acamarata/cascade/internal/runtime"

func init() { runtime.SetSecretLiteralScanner(func(string) []string { return nil }) }

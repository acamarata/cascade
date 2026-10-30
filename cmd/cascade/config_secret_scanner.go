// Purpose: installs the process secret detector as the scanner every
// config value write runs (P1-SEC-30, AUD-027). Without it ConfigWriter.Set,
// ApplyDiff and `config edit` refuse (runtime.ScanTreeForSecrets fails closed).
// Inputs: none. Outputs: the installed runtime.SecretLiteralScanner.
// Constraints: the adapter resolves secrets.ProcessDetector on every scan,
// never at init, so the [secrets] source installed at startup governs it.
// SPORT: cmd/cascade config secret scanner (P1-SEC-30).
package main

import (
	"github.com/acamarata/cascade/cmd/cascade/config"
	"github.com/acamarata/cascade/internal/runtime"
)

func init() { runtime.SetSecretLiteralScanner(config.ProcessSecretLiteralScanner) }

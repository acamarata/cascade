// Purpose: providerDepsFor, the provider dependency constructor that takes
//
//	its PathProvider as a PARAMETER rather than capturing an ambient one.
//
// Constraints: every consumer that needs the path (custody, the elevation
//
//	backend, the registry) must be built from the SAME injected provider,
//	which is why this is a constructor and not a struct a caller can patch.
//
// SPORT: cmd/cascade provider-deps-constructor (ADD).

package main

import (
	"io"
	"os"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// providerDepsFor builds providerDeps against a CALLER-SUPPLIED
// PathProvider.
//
// It exists because productionProviderDeps captures its paths inside the
// NewCustody and Gate closures, so a caller that took the returned struct
// and reassigned only its Paths field changed WHERE the registry was
// opened while leaving custody and the elevation backend still pointed at
// the real home directory. That is not a hypothetical: doctor's
// provider-health mount did exactly that, the darwin run looked clean, and
// the linux CI lane caught it creating a real ~/.cascade under a HOME the
// hygiene gate asserts stays empty. Rebinding the parameter is the fix;
// reassigning a field on the result is not.
func providerDepsFor(paths runtime.PathProvider) providerDeps {
	getenv := os.Getenv
	return providerDeps{
		Paths: paths,
		NewCustody: func() (secrets.Custody, error) {
			dir := paths.DataDir()
			if dir == "" {
				return nil, cascade.New(cascade.KindUnavailable, "provider: could not resolve the cascade data directory")
			}
			return secrets.SelectCustody(secrets.Config{Service: vaultService, Dir: dir})
		},
		Gate: newElevationGate(
			elevation.NewKeystore,
			func() elevation.Backend { return elevation.NewFileBackend(paths.DataDir()) },
			runtime.NewSystemClock(), getenv,
		),
		Getenv: getenv,
		ReadStdin: func() ([]byte, error) {
			return io.ReadAll(io.LimitReader(os.Stdin, maxSecretValueBytes+1))
		},
		StdinIsPiped: productionStdinIsPiped,
		Doer:         intake.NewHTTPDoer(intakeHTTPTimeout),
	}
}

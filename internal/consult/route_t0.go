package consult

import "errors"

// ErrNativeOpusForbidden indicates that native Opus model is forbidden.
var ErrNativeOpusForbidden = errors.New("D6166: native Opus forbidden")

// ErrInvalidT0Request indicates that the T0 request is invalid.
var ErrInvalidT0Request = errors.New("invalid T0 request")

// ErrNoBurnAccount indicates that no burn account is available.
var ErrNoBurnAccount = errors.New("no burn account")

// T0Request represents a T0 routing request.
type T0Request struct {
	Tier             string
	Model            string
	NativeQuotaShort bool
	BurnAccount      bool
}

// T0Route represents a T0 routing destination.
type T0Route struct {
	Destination string
	Account     string
}

// RouteT0Plan evaluates a T0 routing request and returns the appropriate route.
func RouteT0Plan(req T0Request) (T0Route, error) {
	// Reject native Opus first for any Tier/quota state
	if req.Model == "native Opus" {
		return T0Route{}, ErrNativeOpusForbidden
	}

	// Check for exact T0 tier
	if req.Tier != "T0" {
		return T0Route{}, ErrInvalidT0Request
	}

	// Check for valid model: empty, "GC2", or "Haiku 5.5"
	validModels := map[string]bool{
		"":          true,
		"GC2":       true,
		"Haiku 5.5": true,
	}
	if !validModels[req.Model] {
		return T0Route{}, ErrInvalidT0Request
	}

	// Quota short routes to GC2 regardless of BurnAccount
	if req.NativeQuotaShort {
		return T0Route{
			Destination: "GC2",
			Account:     "",
		}, nil
	}

	// Return a valid route
	return T0Route{
		Destination: "t0-default",
		Account:     "t0-account",
	}, nil
}

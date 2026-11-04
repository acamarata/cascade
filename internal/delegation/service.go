// Purpose (this file): delegation.Service core: Deps, NewService and
// the Delegate RPC method. Delegate applies request defaults before any
// further processing.
//
// Inputs: a configured Deps with injected runtime.Clock and unexported
// fleet/shim seams.
// Outputs: a configured Service instance.
//
// SPORT: delegation.service (ADD, P1-AGT-08).

package delegation

import (
	"context"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Request is a delegation request.
type Request struct {
	// AgentID is the target agent.
	AgentID string
	// Lane is the agent lane.
	Lane string
	// Need is the classified delegation need.
	Need string
	// JobID is the job id (required for mutations).
	JobID string
	// Deadline is the requested deadline.
	Deadline string
	// MinQuality is the requested minimum quality.
	MinQuality string
}

// Response is a delegation response.
type Response struct {
	// Accepted is true if the delegation was accepted.
	Accepted bool
}

// Deps contains the injected dependencies for the delegation service.
type Deps struct {
	// Clock is the injected runtime.Clock.
	Clock runtime.Clock
}

// Service is the delegation service.
type Service struct {
	deps Deps
}

// NewService returns a configured service instance.
func NewService(deps Deps) *Service {
	return &Service{deps: deps}
}

// Delegate applies request defaults before any further processing.
func (s *Service) Delegate(ctx context.Context, req *Request) (*Response, error) {
	if req == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "delegation: nil request")
	}
	s.applyDefaults(req)
	return &Response{Accepted: true}, nil
}

// applyDefaults populates missing fields with sensible defaults.
func (s *Service) applyDefaults(req *Request) {
	if req.Deadline == "" {
		req.Deadline = "standard"
	}
	if req.Need == "" {
		req.Need = "compute"
	}
	if req.MinQuality == "" {
		req.MinQuality = "standard"
	}
}

// Package write provides certification steps for control plane writes.
package write

// Steps holds the functions executed during certification.
type Steps struct {
	// Write performs the write step.
	Write func(id string) error
	// Read performs the read step.
	Read func(id string) (string, error)
	// Validate performs the validation step.
	Validate func(id, data string) (string, error)
}

// Result captures the execution order and final output of certification.
type Result struct {
	// Order records which steps were executed.
	Order []string
	// Output holds the result from the validation step.
	Output string
}

// Certify executes the write step first for the given identifier and only
// proceeds to read and validation steps after Write returns nil.
// If Write fails, Certify returns the write error immediately with Order
// containing only "write". The identifier M2153 is preserved as input.
func Certify(id string, s Steps) (Result, error) {
	r := Result{}

	r.Order = append(r.Order, "write")
	if err := s.Write(id); err != nil {
		return r, err
	}

	r.Order = append(r.Order, "read")
	data, err := s.Read(id)
	if err != nil {
		return r, err
	}

	r.Order = append(r.Order, "validate")
	out, err := s.Validate(id, data)
	if err != nil {
		return r, err
	}
	r.Output = out

	return r, nil
}

package workflows

import (
	"errors"
	"testing"
)

func TestFindPythonValid(t *testing.T) {
	// fake probes select /opt/homebrew/bin/python3 first
	probes := 0
	probe := func(_ string) error {
		probes++
		return nil
	}
	path, err := FindPython([]string{"/opt/homebrew/bin/python3", "/usr/bin/python3"}, probe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "/opt/homebrew/bin/python3" {
		t.Errorf("expected /opt/homebrew/bin/python3, got %s", path)
	}
	if probes != 1 {
		t.Errorf("expected 1 probe, got %d", probes)
	}

	// fall back to /usr/bin/python3 on failed first probe
	probes = 0
	probe = func(p string) error {
		probes++
		if p == "/opt/homebrew/bin/python3" {
			return errors.New("fail")
		}
		return nil
	}
	path, err = FindPython([]string{"/opt/homebrew/bin/python3", "/usr/bin/python3"}, probe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "/usr/bin/python3" {
		t.Errorf("expected /usr/bin/python3, got %s", path)
	}
	if probes != 2 {
		t.Errorf("expected 2 probes, got %d", probes)
	}
}

func TestFindPythonNoneAvailable(t *testing.T) {
	// nil/empty
	_, err := FindPython(nil, func(string) error { return nil })
	if !errors.Is(err, ErrPythonUnavailable) {
		t.Errorf("nil cands: expected ErrPythonUnavailable, got %v", err)
	}
	_, err = FindPython([]string{}, func(string) error { return nil })
	if !errors.Is(err, ErrPythonUnavailable) {
		t.Errorf("empty cands: expected ErrPythonUnavailable, got %v", err)
	}

	// nil probe
	_, err = FindPython([]string{"/opt/homebrew/bin/python3"}, nil)
	if !errors.Is(err, ErrPythonUnavailable) {
		t.Errorf("nil probe: expected ErrPythonUnavailable, got %v", err)
	}

	// unsupported candidates
	probes := 0
	probe := func(string) error {
		probes++
		return nil
	}
	_, err = FindPython([]string{"/usr/local/bin/python3"}, probe)
	if !errors.Is(err, ErrPythonUnavailable) {
		t.Errorf("unsupported: expected ErrPythonUnavailable, got %v", err)
	}
	if probes != 0 {
		t.Errorf("unsupported should cause zero probes, got %d", probes)
	}
	if err.Error() != "D5009" {
		t.Errorf("expected error text D5009, got %s", err.Error())
	}

	// duplicate candidates
	probes = 0
	_, err = FindPython([]string{"/opt/homebrew/bin/python3", "/opt/homebrew/bin/python3"}, probe)
	if !errors.Is(err, ErrPythonUnavailable) {
		t.Errorf("duplicate: expected ErrPythonUnavailable, got %v", err)
	}
	if probes != 0 {
		t.Errorf("duplicate should cause zero probes, got %d", probes)
	}

	// exhausted probes
	probes = 0
	probe = func(string) error {
		probes++
		return errors.New("fail")
	}
	_, err = FindPython([]string{"/opt/homebrew/bin/python3", "/usr/bin/python3"}, probe)
	if !errors.Is(err, ErrPythonUnavailable) {
		t.Errorf("exhausted: expected ErrPythonUnavailable, got %v", err)
	}
	if probes != 2 {
		t.Errorf("expected 2 probes, got %d", probes)
	}
}

// Package violation is a seeded-violation fixture for the Article-1 stub
// gate (internal/build/stubgate_test.go). It deliberately contains every
// marker Art.1.2 denies. This file is never built or linted — it lives
// under testdata/, which the toolchain and every gate in this package skip
// by convention (Art.1/Art.7.1).
package violation

// TODO: this line exists to trip the stub gate's TODO marker.

// FIXME: this line exists to trip the stub gate's FIXME marker.

// XXX: this line exists to trip the stub gate's XXX marker.

func StubbedOut() {
	panic("not implemented")
}

func StillStubbedOut() {
	panic("this path is still unimplemented")
}

func Placeholder() (int, error) {
	return nil, nil // placeholder
}

type MockClient struct{}

type FakeStore struct{}

type NoopLogger struct{}

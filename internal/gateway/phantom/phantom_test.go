package phantom

import (
	"testing"
)

// fakeCache implements SizeCache for testing
type fakeCache struct {
	stale  map[string]bool
	limits map[string]int
}

func newFakeCache() *fakeCache {
	return &fakeCache{
		stale:  make(map[string]bool),
		limits: make(map[string]int),
	}
}

func (f *fakeCache) MarkStale(model string) {
	f.stale[model] = true
}

func (f *fakeCache) IsStale(model string) bool {
	return f.stale[model]
}

func (f *fakeCache) Limit(model string) int {
	if limit, ok := f.limits[model]; ok {
		return limit
	}
	return 0
}

func (f *fakeCache) SetLimit(model string, limit int) {
	f.limits[model] = limit
}

// TestPhantomIsRetryable verifies that a phantom reply returns Retryable true.
func TestPhantomIsRetryable(t *testing.T) {
	cache := newFakeCache()
	model := "test-model"
	reply := Reply{Kind: KindPhantom}

	result := HandleReply(cache, model, reply)

	if !result.Retryable {
		t.Errorf("expected phantom reply to be retryable, got false")
	}
}

// TestPhantomMarksCacheStale verifies that after HandleReply, the fake cache reports stale true for the model.
func TestPhantomMarksCacheStale(t *testing.T) {
	cache := newFakeCache()
	model := "test-model"
	reply := Reply{Kind: KindPhantom}

	HandleReply(cache, model, reply)

	if !cache.IsStale(model) {
		t.Errorf("expected cache to be marked stale for model %q, got false", model)
	}
}

// TestPhantomKeepsLimitConstant verifies that cached limit 8000 before and after HandleReply.
func TestPhantomKeepsLimitConstant(t *testing.T) {
	cache := newFakeCache()
	model := "test-model"
	cache.SetLimit(model, 8000)
	reply := Reply{Kind: KindPhantom}

	before := cache.Limit(model)
	HandleReply(cache, model, reply)
	after := cache.Limit(model)

	if before != 8000 {
		t.Errorf("expected limit before to be 8000, got %d", before)
	}
	if after != 8000 {
		t.Errorf("expected limit after to be 8000, got %d", after)
	}
}

// TestRepeatedPhantomsKeepLimit verifies that 5 phantom replies leave the limit at 8000.
func TestRepeatedPhantomsKeepLimit(t *testing.T) {
	cache := newFakeCache()
	model := "test-model"
	cache.SetLimit(model, 8000)
	reply := Reply{Kind: KindPhantom}

	for i := 0; i < 5; i++ {
		HandleReply(cache, model, reply)
	}

	limit := cache.Limit(model)
	if limit != 8000 {
		t.Errorf("expected limit to remain 8000 after 5 phantom replies, got %d", limit)
	}
}

// TestNonPhantomNotHandled verifies that a normal reply returns Retryable false and does not mark the cache stale.
func TestNonPhantomNotHandled(t *testing.T) {
	cache := newFakeCache()
	model := "test-model"
	reply := Reply{Kind: "normal"}

	result := HandleReply(cache, model, reply)

	if result.Retryable {
		t.Errorf("expected non-phantom reply to not be retryable, got true")
	}
	if cache.IsStale(model) {
		t.Errorf("expected cache to not be marked stale for non-phantom reply, got true")
	}
}

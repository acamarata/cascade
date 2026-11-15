package models

import (
	"testing"
)

// TestDefaultEmbedderIsOllama verifies that NewDefaultEmbedder returns an
// *OllamaEmbedder configured with the expected defaults.
func TestDefaultEmbedderIsOllama(t *testing.T) {
	e := NewDefaultEmbedder()

	if _, ok := interface{}(e).(*OllamaEmbedder); !ok {
		t.Fatalf("expected *OllamaEmbedder, got %T", e)
	}

	if e.BaseURL != "http://127.0.0.1:11434" {
		t.Errorf("BaseURL: expected http://127.0.0.1:11434, got %q", e.BaseURL)
	}
	if e.ModelName != "bge-m3" {
		t.Errorf("ModelName: expected bge-m3, got %q", e.ModelName)
	}
	if e.HTTP != SharedCtxwinHTTP {
		t.Errorf("HTTP: expected SharedCtxwinHTTP, got %v", e.HTTP)
	}
}

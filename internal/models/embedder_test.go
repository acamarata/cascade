package models

import (
	"testing"
)

// TestDefaultEmbedderIsOllama verifies that NewDefaultEmbedder returns the correct Ollama adapter type
// with the expected BaseURL, ModelName, and HTTP client.
func TestDefaultEmbedderIsOllama(t *testing.T) {
	var e Embedder = NewDefaultEmbedder()

	// Verify that the returned embedder is of type *OllamaEmbedder
	o, ok := e.(*OllamaEmbedder)
	if !ok {
		t.Fatalf("NewDefaultEmbedder() returned %T, want *OllamaEmbedder", e)
	}

	// Verify BaseURL
	expectedBaseURL := "http://127.0.0.1:11434"
	if o.BaseURL != expectedBaseURL {
		t.Errorf("BaseURL = %q, want %q", o.BaseURL, expectedBaseURL)
	}

	// Verify ModelName
	expectedModelName := "bge-m3"
	if o.ModelName != expectedModelName {
		t.Errorf("ModelName = %q, want %q", o.ModelName, expectedModelName)
	}

	// Verify HTTP client is SharedCtxwinHTTP
	if o.HTTP != SharedCtxwinHTTP {
		t.Errorf("HTTP = %v, want %v (SharedCtxwinHTTP)", o.HTTP, SharedCtxwinHTTP)
	}
}

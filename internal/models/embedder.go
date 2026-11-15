// Package models defines the embedder interfaces and default implementations.
package models

import (
	"context"
	"errors"
	"net/http"
)

// ErrEmbedderInvalidInput is returned when the embedder receives invalid input.
var ErrEmbedderInvalidInput = errors.New("invalid embedder input")

// SharedCtxwinHTTP is the default HTTP client used by embedders.
var SharedCtxwinHTTP HTTPDoer = http.DefaultClient

// Embedder is the interface for text embedding models.
type Embedder interface {
	Model() string
	Local() bool
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// HTTPDoer is an interface for executing HTTP requests.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// OllamaEmbedder is an embedder implementation that uses the Ollama API.
type OllamaEmbedder struct {
	BaseURL   string
	ModelName string
	HTTP      HTTPDoer
}

// NewDefaultEmbedder returns a new OllamaEmbedder configured with default settings.
func NewDefaultEmbedder() *OllamaEmbedder {
	return &OllamaEmbedder{
		BaseURL:   "http://127.0.0.1:11434",
		ModelName: "bge-m3",
		HTTP:      SharedCtxwinHTTP,
	}
}

// Model returns the name of the model used by the embedder.
func (e *OllamaEmbedder) Model() string {
	return e.ModelName
}

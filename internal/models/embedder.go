package models

import (
	"context"
	"errors"
	"net/http"
)

// Embedder defines the interface for text embedding models.
type Embedder interface {
	// Model returns the name of the model.
	Model() string
	// Local returns whether the model runs locally.
	Local() bool
	// Embed generates embeddings for the provided texts.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// HTTPDoer defines the interface for executing HTTP requests.
type HTTPDoer interface {
	// Do executes an HTTP request.
	Do(req *http.Request) (*http.Response, error)
}

// ErrEmbedderInvalidInput is returned when the embedder receives invalid input.
var ErrEmbedderInvalidInput = errors.New("invalid embedder input")

// SharedCtxwinHTTP is the default HTTP client used by embedders.
var SharedCtxwinHTTP HTTPDoer = http.DefaultClient

// OllamaEmbedder implements the Embedder interface using a local Ollama server.
type OllamaEmbedder struct {
	// BaseURL is the address of the Ollama server.
	BaseURL string
	// ModelName is the name of the embedding model.
	ModelName string
	// HTTP is the client used to make requests.
	HTTP HTTPDoer
}

// NewDefaultEmbedder returns a new OllamaEmbedder configured with default settings.
func NewDefaultEmbedder() *OllamaEmbedder {
	return &OllamaEmbedder{
		BaseURL:   "http://127.0.0.1:11434",
		ModelName: "bge-m3",
		HTTP:      SharedCtxwinHTTP,
	}
}

// Model returns the name of the model used by the OllamaEmbedder.
func (o *OllamaEmbedder) Model() string {
	return o.ModelName
}

// Local returns true indicating the OllamaEmbedder uses a local model.
func (o *OllamaEmbedder) Local() bool {
	return true
}

// Embed generates embeddings for the given texts.
func (o *OllamaEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, ErrEmbedderInvalidInput
	}
	return nil, errors.New("embed operation not available")
}

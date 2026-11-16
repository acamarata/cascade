package models

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ErrEmbedderInvalidInput is returned when validateEmbed finds bad input.
var ErrEmbedderInvalidInput = errors.New("embedder invalid input")

// HTTPDoer executes HTTP requests.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// OllamaEmbedder embeds text via an Ollama endpoint.
type OllamaEmbedder struct {
	HTTP   HTTPDoer
	Model  string
	BaseURL string
}

// validateEmbed checks inputs before making any HTTP calls.
func (o *OllamaEmbedder) validateEmbed(ctx context.Context, texts []string) error {
	if o == nil || ctx == nil {
		return ErrEmbedderInvalidInput
	}
	if err := o.checkClient(); err != nil {
		return err
	}
	if len(texts) == 0 {
		return ErrEmbedderInvalidInput
	}
	seen := make(map[string]struct{}, len(texts))
	for _, t := range texts {
		if strings.TrimSpace(t) == "" {
			return ErrEmbedderInvalidInput
		}
		if _, dup := seen[t]; dup {
			return ErrEmbedderInvalidInput
		}
		seen[t] = struct{}{}
	}
	if strings.TrimSpace(o.Model) == "" {
		return ErrEmbedderInvalidInput
	}
	return o.checkBaseURL()
}

func (o *OllamaEmbedder) checkClient() error {
	if o.HTTP == nil {
		return ErrEmbedderInvalidInput
	}
	c, ok := o.HTTP.(*http.Client)
	if !ok {
		return ErrEmbedderInvalidInput
	}
	if c == nil {
		return ErrEmbedderInvalidInput
	}
	return nil
}

func (o *OllamaEmbedder) checkBaseURL() error {
	if o.BaseURL == "" {
		return ErrEmbedderInvalidInput
	}
	u, err := url.Parse(o.BaseURL)
	if err != nil {
		return ErrEmbedderInvalidInput
	}
	if u.Scheme != "http" {
		return ErrEmbedderInvalidInput
	}
	if u.User != nil {
		return ErrEmbedderInvalidInput
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return ErrEmbedderInvalidInput
	}
	if u.Path != "" && u.Path != "/" {
		return ErrEmbedderInvalidInput
	}
	host := u.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return ErrEmbedderInvalidInput
	}
	if p := u.Port(); p != "" {
		if err := checkPort(p); err != nil {
			return err
		}
	}
	return nil
}

func checkPort(p string) error {
	for _, c := range p {
		if c < '0' || c > '9' {
			return ErrEmbedderInvalidInput
		}
	}
	port, err := strconv.Atoi(p)
	if err != nil || port < 1 || port > 65535 {
		return ErrEmbedderInvalidInput
	}
	return nil
}

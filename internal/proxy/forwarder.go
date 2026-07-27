package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// UpstreamResponse is the buffered result of a forwarded call. The body is fully
// read so the gateway can both parse usage.prompt_tokens from it and relay it to
// the client; embedding responses are small enough (no streaming) that buffering
// is cheap.
type UpstreamResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// Forwarder sends an accepted request to the upstream and returns the buffered
// response. It is an interface so the handler can be tested against a stub with
// no network.
type Forwarder interface {
	Forward(ctx context.Context, path string, body []byte) (*UpstreamResponse, error)
}

// HTTPForwarder forwards to the Caddy load balancer, which fans out across the
// Ollama backends. The gateway's own API key is never forwarded — Ollama sits
// behind Caddy on a trusted network and needs no credential — so a client key
// cannot leak upstream.
type HTTPForwarder struct {
	baseURL string
	client  *http.Client
}

// NewHTTPForwarder returns a forwarder targeting baseURL (the Caddy upstream)
// with the given per-request timeout. Embedding calls can be slow under load, so
// the timeout is generous relative to a typical API.
func NewHTTPForwarder(baseURL string, timeout time.Duration) *HTTPForwarder {
	return &HTTPForwarder{
		baseURL: baseURL,
		client:  &http.Client{Timeout: timeout},
	}
}

// Forward POSTs body to baseURL+path as JSON and buffers the response.
func (f *HTTPForwarder) Forward(ctx context.Context, path string, body []byte) (*UpstreamResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("proxy: build upstream request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("proxy: upstream call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("proxy: read upstream body: %w", err)
	}
	return &UpstreamResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       respBody,
	}, nil
}

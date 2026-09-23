// Implementation for mock backend provider interface
// HTTP client for mock video backend
package mockvideo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
 
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

// Provides factory registration for the mock video provider in provider factory
var Registration = providers.Registration{
	Type: "mockvideo",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL:  defaultBaseURL,
		AllowAPIKeyless: true,
	},
}

const (
	defaultBaseURL = "http://mock-video:8000"
	modelName = "minimax-h3-mock"
	// requestTimeout bounds every request
	requestTimeout = 15 * time.Second
	// maxRetries bounds retries of a 503 from the backend's flakiness
	maxRetries = 5
	// defaultRetryWait is used if a 503 response has no Retry-After header
	defaultRetryWait = 1 * time.Second
)

// Reads Retry-After header and returns duration to wait before retrying
func retryAfter(h http.Header) time.Duration {
	secs, err := strconv.Atoi(h.Get("Retry-After"))
	// Default to 1 second if header is missing or invalid (negative)
	if err != nil || secs < 0 {
		return defaultRetryWait
	}
	return time.Duration(secs) * time.Second
}

// Waits for d, or returns ctx.Err() early if the context is cancelled
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	// Wait for either the timer to expire or the context to be done
	select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
	}
}

// Implements core.NativeVideoProvider for the mock video backend
type Provider struct {
	baseURL string
	http    *http.Client
}

// BackendError represents the error response from the mock video backend
type BackendError struct {
	Error struct {
		Code    string
		Message string
	}
}

// Constructor for the mock video provider
// Resolves confibured base URL or uses default if not provided
// Builds http.Client with 15s timeout for all requests to the mock backend
func New(providerCfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{
		baseURL: providers.ResolveBaseURL(providerCfg.BaseURL, defaultBaseURL),
		http:    &http.Client{Timeout: requestTimeout},
	}
}

// Health check of mock video backend
func (p *Provider) CheckAvailability(ctx context.Context) error {
	_, err := p.HealthCheck(ctx)
	return err
}

// Helper for handling HTTP requests to mock video backend
func (p *Provider) do(ctx context.Context, method, path string, body, out any) error {
	// Marshal the body to JSON if it's not nil
	var bodyBytes []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("mockvideo: marshal request: %w", err)
		}
		bodyBytes = b
	}
 
	// Repeat HTTP request if it encounters retryable server error (retry-after header)
	for attempt := 0; ; attempt++ {
		// Prepare request body reader
		var reqBody io.Reader
		if bodyBytes != nil {
			reqBody = bytes.NewReader(bodyBytes)
		}
 
		// Build the HTTP request
		req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, reqBody)
		if err != nil {
			return fmt.Errorf("mockvideo: build request: %w", err)
		}
		// Set Content-Type header for JSON body
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}
 
		// Send the HTTP request
		resp, err := p.http.Do(req)
		if err != nil {
			return fmt.Errorf("mockvideo: request failed: %w", err)
		}
 
		// Check for retryable errors and retry
		// Don't retry on POST to prevent duplicate jobs
		if resp.StatusCode == http.StatusServiceUnavailable 
		&& method != http.MethodPost && attempt < maxRetries {
			wait := retryAfter(resp.Header)
			resp.Body.Close()
			// Wait and then retry
			if err := sleep(ctx, wait); err != nil {
				return fmt.Errorf("mockvideo: %s %s: %w", method, path, err)
			}
			continue
		}
 
		// Error handling
		if resp.StatusCode >= http.StatusBadRequest {
			var backendErr BackendError
			if err := json.NewDecoder(resp.Body).Decode(&backendErr); err != nil {
				resp.Body.Close()
				return fmt.Errorf("mockvideo: request returned HTTP %d", resp.StatusCode)
			}
			resp.Body.Close()
			return fmt.Errorf("mockvideo: %s (%d): %s", backendErr.Error.Code, 
				resp.StatusCode, backendErr.Error.Message,
			)
		}
		if out == nil {
			resp.Body.Close()
			return nil
		}
		// Read JSON data
		decodeErr := json.NewDecoder(resp.Body).Decode(out)
		resp.Body.Close()
		return decodeErr
	}
}

// Submit a new video generation job
func (p *Provider) CreateVideo(ctx context.Context, req *core.VideoRequest) (*core.VideoResponse, error) {
	var resp core.VideoResponse
	if err := p.do(ctx, http.MethodPost, "/v1/videos", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Retrieve current status/progress for a job
func (p *Provider) GetVideo(ctx context.Context, id string) (*core.VideoResponse, error) {
	var resp core.VideoResponse
	if err := p.do(ctx, http.MethodGet, "/v1/videos/"+id, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Stream the raw mp4 bytes for a completed job
func (p *Provider) GetVideoContent(ctx context.Context, id string) (io.ReadCloser, error) {
	path := "/v1/videos/" + id + "/content"
	// Repeat HTTP request if it encounters retryable server error
	for attempt := 0; ; attempt++ {
		// Build the HTTP request
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+path, nil)
		if err != nil {
			return nil, fmt.Errorf("mockvideo: build request: %w", err)
		}
		
		// Send HTTP request
		resp, err := p.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("mockvideo: request failed: %w", err)
		}
 
		// Check for backend flakiness and retry
		if resp.StatusCode == http.StatusServiceUnavailable 
		&& attempt < maxRetries {
			wait := retryAfter(resp.Header)
			resp.Body.Close()
			// Wait and then retry
			if err := sleep(ctx, wait); err != nil {
				return nil, fmt.Errorf("mockvideo: %s: %w", path, err)
			}
			continue
		}
 
		// Error handling
		if resp.StatusCode >= http.StatusBadRequest {
			var backendErr BackendError
			if err := json.NewDecoder(resp.Body).Decode(&backendErr); err != nil {
				resp.Body.Close()
				return nil, fmt.Errorf("mockvideo: request returned HTTP %d", resp.StatusCode)
			}
			resp.Body.Close()
			return nil, fmt.Errorf("mockvideo: %s (%d): %s", backendErr.Error.Code, 
				resp.StatusCode, backendErr.Error.Message,)
		}
		return resp.Body, nil
	}
}

// Cancel/delete a job (best effort)
func (p *Provider) DeleteVideo(ctx context.Context, id string) (*core.VideoResponse, error) {
	var resp core.VideoResponse
	if err := p.do(ctx, http.MethodDelete, "/v1/videos/"+id, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Report liveness and active job count
func (p *Provider) HealthCheck(ctx context.Context) (*core.VideoHealthResponse, error) {
	var resp core.VideoHealthResponse
	if err := p.do(ctx, http.MethodGet, "/healthz", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Functions to satisfy the core.Provider struct
func errVideoOnly() error {
	return core.NewInvalidRequestError("mockvideo is a video-generation backend and does not support this operation", nil)
}

func (p *Provider) ChatCompletion(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, errVideoOnly()
}

func (p *Provider) StreamChatCompletion(_ context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	return nil, errVideoOnly()
}

// Mock video providor only has one model
func (p *Provider) ListModels(_ context.Context) (*core.ModelsResponse, error) {
	return &core.ModelsResponse{
		Object: "list",
		Data: []core.Model{
			{ID: modelName, Object: "model"},
		},
	}, nil
}

func (p *Provider) Responses(_ context.Context, _ *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, errVideoOnly()
}
 
func (p *Provider) StreamResponses(_ context.Context, _ *core.ResponsesRequest) (io.ReadCloser, error) {
	return nil, errVideoOnly()
}
 
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, errVideoOnly()
}
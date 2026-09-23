// Wire types
package core

// Request body for creating a video generation request
type VideoRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	Seconds        string `json:"seconds"`
	Size           string `json:"size"`
	InputReference string `json:"input_reference,omitempty"`
}

// Rrror in the response body for a failed video generation request
type VideoError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Video generation response body returned by the provider
type VideoResponse struct {
	ID          string      `json:"id"`
	Object      string      `json:"object"`
	Model       string      `json:"model"`
	Status      string      `json:"status"`
	Progress    int         `json:"progress"`
	CreatedAt   int64       `json:"created_at"`
	CompletedAt *int64      `json:"completed_at"`
	ExpiresAt   *int64      `json:"expires_at"`
	Seconds     string      `json:"seconds"`
	Size        string      `json:"size"`
	Error       *VideoError `json:"error"`
	Deleted 	*bool 		`json:"deleted,omitempty"`
	
	Provider        string `json:"-"`
	ProviderVideoID string `json:"-"`
}

// Response body returned by the provider for a health check
type VideoHealthResponse struct {
	OK         bool `json:"ok"`
	ActiveJobs int  `json:"active_jobs"`
	Capacity   int  `json:"capacity"`
}
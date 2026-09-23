// Orchestrator that creates and tracks native async video generation jobs
// Implements background polling, manages storage lifecycles
package gateway
 
import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
 
	"github.com/google/uuid"
 
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/video"
)

// Video metadata for bookkeeping
type VideoMeta struct {
	RequestID string
	UserPath  string
	SessionID string
}

// Constructor config
// Which provider (router) to use, VideoStore for persistence, 
// backend type default, where to store content, background polling vars
type VideoConfig struct {
	Provider            core.RoutableProvider
	VideoStore          videostore.Store
	DefaultProviderType string
	ContentDir          string
	// How often background poller checks backend for job status
	PollInterval time.Duration
	// Bounds how long background poller keeps polling unfinished/failed jobs
	PollTimeout time.Duration
}

const (
	// How often to poll
	defaultPollInterval = 3 * time.Second
	// How long to keep polling before giving up on job
	defaultPollTimeout = 15 * time.Minute
	// How many consecutive failures kill poller
	maxConsecutivePollErrs = 10
)

// Manages video generation jobs, including background polling and local content caching.
type VideoOrchestrator struct {
	provider             core.RoutableProvider
	videoStore           videostore.Store
	videoFiles           *videoFileStore
	defaultProviderType  string
	pollInterval         time.Duration
	pollTimeout          time.Duration
}

// Constructor for VideoOrchestrator
func NewVideoOrchestrator(cfg VideoConfig) *VideoOrchestrator {
	pollInterval := cfg.PollInterval
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	pollTimeout := cfg.PollTimeout
	if pollTimeout <= 0 {
		pollTimeout = defaultPollTimeout
	}
	return &VideoOrchestrator{
		provider:            cfg.Provider,
		videoStore:          cfg.VideoStore,
		videoFiles:          newVideoFileStore(cfg.ContentDir),
		defaultProviderType: cfg.DefaultProviderType,
		pollInterval:        pollInterval,
		pollTimeout:         pollTimeout,
	}
}

// Type-asserts provider to NativeVideoRoutableProvider
func (o *VideoOrchestrator) videoRouter() (core.NativeVideoRoutableProvider, error) {
	nativeRouter, ok := o.provider.(core.NativeVideoRoutableProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("video routing is not supported by the current provider router", nil)
	}
	return nativeRouter, nil
}

// Create new video job, persist it, and start background polling for completion
func (o *VideoOrchestrator) Create(ctx context.Context, req *core.VideoRequest, meta VideoMeta) (*core.VideoResponse, error) {
	// validate request
	if req == nil {
		return nil, core.NewInvalidRequestError("video request is required", nil)
	}
	// Check that default provider type is configured
	providerType := o.defaultProviderType
	if providerType == "" {
		return nil, core.NewInvalidRequestError("no video backend configured", nil)
	}
	nativeRouter, err := o.videoRouter()
	if err != nil {
		return nil, err
	}
	// Call backend's CreateVideo
	upstream, err := nativeRouter.CreateVideo(ctx, providerType, req)
	if err != nil {
		return nil, err
	}
	// Validate backend response
	if upstream == nil {
		return nil, core.NewProviderError(providerType, http.StatusBadGateway, "provider returned empty video response", nil)
	}
	providerVideoID := upstream.ID
	if providerVideoID == "" {
		return nil, core.NewProviderError(providerType, http.StatusBadGateway, "provider response missing video id", nil)
	}
 
	resp := *upstream
	resp.Provider = providerType
	resp.ProviderVideoID = providerVideoID
	// Assign a new gateway ID for this job that clients will see
	resp.ID = "video_" + uuid.NewString()
	stored := &videostore.StoredVideo{
		Video:           &resp,
		ProviderType:    providerType,
		ProviderVideoID: providerVideoID,
		RequestID:       meta.RequestID,
		UserPath:        meta.UserPath,
		SessionID:       meta.SessionID,
	}
	// Persist mapping of gateway ID to provider ID to metadata
	if err := o.videoStore.Create(ctx, stored); err != nil {
		return nil, core.NewProviderError("video_store", http.StatusInternalServerError, "failed to persist video job", err)
	}
 
	// Poll until job reaches terminal state or poll timeout
	go o.pollUntilTerminal(resp.ID)
	return &resp, nil
}

// Ticks every pollInterval and polls backend for jub status
func (o *VideoOrchestrator) pollUntilTerminal(gatewayID string) {
	// Use separate context so poller isn't canceled if the original request's context is canceled
	ctx, cancel := context.WithTimeout(context.Background(), o.pollTimeout)
	defer cancel()
	// Poll every o.pollInterval until job reaches terminal state or timeout
	ticker := time.NewTicker(o.pollInterval)
	defer ticker.Stop()
 
	consecutiveErrs := 0
	for {
		select {
			// Stop polling if context is canceled or timeout reached
			case <-ctx.Done():
				slog.Warn("video poll timed out before job reached a terminal state", "id", gatewayID)
				return
			case <-ticker.C:
				// Poll backend for job status
				done, err := o.pollOnce(ctx, gatewayID)
				if err != nil {
					consecutiveErrs++
					slog.Warn("video poll attempt failed",
						"id", gatewayID, "error", err, "consecutive_failures", consecutiveErrs)
					if consecutiveErrs >= maxConsecutivePollErrs {
						slog.Warn("video poll giving up after repeated failures", "id", gatewayID)
						return
					}
					continue
				}
				consecutiveErrs = 0
				if done {
					return
				}
		}
	}
}

// One pollng tick to fetch stored job
func (o *VideoOrchestrator) pollOnce(ctx context.Context, gatewayID string) (done bool, err error) {
	// Fetch stored job from videoStore
	stored, err := o.videoStore.Get(ctx, gatewayID)
	// Job no longer tracked or job already terminal, stop polling
	if err != nil {
		return true, nil
	}
	if stored.Video.Status == "completed" || stored.Video.Status == "failed" {
		return true, nil
	}
 
	// If not stored or not terminal, fetch status from backend and update store
	nativeRouter, err := o.videoRouter()
	if err != nil {
		return true, err
	}
	upstream, err := nativeRouter.GetVideo(ctx, stored.ProviderType, stored.ProviderVideoID)
	if err != nil {
		// Transient — the mockvideo client already retries a 503 internally,
		// so anything still surfacing here is worth backing off on rather
		// than aborting the job outright.
		return false, err
	}
 
	// Update the stored job with the latest status from the backend
	updated := *upstream
	updated.Provider = stored.ProviderType
	updated.ProviderVideoID = stored.ProviderVideoID
	updated.ID = gatewayID
	stored.Video = &updated
	if err := o.videoStore.Update(ctx, stored); err != nil {
		return false, err
	}
	// Job is not terminal, keep polling
	if updated.Status != "completed" && updated.Status != "failed" {
		return false, nil
	}
	// Job is terminal, cache content if completed
	if updated.Status == "completed" {
		if err := o.cacheContent(ctx, gatewayID, stored); err != nil {
			slog.Warn("failed to cache video content after completion", "id", gatewayID, "error", err)
		}
	}
	return true, nil
}

const contentTTL = time.Hour
// Helper to cache content locally after job completion
func (o *VideoOrchestrator) cacheContent(ctx context.Context, gatewayID string, stored *videostore.StoredVideo) error {
	// Check if content already cached
	if o.videoFiles.Exists(gatewayID) {
		return nil
	}
	// Fetch content from backend
	nativeRouter, err := o.videoRouter()
	if err != nil {
		return err
	}
	upstream, err := nativeRouter.GetVideoContent(ctx, stored.ProviderType, stored.ProviderVideoID)
	if err != nil {
		return err
	}
	if err := o.videoFiles.Save(gatewayID, upstream); err != nil {
		return err
	}

	// File is on disk now, delete it in an hour
	time.AfterFunc(contentTTL, func() { o.expire(gatewayID) })
	return nil
}

// Runs one hour after a video was cached
func (o *VideoOrchestrator) expire(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	o.videoFiles.Delete(id)
	if _, err := o.Delete(ctx, id); err != nil {
		slog.Warn("failed to expire video", "id", id, "error", err)
	}
}

// Return's job's current status
func (o *VideoOrchestrator) Get(ctx context.Context, id string) (*core.VideoResponse, error) {
	// Fetch stored job from videoStore
	stored, err := o.videoStore.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if stored.Video.Status == "completed" || stored.Video.Status == "failed" {
		return stored.Video, nil
	}
 
	// Check backend for latest status 
	nativeRouter, err := o.videoRouter()
	if err != nil {
		return nil, err
	}
	upstream, err := nativeRouter.GetVideo(ctx, stored.ProviderType, stored.ProviderVideoID)
	if err != nil {
		return nil, err
	}
	// Update stored job with latest status from backend
	updated := *upstream
	updated.Provider = stored.ProviderType
	updated.ProviderVideoID = stored.ProviderVideoID
	updated.ID = id // caller's gateway ID, not the backend's own ID
	stored.Video = &updated
	if err := o.videoStore.Update(ctx, stored); err != nil {
		return nil, core.NewProviderError("video_store", http.StatusInternalServerError, "failed to update video job", err)
	}
	// If job is completed, cache content locally
	if updated.Status == "completed" {
		if err := o.cacheContent(ctx, id, stored); err != nil {
			slog.Warn("failed to cache video content after completion", "id", id, "error", err)
		}
	}
 
	return &updated, nil
}

// Stream mp4 content for a completed job
func (o *VideoOrchestrator) GetContent(ctx context.Context, id string) (io.ReadCloser, error) {
	// Fetch stored job from videoStore
	stored, err := o.videoStore.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// If job is not completed, return error
	if stored.Video.Status != "completed" {
		return nil, core.NewInvalidRequestError(fmt.Sprintf("video %s is %s", id, stored.Video.Status), nil)
	}
	// If content already cached, return it
	if o.videoFiles.Exists(id) {
		return o.videoFiles.Open(id)
	}
	// If not cached, fetch from backend and cache it
	if err := o.cacheContent(ctx, id, stored); err != nil {
		return nil, err
	}
	return o.videoFiles.Open(id)
}

// Delete video job and its cached content
func (o *VideoOrchestrator) Delete(ctx context.Context, id string) (*core.VideoResponse, error) {
	// Retrieve stored job from videoStore
	stored, err := o.videoStore.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// Delete job from backend and remove from videoStore and videoFiles
	nativeRouter, err := o.videoRouter()
	if err != nil {
		return nil, err
	}
	if _, err := nativeRouter.DeleteVideo(ctx, stored.ProviderType, stored.ProviderVideoID); err != nil {
		return nil, err
	}
	if err := o.videoStore.Delete(ctx, id); err != nil {
		return nil, core.NewProviderError("video_store", http.StatusInternalServerError, "failed to remove video job", err)
	}
	if err := o.videoFiles.Delete(id); err != nil {
		return nil, core.NewProviderError("video_store", http.StatusInternalServerError, "failed to remove stored video content", err)
	}
 
	deleted := true
	return &core.VideoResponse{ID: id, Object: "video", Deleted: &deleted}, nil
}
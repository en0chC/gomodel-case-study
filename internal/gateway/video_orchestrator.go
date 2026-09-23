// Goes in internal/gateway/video_orchestrator.go, next to batch_orchestrator.go.
package gateway
 
import (
	"context"
	"fmt"
	"io"
	"net/http"
 
	"github.com/google/uuid"
 
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/video"
)

// VideoMeta carries request-scoped metadata into Create
type VideoMeta struct {
	RequestID string
	UserPath  string
	SessionID string
}

// VideoConfig configures a VideoOrchestrator
type VideoConfig struct {
	// Provider mirrors BatchConfig.Provider — Handler.provider's real type
	// (core.RoutableProvider), asserted down to core.NativeVideoRoutableProvider
	// at call time, same pattern BatchOrchestrator.Create uses for batch.
	Provider   core.RoutableProvider
	VideoStore videostore.Store
	DefaultProviderType string
	ContentDir string
}

// VideoOrchestrator creates and tracks native async video generation jobs,
// mirroring BatchOrchestrator's role and shape.
type VideoOrchestrator struct {
	provider             core.RoutableProvider
	videoStore           videostore.Store
	videoFiles           *videoFileStore
	defaultProviderType  string
}

// NewVideoOrchestrator creates a new VideoOrchestrator.
func NewVideoOrchestrator(cfg VideoConfig) *VideoOrchestrator {
	return &VideoOrchestrator{
		provider:            cfg.Provider,
		videoStore:          cfg.VideoStore,
		videoFiles:          newVideoFileStore(cfg.ContentDir),
		defaultProviderType: cfg.DefaultProviderType,
	}
}

// videoRouter asserts provider down to core.NativeVideoRoutableProvider,
// mirroring the inline check BatchOrchestrator.Create does for
// core.NativeBatchRoutableProvider (there: "batch routing is not supported
// by the current provider router").
func (o *VideoOrchestrator) videoRouter() (core.NativeVideoRoutableProvider, error) {
	nativeRouter, ok := o.provider.(core.NativeVideoRoutableProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("video routing is not supported by the current provider router", nil)
	}
	return nativeRouter, nil
}

// Create submits a new video generation job and persists the gateway ID <->
// backend ID mapping, mirroring BatchOrchestrator.Create's shape (minted ID,
// stamp Provider/ProviderVideoID, persist) without the workflow/budget/
// request-preparer machinery batch also does — not needed yet for a single
// mock backend, but the same pattern to extend later if video needs budget
// enforcement or per-workflow policy the way batch does.
func (o *VideoOrchestrator) Create(ctx context.Context, req *core.VideoRequest, meta VideoMeta) (*core.VideoResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("video request is required", nil)
	}
 
	providerType := o.defaultProviderType
	if providerType == "" {
		return nil, core.NewInvalidRequestError("no video backend configured", nil)
	}
 
	nativeRouter, err := o.videoRouter()
	if err != nil {
		return nil, err
	}
 
	upstream, err := nativeRouter.CreateVideo(ctx, providerType, req)
	if err != nil {
		return nil, err
	}
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
	resp.ID = "video_" + uuid.NewString() // the gateway ID callers actually use going forward
 
	stored := &videostore.StoredVideo{
		Video:           &resp,
		ProviderType:    providerType,
		ProviderVideoID: providerVideoID,
		RequestID:       meta.RequestID,
		UserPath:        meta.UserPath,
		SessionID:       meta.SessionID,
	}
	if err := o.videoStore.Create(ctx, stored); err != nil {
		return nil, core.NewProviderError("video_store", http.StatusInternalServerError, "failed to persist video job", err)
	}
 
	return &resp, nil
}

// Get returns a job's current status. Terminal jobs (completed/failed) are
// served straight from the store, per the Flows doc's own diagram for this
// endpoint — the backend forgets a job MOCK_RETENTION_SECONDS after it
// finishes, so re-asking after that window would wrongly 404 a job GoModel
// still knows finished. Non-terminal jobs are checked live and the store
// updated with whatever the backend reports.
func (o *VideoOrchestrator) Get(ctx context.Context, id string) (*core.VideoResponse, error) {
	stored, err := o.videoStore.Get(ctx, id)
	if err != nil {
		return nil, err
	}
 
	if stored.Video.Status == "completed" || stored.Video.Status == "failed" {
		return stored.Video, nil
	}
 
	nativeRouter, err := o.videoRouter()
	if err != nil {
		return nil, err
	}
 
	upstream, err := nativeRouter.GetVideo(ctx, stored.ProviderType, stored.ProviderVideoID)
	if err != nil {
		return nil, err
	}
 
	updated := *upstream
	updated.Provider = stored.ProviderType
	updated.ProviderVideoID = stored.ProviderVideoID
	updated.ID = id // caller's gateway ID, not the backend's own ID
 
	stored.Video = &updated
	if err := o.videoStore.Update(ctx, stored); err != nil {
		return nil, core.NewProviderError("video_store", http.StatusInternalServerError, "failed to update video job", err)
	}
 
	return &updated, nil
}

// GetContent streams the raw mp4 bytes for a completed job.
//
// Local storage is checked first — that's what lets content survive past the
// backend's own MOCK_RETENTION_SECONDS window; the backend forgets its copy,
// GoModel doesn't have to. On a first request (nothing local yet), the bytes
// are pulled from the backend once, saved locally, and served from that
// fresh local copy — so every request after the first for the same video
// never touches the backend again, retention window or not.
func (o *VideoOrchestrator) GetContent(ctx context.Context, id string) (io.ReadCloser, error) {
	stored, err := o.videoStore.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if stored.Video.Status != "completed" {
		return nil, core.NewInvalidRequestError(fmt.Sprintf("video %s is %s", id, stored.Video.Status), nil)
	}
 
	if o.videoFiles.Exists(id) {
		return o.videoFiles.Open(id)
	}
 
	nativeRouter, err := o.videoRouter()
	if err != nil {
		return nil, err
	}
	upstream, err := nativeRouter.GetVideoContent(ctx, stored.ProviderType, stored.ProviderVideoID)
	if err != nil {
		return nil, err
	}
	if err := o.videoFiles.Save(id, upstream); err != nil { // Save closes upstream itself, success or failure
		return nil, core.NewProviderError("video_store", http.StatusInternalServerError, "failed to store video content", err)
	}
	return o.videoFiles.Open(id)
}

// Delete cancels/deletes a job on its backend and removes GoModel's own
// record of it.
func (o *VideoOrchestrator) Delete(ctx context.Context, id string) (*core.VideoResponse, error) {
	stored, err := o.videoStore.Get(ctx, id)
	if err != nil {
		return nil, err
	}
 
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
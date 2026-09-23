// Bridge between incoming HTTP requests and the VideoOrchestrator
package server

import (
	"net/http"
	"encoding/json"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/video"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
)

// Holds provider router, video store, and orchestrator
type nativeVideoService struct {
	provider   core.RoutableProvider
	videoStore videostore.Store
	orchestrator *gateway.VideoOrchestrator
}
 
// COnstructs a VideoOrchestrator if not already constructed, and returns it.
func (s *nativeVideoService) video() *gateway.VideoOrchestrator {
	if s.orchestrator != nil {
		return s.orchestrator
	}
	s.orchestrator = gateway.NewVideoOrchestrator(gateway.VideoConfig{
		Provider:   s.provider,
		VideoStore: s.videoStore,
		// Only one video backend exists right now, so hardcode it for now
		DefaultProviderType: "mockvideo",
	})
	return s.orchestrator
}
 
// Handles POST /v1/videos, delegating to the VideoOrchestrator's Create method
func (s *nativeVideoService) CreateVideo(c *echo.Context) error {
	var req core.VideoRequest
	// Decode JSON body into VideoRequest struct
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	// Delegate to VideoOrchestrator's Create method, passing request context and metadata
	ctx, requestID := requestContextWithRequestID(c.Request())
	resp, err := s.video().Create(ctx, &req, gateway.VideoMeta{
		RequestID: requestID,
		UserPath:  core.UserPathFromContext(ctx),
		SessionID: core.SessionIDFromContext(ctx),
	})
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}
 
// Handles GET /v1/videos/{id}, delegating to the VideoOrchestrator's Get method
func (s *nativeVideoService) GetVideo(c *echo.Context) error {
	// Retrieve video job status/progress from VideoOrchestrator
	ctx, _ := requestContextWithRequestID(c.Request())
	resp, err := s.video().Get(ctx, c.Param("id"))
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}
 
// Handles GET /v1/videos/{id}/content, delegating to the VideoOrchestrator's GetContent method
func (s *nativeVideoService) GetVideoContent(c *echo.Context) error {
	// Retrieve video content from VideoOrchestrator
	ctx, _ := requestContextWithRequestID(c.Request())
	content, err := s.video().GetContent(ctx, c.Param("id"))
	if err != nil {
		return handleError(c, err)
	}
	defer content.Close()
	return c.Stream(http.StatusOK, "video/mp4", content)
}
 
// Handles DELETE /v1/videos/{id}, delegating to the VideoOrchestrator's Delete method
func (s *nativeVideoService) DeleteVideo(c *echo.Context) error {
	// Delete video job and its cached content from VideoOrchestrator
	ctx, _ := requestContextWithRequestID(c.Request())
	resp, err := s.video().Delete(ctx, c.Param("id"))
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}
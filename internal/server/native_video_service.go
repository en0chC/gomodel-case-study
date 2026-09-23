package server

import (
	"net/http"
	"encoding/json"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/video"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
)

type nativeVideoService struct {
	provider   core.RoutableProvider
	videoStore videostore.Store
 
	orchestrator *gateway.VideoOrchestrator
}
 
func (s *nativeVideoService) video() *gateway.VideoOrchestrator {
	if s.orchestrator != nil {
		return s.orchestrator
	}
	s.orchestrator = gateway.NewVideoOrchestrator(gateway.VideoConfig{
		Provider:   s.provider,
		VideoStore: s.videoStore,
		// Only one video backend exists right now, so there's nothing to
		// actually select between yet — see the comment on this field in
		// video_orchestrator.go.
		DefaultProviderType: "mockvideo",
	})
	return s.orchestrator
}
 
func (s *nativeVideoService) CreateVideo(c *echo.Context) error {
	// Plain stdlib decode rather than batch's canonicalJSONRequestFromSemantics
	// + core.DecodeBatchRequest: that pairing exists specifically to translate
	// OpenAI-compat field variations (input_file_id, etc.) into GoModel's
	// canonical shape. The mock video request body has no such variants to
	// reconcile — CreateVideoRequest in the mock's own app.py is already a
	// single flat shape — so there's nothing for a Decode helper to do here
	// that json.Decode doesn't already do, and this avoids guessing at a
	// generic helper's signature we haven't been shown.
	var req core.VideoRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
 
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
 
func (s *nativeVideoService) GetVideo(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())
	resp, err := s.video().Get(ctx, c.Param("id"))
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}
 
func (s *nativeVideoService) GetVideoContent(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())
	content, err := s.video().GetContent(ctx, c.Param("id"))
	if err != nil {
		return handleError(c, err)
	}
	defer content.Close()
	// UNVERIFIED: echo.Context's actual streaming response method. Standard
	// Echo has c.Stream(status, contentType, io.Reader) — used here assuming
	// this fork matches; the mp4 content-type and status are the only parts
	// I'm confident about.
	return c.Stream(http.StatusOK, "video/mp4", content)
}
 
func (s *nativeVideoService) DeleteVideo(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())
	resp, err := s.video().Delete(ctx, c.Param("id"))
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wesm/agentsview/internal/config"
	"github.com/wesm/agentsview/internal/db"
	"github.com/wesm/agentsview/internal/live"
	"github.com/wesm/agentsview/internal/sessionwatch"
)

// imageContentTypeExts maps accepted image MIME types to file extensions.
// Using a hardcoded map avoids platform-specific results from mime.ExtensionsByType.
var imageContentTypeExts = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

type liveSendRequest struct {
	Content string `json:"content"`
}

type liveApprovalRequest struct {
	Decision string `json:"decision"`
}

type liveReplyRequest struct {
	Answers map[string][]string `json:"answers"`
}

func newDefaultLiveManager(cfg config.Config, database db.Store) *live.Manager {
	codexServer := live.NewCodexAppServer()
	// Enable when codex is on PATH and the DB is writable.
	// cfg.LiveChatEnabled can explicitly force it on even when codex is
	// not on PATH (e.g. for testing), but the default auto-detection
	// requires no config change from the user.
	enabled := !database.ReadOnly() && (cfg.LiveChatEnabled || live.CodexAppServerSupported())
	return live.NewManager(
		enabled,
		func(ctx context.Context, id string) (*db.Session, error) {
			return database.GetSessionFull(ctx, id)
		},
		map[string]live.AdapterFactory{
			"codex": func(session *db.Session) (live.Adapter, error) {
				return live.NewCodexAdapter(session, codexServer)
			},
		},
	)
}

func (s *Server) handleGetLiveState(w http.ResponseWriter, r *http.Request) {
	state, err := s.liveManager.State(r.Context(), r.PathValue("id"))
	if err != nil {
		if s.handleLiveError(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleConnectLive(w http.ResponseWriter, r *http.Request) {
	_, state, err := s.liveManager.Ensure(r.Context(), r.PathValue("id"))
	if err != nil {
		if s.handleLiveError(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleSendLiveMessage(w http.ResponseWriter, r *http.Request) {
	input, err := s.parseLiveTurnInput(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(input.Text) == "" && len(input.LocalImagePaths) == 0 {
		writeError(w, http.StatusBadRequest, "content or image is required")
		return
	}

	state, err := s.liveManager.SendInput(r.Context(), r.PathValue("id"), input)
	// Clean up uploaded image temp files after the send is accepted.
	for _, path := range input.LocalImagePaths {
		uploadDir := filepath.Join(s.cfg.DataDir, "live-images")
		if strings.HasPrefix(filepath.Clean(path), filepath.Clean(uploadDir)) {
			_ = os.Remove(path)
		}
	}
	if err != nil {
		if s.handleLiveError(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleStopLiveTurn(w http.ResponseWriter, r *http.Request) {
	state, err := s.liveManager.Stop(r.Context(), r.PathValue("id"))
	if err != nil {
		if s.handleLiveError(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleApproveLiveRequest(w http.ResponseWriter, r *http.Request) {
	var req liveApprovalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Decision) == "" {
		writeError(w, http.StatusBadRequest, "decision is required")
		return
	}

	requestID := r.PathValue("requestID")
	state, err := s.liveManager.State(r.Context(), r.PathValue("id"))
	if err != nil {
		if s.handleLiveError(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	pending, ok := findPendingRequest(state.PendingRequests, requestID)
	if !ok {
		writeError(w, http.StatusNotFound, "pending request not found")
		return
	}
	result, supported := liveApprovalResult(pending.Kind, req.Decision)
	if !supported {
		writeError(w, http.StatusNotImplemented, "approval type not supported")
		return
	}
	state, err = s.liveManager.Respond(r.Context(), r.PathValue("id"), requestID, result)
	if err != nil {
		if s.handleLiveError(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleReplyLiveRequest(w http.ResponseWriter, r *http.Request) {
	var req liveReplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.Answers) == 0 {
		writeError(w, http.StatusBadRequest, "answers are required")
		return
	}

	// Build the payload Codex expects for user-input replies.
	answersMap := map[string]any{}
	for key, values := range req.Answers {
		answersMap[key] = map[string]any{"answers": values}
	}
	payload := map[string]any{"answers": answersMap}

	state, err := s.liveManager.Respond(
		r.Context(), r.PathValue("id"), r.PathValue("requestID"), payload,
	)
	if err != nil {
		if s.handleLiveError(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleWatchLive(w http.ResponseWriter, r *http.Request) {
	events, unsub, err := s.liveManager.Subscribe(r.Context(), r.PathValue("id"))
	if err != nil {
		if s.handleLiveError(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer unsub()

	stream, err := NewSSEStream(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	heartbeat := time.NewTicker(sessionwatch.PollInterval * sessionwatch.HeartbeatTicks)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			stream.SendJSON(ev.Type, ev.Data)
		case <-heartbeat.C:
			stream.Send("heartbeat", time.Now().UTC().Format(time.RFC3339))
		}
	}
}

func (s *Server) parseLiveTurnInput(r *http.Request) (live.TurnInput, error) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		return s.parseMultipartLiveTurnInput(r)
	}
	var req liveSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return live.TurnInput{}, errors.New("invalid JSON body")
	}
	return live.TurnInput{Text: req.Content}, nil
}

func (s *Server) parseMultipartLiveTurnInput(r *http.Request) (live.TurnInput, error) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return live.TurnInput{}, errors.New("invalid multipart form")
	}
	input := live.TurnInput{Text: r.FormValue("content")}
	for _, header := range r.MultipartForm.File["images"] {
		path, err := s.saveLiveImageUpload(header)
		if err != nil {
			return live.TurnInput{}, err
		}
		input.LocalImagePaths = append(input.LocalImagePaths, path)
	}
	return input, nil
}

func (s *Server) saveLiveImageUpload(header *multipart.FileHeader) (string, error) {
	if header == nil {
		return "", errors.New("missing image upload")
	}
	ct := header.Header.Get("Content-Type")
	if ct == "" {
		ct = mime.TypeByExtension(filepath.Ext(header.Filename))
	}
	// Strip parameters (e.g. "image/jpeg; charset=utf-8" → "image/jpeg")
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	ext, ok := imageContentTypeExts[ct]
	if !ok {
		return "", fmt.Errorf("unsupported image type %q", ct)
	}

	uploadDir := filepath.Join(s.cfg.DataDir, "live-images")
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		return "", errors.New("failed to prepare image upload directory")
	}

	file, err := header.Open()
	if err != nil {
		return "", errors.New("failed to read image upload")
	}
	defer file.Close()

	tmp, err := os.CreateTemp(uploadDir, "live-image-*"+ext)
	if err != nil {
		return "", errors.New("failed to stage image upload")
	}
	defer tmp.Close()

	if _, err := io.Copy(tmp, file); err != nil {
		_ = os.Remove(tmp.Name())
		return "", errors.New("failed to save image upload")
	}
	return tmp.Name(), nil
}

func (s *Server) handleLiveError(w http.ResponseWriter, err error) bool {
	if handleContextError(w, err) {
		return true
	}
	switch {
	case errors.Is(err, live.ErrNotFound):
		writeError(w, http.StatusNotFound, "session not found")
	case errors.Is(err, live.ErrDisabled):
		writeError(w, http.StatusNotImplemented, "live chat not available in this mode")
	case errors.Is(err, live.ErrUnsupported):
		writeError(w, http.StatusBadRequest, "agent does not support live chat")
	case errors.Is(err, live.ErrBlocked):
		writeError(w, http.StatusConflict, "live session is blocked by an interactive prompt")
	default:
		return false
	}
	return true
}

func findPendingRequest(reqs []live.PendingRequest, id string) (live.PendingRequest, bool) {
	for _, req := range reqs {
		if req.ID == id {
			return req, true
		}
	}
	return live.PendingRequest{}, false
}

func liveApprovalResult(kind live.PendingRequestKind, decision string) (map[string]any, bool) {
	switch kind {
	case live.PendingRequestCommand, live.PendingRequestFileChange:
		return map[string]any{"decision": decision}, true
	default:
		return nil, false
	}
}

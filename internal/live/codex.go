package live

import (
	"context"
	"errors"
	"strings"

	"github.com/wesm/agentsview/internal/db"
)

// NewCodexAdapter creates a live adapter for a Codex session.
// server must be a non-nil CodexAppServer instance.
func NewCodexAdapter(session *db.Session, server *CodexAppServer) (Adapter, error) {
	if session == nil {
		return nil, ErrNotFound
	}
	if server == nil {
		return nil, errors.New("codex app server is required")
	}
	rawID := strings.TrimPrefix(session.ID, "codex:")
	if rawID == "" {
		return nil, errors.New("codex session id is empty")
	}
	return &codexAdapter{rawID: rawID, cwd: session.Cwd, server: server}, nil
}

type codexAdapter struct {
	rawID  string
	cwd    string
	server *CodexAppServer
}

func (a *codexAdapter) Start(ctx context.Context) (Process, error) {
	return a.server.ResumeThread(ctx, a.rawID, a.cwd)
}

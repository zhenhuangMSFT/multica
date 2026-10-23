package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/chatcontext"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type ChatSourceContextResponse struct {
	ChatSessionID   string `json:"chat_session_id"`
	SourceTaskID    string `json:"source_task_id"`
	SourceMessageID string `json:"source_message_id,omitempty"`
	OwnerID         string `json:"owner_id"`
	DelegationHops  int    `json:"delegation_hops"`
	// Provenance is intentionally never an approval or a send capability.
	AuthorizationGranted bool `json:"authorization_granted"`
}

func (h *Handler) GetChatSourceContext(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, http.StatusForbidden, "source chat context requires a task token")
		return
	}
	taskID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Task-ID"), "task id")
	if !ok {
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Agent-ID"), "agent id")
	if !ok {
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace id")
	if !ok {
		return
	}
	var messageID pgtype.UUID
	if id := r.URL.Query().Get("source_message_id"); id != "" {
		messageID, ok = parseUUIDOrBadRequest(w, id, "source message id")
		if !ok {
			return
		}
	}
	source, err := chatcontext.Resolve(r.Context(), h.Queries, taskID, agentID, workspaceID, messageID)
	if err != nil {
		switch {
		case errors.Is(err, chatcontext.ErrUnavailable), errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusNotFound, "source chat context is not available")
		case errors.Is(err, chatcontext.ErrInvalidChain):
			writeError(w, http.StatusConflict, "invalid source task chain")
		default:
			slog.Error("source chat context read failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to read source chat context")
		}
		return
	}
	ownerID := uuidToString(source.Session.CreatorID)
	wsID := uuidToString(workspaceID)
	if _, err := h.getWorkspaceMember(r.Context(), ownerID, wsID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "source chat context is not available")
		} else {
			slog.Error("source chat membership read failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to read source chat membership")
		}
		return
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID: source.Session.AgentID, WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "source chat context is not available")
		} else {
			slog.Error("source chat agent read failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to read source chat agent")
		}
		return
	}
	if !h.canAccessPrivateAgent(r.Context(), agent, "member", ownerID, wsID) {
		writeError(w, http.StatusNotFound, "source chat context is not available")
		return
	}
	// IM bindings can represent a channel rather than a creator-only direct
	// conversation. Do not expose them as direct-chat routing destinations.
	if _, err := h.Queries.GetChannelChatSessionBindingBySessionAny(r.Context(), source.Session.ID); err == nil {
		writeError(w, http.StatusNotFound, "source chat context is not available")
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		slog.Error("source chat binding read failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to read source chat binding")
		return
	}
	response := ChatSourceContextResponse{
		ChatSessionID:  uuidToString(source.Session.ID),
		SourceTaskID:   uuidToString(source.Task.ID),
		OwnerID:        ownerID,
		DelegationHops: source.Hops,
	}
	if source.Message != nil {
		response.SourceMessageID = uuidToString(source.Message.ID)
	}
	writeJSON(w, http.StatusOK, response)
}

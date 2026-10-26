package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type taskStartupRequest struct {
	DispatchedAt string                    `json:"dispatched_at"`
	Phase        protocol.TaskStartupPhase `json:"phase"`
}

func (h *Handler) ReportTaskStartup(w http.ResponseWriter, r *http.Request) {
	task, workspaceID, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	var req taskStartupRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil || !req.Phase.Valid() {
		writeError(w, http.StatusBadRequest, "invalid startup phase")
		return
	}
	claim, err := time.Parse(time.RFC3339Nano, req.DispatchedAt)
	if err != nil || claim.Nanosecond()%1000 != 0 {
		writeError(w, http.StatusBadRequest, "invalid dispatched_at")
		return
	}
	sample, err := json.Marshal(req.Phase)
	if err != nil {
		slog.Error("encode task startup phase failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to record startup phase")
		return
	}
	rows, err := h.Queries.RecordTaskStartupPhase(r.Context(), db.RecordTaskStartupPhaseParams{
		ID: task.ID, RuntimeID: task.RuntimeID,
		DispatchedAt: pgtype.Timestamptz{Time: claim, Valid: true},
		Claim:        claim.UTC().Format(time.RFC3339Nano), Phase: req.Phase.Phase, Sample: sample,
	})
	if err != nil {
		slog.Warn("record task startup phase failed", "task_id", uuidToString(task.ID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to record startup phase")
		return
	}
	if rows == 0 {
		writeError(w, http.StatusConflict, "task claim is stale")
		return
	}
	h.publishTask(protocol.EventTaskStartup, workspaceID, "system", "", uuidToString(task.ID),
		map[string]string{"task_id": uuidToString(task.ID), "issue_id": uuidToString(task.IssueID), "agent_id": uuidToString(task.AgentID)})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

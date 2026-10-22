package daemoncontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type Directory interface {
	GetAgentRuntime(context.Context, pgtype.UUID) (db.AgentRuntime, error)
	ListDaemonMaintenanceRegistrations(context.Context, pgtype.Text) ([]db.ListDaemonMaintenanceRegistrationsRow, error)
	GetMemberByUserAndWorkspace(context.Context, db.GetMemberByUserAndWorkspaceParams) (db.Member, error)
}

// Control must be mounted behind the backend Auth/DaemonAuth middleware.
// Header guards here are backstops, not an independent authentication scheme.
type Control struct {
	directory Directory
	store     Store
}

func NewControl(directory Directory, store Store) *Control {
	return &Control{directory: directory, store: store}
}

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Warn("maintenance response failed", "error", err)
	}
}

func failure(w http.ResponseWriter, status int, code string) {
	respond(w, status, map[string]string{"error": code})
}

func (c *Control) unavailable(w http.ResponseWriter, err error) {
	slog.Warn("maintenance backend unavailable", "error", err)
	failure(w, http.StatusServiceUnavailable, "maintenance_backend_unavailable")
}

func (c *Control) decode(w http.ResponseWriter, r *http.Request, target any) bool {
	deadline, ok := r.Context().Deadline()
	if !ok {
		c.unavailable(w, errors.New("maintenance request deadline missing"))
		return false
	}
	if err := http.NewResponseController(w).SetReadDeadline(deadline); err != nil {
		c.unavailable(w, fmt.Errorf("maintenance body deadline unavailable: %w", err))
		return false
	}
	if err := protocol.DecodeMaintenanceJSON(r.Body, target); err != nil {
		failure(w, http.StatusBadRequest, "invalid_maintenance_message")
		return false
	}
	return true
}

func (c *Control) human(w http.ResponseWriter, r *http.Request) (string, bool) {
	source := r.Header.Get("X-Actor-Source")
	if source != "" || r.Header.Get("X-Agent-ID") != "" || r.Header.Get("X-Task-ID") != "" {
		failure(w, http.StatusForbidden, "maintenance_requires_human_owner")
		return "", false
	}
	user := r.Header.Get("X-User-ID")
	if _, err := util.ParseUUID(user); err != nil {
		failure(w, http.StatusUnauthorized, "maintenance_requires_authentication")
		return "", false
	}
	return user, true
}

// ownerScope intentionally reads every workspace registration for this daemon.
// Role (including admin) never substitutes for equality with the runtime owner.
func (c *Control) ownerScope(ctx context.Context, daemon, owner string) (string, error) {
	userID, err := util.ParseUUID(owner)
	if err != nil || owner == "" || daemon == "" {
		return "", ErrConflict
	}
	rows, err := c.directory.ListDaemonMaintenanceRegistrations(ctx, pgtype.Text{String: daemon, Valid: true})
	if err != nil {
		return "", err
	}
	if len(rows) == 0 || len(rows) > 512 {
		return "", ErrConflict
	}
	parts := make([]string, 0, len(rows))
	workspaces := make(map[pgtype.UUID]bool)
	for _, row := range rows {
		if !row.DaemonID.Valid || row.DaemonID.String != daemon || !row.ID.Valid || !row.WorkspaceID.Valid ||
			!row.OwnerID.Valid || util.UUIDToString(row.OwnerID) != owner {
			return "", ErrConflict
		}
		parts = append(parts, util.UUIDToString(row.ID)+":"+util.UUIDToString(row.WorkspaceID)+":"+owner)
		workspaces[row.WorkspaceID] = true
	}
	for workspaceID := range workspaces {
		member, err := c.directory.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: userID, WorkspaceID: workspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrConflict
		}
		if err != nil {
			return "", err
		}
		if member.UserID != userID || member.WorkspaceID != workspaceID {
			return "", ErrConflict
		}
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:]), nil
}

func (c *Control) runtime(w http.ResponseWriter, r *http.Request) (db.AgentRuntime, bool) {
	id, err := util.ParseUUID(chi.URLParam(r, "runtimeId"))
	if err != nil {
		failure(w, http.StatusBadRequest, "invalid_runtime_id")
		return db.AgentRuntime{}, false
	}
	rt, err := c.directory.GetAgentRuntime(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		failure(w, http.StatusNotFound, "runtime_not_found")
		return rt, false
	}
	if err != nil {
		c.unavailable(w, err)
		return rt, false
	}
	return rt, true
}

func (c *Control) authorizeOwner(w http.ResponseWriter, r *http.Request, rt db.AgentRuntime, owner string) (string, bool) {
	if !rt.OwnerID.Valid || !rt.DaemonID.Valid || util.UUIDToString(rt.OwnerID) != owner {
		failure(w, http.StatusForbidden, "maintenance_owner_mismatch")
		return "", false
	}
	scope, err := c.ownerScope(r.Context(), rt.DaemonID.String, owner)
	if errors.Is(err, ErrConflict) {
		failure(w, http.StatusForbidden, "maintenance_ownership_unresolved")
		return "", false
	}
	if err != nil {
		c.unavailable(w, err)
		return "", false
	}
	if c.store == nil {
		c.unavailable(w, errors.New("shared maintenance store not configured"))
		return "", false
	}
	return scope, true
}

func (c *Control) Submit(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	owner, ok := c.human(w, r)
	if !ok {
		return
	}
	rt, ok := c.runtime(w, r)
	if !ok {
		return
	}
	scope, ok := c.authorizeOwner(w, r, rt, owner)
	if !ok {
		return
	}
	var command protocol.MaintenanceCommand
	if !c.decode(w, r, &command) {
		return
	}
	if command.Validate(time.Now()) != nil || command.DaemonID != rt.DaemonID.String {
		failure(w, http.StatusBadRequest, "invalid_maintenance_command")
		return
	}
	request, err := c.store.Create(r.Context(), protocol.MaintenanceDelivery{Command: command, OwnerID: owner, Scope: scope})
	if errors.Is(err, ErrConflict) {
		failure(w, http.StatusConflict, "maintenance_command_conflict")
		return
	}
	if err != nil {
		c.unavailable(w, err)
		return
	}
	respond(w, http.StatusAccepted, request)
}

func (c *Control) Get(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	owner, ok := c.human(w, r)
	if !ok {
		return
	}
	rt, ok := c.runtime(w, r)
	if !ok {
		return
	}
	scope, ok := c.authorizeOwner(w, r, rt, owner)
	if !ok {
		return
	}
	id := chi.URLParam(r, "requestId")
	if _, err := util.ParseUUID(id); err != nil {
		failure(w, http.StatusBadRequest, "invalid_request_id")
		return
	}
	request, err := c.store.Get(r.Context(), id)
	if err != nil {
		c.unavailable(w, err)
		return
	}
	if request == nil || request.Delivery.Command.DaemonID != rt.DaemonID.String || request.Delivery.OwnerID != owner {
		failure(w, http.StatusNotFound, "maintenance_request_not_found")
		return
	}
	if request.Delivery.Scope != scope {
		failure(w, http.StatusConflict, "maintenance_scope_changed")
		return
	}
	respond(w, http.StatusOK, request)
}

// Daemon token delivery is scoped to its exact daemon and workspace. The
// supported human PAT/JWT fallback must equal the owner of every registration.
func (c *Control) daemon(w http.ResponseWriter, r *http.Request, rt db.AgentRuntime, owner string) bool {
	switch middleware.DaemonAuthPathFromContext(r.Context()) {
	case middleware.DaemonAuthPathDaemonToken:
		if middleware.DaemonIDFromContext(r.Context()) == rt.DaemonID.String &&
			middleware.DaemonWorkspaceIDFromContext(r.Context()) == util.UUIDToString(rt.WorkspaceID) {
			return true
		}
	case middleware.DaemonAuthPathPAT, middleware.DaemonAuthPathJWT:
		if r.Header.Get("X-User-ID") == owner && r.Header.Get("X-Actor-Source") == "" {
			return true
		}
	}
	failure(w, http.StatusForbidden, "maintenance_daemon_identity_mismatch")
	return false
}

// Claim is POST, not a read-side heartbeat effect. Persisting delivery before
// responding prevents retries from executing a command twice after a lost ack.
func (c *Control) Claim(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	rt, ok := c.runtime(w, r)
	if !ok || !c.daemon(w, r, rt, util.UUIDToString(rt.OwnerID)) {
		return
	}
	_, ok = c.authorizeOwner(w, r, rt, util.UUIDToString(rt.OwnerID))
	if !ok {
		return
	}
	request, err := c.store.Pending(r.Context(), rt.DaemonID.String)
	if err != nil {
		c.unavailable(w, err)
		return
	}
	if request == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	scope, ok := c.authorizeOwner(w, r, rt, request.Delivery.OwnerID)
	if !ok {
		return
	}
	if scope != request.Delivery.Scope || request.Delivery.Command.Validate(time.Now()) != nil {
		failure(w, http.StatusConflict, "maintenance_scope_changed_or_expired")
		return
	}
	delivered, err := c.store.Deliver(r.Context(), request.Delivery)
	if err != nil {
		c.unavailable(w, err)
		return
	}
	if !delivered {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	respond(w, http.StatusOK, request.Delivery)
}

func (c *Control) Report(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	rt, ok := c.runtime(w, r)
	if !ok || !c.daemon(w, r, rt, util.UUIDToString(rt.OwnerID)) {
		return
	}
	var result protocol.MaintenanceResult
	if !c.decode(w, r, &result) {
		return
	}
	scope, ok := c.authorizeOwner(w, r, rt, result.Delivery.OwnerID)
	if !ok {
		return
	}
	command := result.Delivery.Command
	now := time.Now()
	if command.DaemonID != rt.DaemonID.String || command.ID != chi.URLParam(r, "requestId") ||
		result.Delivery.Scope != scope || result.Validate() != nil ||
		result.Status.ObservedAt.Before(command.IssuedAt.Add(-5*time.Second)) || result.Status.ObservedAt.After(now.Add(5*time.Second)) ||
		!now.Before(command.IssuedAt.Add(5*time.Minute)) {
		failure(w, http.StatusBadRequest, "maintenance_result_binding_mismatch")
		return
	}
	if err := c.store.Complete(r.Context(), result); err != nil {
		if errors.Is(err, ErrConflict) {
			failure(w, http.StatusConflict, "maintenance_result_conflict")
		} else {
			c.unavailable(w, fmt.Errorf("maintenance result persistence: %w", err))
		}
		return
	}
	respond(w, http.StatusOK, protocol.MaintenanceAcknowledgment{ID: command.ID, Accepted: true})
}

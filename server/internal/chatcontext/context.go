// Package chatcontext resolves conversation provenance, not permission to send
// messages or consent to perform actions.
package chatcontext

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	ErrUnavailable  = errors.New("source chat context is not available")
	ErrInvalidChain = errors.New("invalid source task chain")
)

const MaxTaskHops = 32

type Reader interface {
	GetAgentTask(context.Context, pgtype.UUID) (db.AgentTaskQueue, error)
	GetAgentInWorkspace(context.Context, db.GetAgentInWorkspaceParams) (db.Agent, error)
	GetChatSessionInWorkspace(context.Context, db.GetChatSessionInWorkspaceParams) (db.ChatSession, error)
	GetChatMessage(context.Context, pgtype.UUID) (db.ChatMessage, error)
}

type Context struct {
	Task    db.AgentTaskQueue
	Session db.ChatSession
	// Message is populated only when the caller supplied and proved an exact
	// source message. A role=user row is NOT proof of a human approval.
	Message *db.ChatMessage
	Hops    int
}

// Resolve follows only server-persisted delegation or automatic-retry edges.
// Manual reruns and parent_task_id are deliberately not provenance grants.
// No transcript content is returned by the HTTP projection of this result.
func Resolve(ctx context.Context, q Reader, taskID, agentID, workspaceID, messageID pgtype.UUID) (Context, error) {
	seen := make(map[pgtype.UUID]bool)
	var owner pgtype.UUID
	for hop := 0; hop < MaxTaskHops; hop++ {
		if !taskID.Valid {
			return Context{}, ErrUnavailable
		}
		if seen[taskID] {
			return Context{}, ErrInvalidChain
		}
		seen[taskID] = true
		task, err := q.GetAgentTask(ctx, taskID)
		if err != nil {
			return Context{}, fmt.Errorf("read source task: %w", err)
		}
		if hop == 0 {
			if task.AgentID != agentID || !task.OriginatorUserID.Valid {
				return Context{}, ErrUnavailable
			}
			if task.Status != "running" && task.Status != "dispatched" {
				return Context{}, ErrUnavailable
			}
			owner = task.OriginatorUserID
		}
		if task.OriginatorUserID != owner {
			return Context{}, ErrUnavailable
		}
		if _, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
			ID: task.AgentID, WorkspaceID: workspaceID,
		}); err != nil {
			return Context{}, fmt.Errorf("read source agent: %w", err)
		}
		if task.ChatSessionID.Valid {
			session, err := q.GetChatSessionInWorkspace(ctx, db.GetChatSessionInWorkspaceParams{
				ID: task.ChatSessionID, WorkspaceID: workspaceID,
			})
			if err != nil {
				return Context{}, fmt.Errorf("read source chat: %w", err)
			}
			if session.CreatorID != owner || session.Status != "active" || session.AgentID != task.AgentID {
				return Context{}, ErrUnavailable
			}
			out := Context{Task: task, Session: session, Hops: hop}
			if messageID.Valid {
				message, err := q.GetChatMessage(ctx, messageID)
				if err != nil {
					return Context{}, fmt.Errorf("read source message: %w", err)
				}
				inputTaskID := task.ID
				if task.ChatInputTaskID.Valid {
					inputTaskID = task.ChatInputTaskID
				}
				if message.ChatSessionID != session.ID || message.TaskID != inputTaskID ||
					message.Role != "user" || message.MessageKind != "message" {
					return Context{}, ErrUnavailable
				}
				out.Message = &message
			}
			return out, nil
		}
		taskID = task.DelegatedFromTaskID
		if !taskID.Valid {
			taskID = task.RetryOfTaskID
		}
	}
	return Context{}, ErrInvalidChain
}

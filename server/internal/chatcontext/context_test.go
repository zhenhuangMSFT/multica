package chatcontext

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type memoryReader struct {
	tasks    map[pgtype.UUID]db.AgentTaskQueue
	agents   map[pgtype.UUID]db.Agent
	sessions map[pgtype.UUID]db.ChatSession
	messages map[pgtype.UUID]db.ChatMessage
	err      error
}

func (r *memoryReader) GetAgentTask(_ context.Context, id pgtype.UUID) (db.AgentTaskQueue, error) {
	if r.err != nil {
		return db.AgentTaskQueue{}, r.err
	}
	if v, ok := r.tasks[id]; ok {
		return v, nil
	}
	return db.AgentTaskQueue{}, pgx.ErrNoRows
}

func (r *memoryReader) GetAgentInWorkspace(_ context.Context, p db.GetAgentInWorkspaceParams) (db.Agent, error) {
	if v, ok := r.agents[p.ID]; ok && v.WorkspaceID == p.WorkspaceID {
		return v, nil
	}
	return db.Agent{}, pgx.ErrNoRows
}

func (r *memoryReader) GetChatSessionInWorkspace(_ context.Context, p db.GetChatSessionInWorkspaceParams) (db.ChatSession, error) {
	if v, ok := r.sessions[p.ID]; ok && v.WorkspaceID == p.WorkspaceID {
		return v, nil
	}
	return db.ChatSession{}, pgx.ErrNoRows
}

func (r *memoryReader) GetChatMessage(_ context.Context, id pgtype.UUID) (db.ChatMessage, error) {
	if v, ok := r.messages[id]; ok {
		return v, nil
	}
	return db.ChatMessage{}, pgx.ErrNoRows
}

func uid() pgtype.UUID { return pgtype.UUID{Bytes: uuid.New(), Valid: true} }

func TestResolve(t *testing.T) {
	for _, name := range []string{
		"direct", "delegation", "automatic retry", "retry input owner", "no exact message",
		"missing source", "wrong agent", "missing originator", "owner changed", "cross workspace agent",
		"cross workspace chat", "wrong chat creator", "wrong chat agent", "archived", "different session message",
		"different turn message", "assistant message", "control message", "missing message",
		"cycle", "manual rerun", "parent task", "terminal token", "database failure",
	} {
		t.Run(name, func(t *testing.T) {
			agent, ws, owner, session, task, message := uid(), uid(), uid(), uid(), uid(), uid()
			r := &memoryReader{
				tasks: map[pgtype.UUID]db.AgentTaskQueue{
					task: {ID: task, AgentID: agent, OriginatorUserID: owner, ChatSessionID: session, Status: "running"},
				},
				agents: map[pgtype.UUID]db.Agent{agent: {ID: agent, WorkspaceID: ws}},
				sessions: map[pgtype.UUID]db.ChatSession{
					session: {ID: session, WorkspaceID: ws, AgentID: agent, CreatorID: owner, Status: "active"},
				},
				messages: map[pgtype.UUID]db.ChatMessage{
					message: {ID: message, ChatSessionID: session, TaskID: task, Role: "user", MessageKind: "message", Content: "yes"},
				},
			}
			current := r.tasks[task]
			source := r.sessions[session]
			input := r.messages[message]
			startID, callerAgent, expectedHops := task, agent, 0
			wantErr := true
			switch name {
			case "direct":
				wantErr = false
			case "delegation", "automatic retry":
				startID = uid()
				child := db.AgentTaskQueue{ID: startID, AgentID: agent, OriginatorUserID: owner, Status: "running"}
				if name == "delegation" {
					child.DelegatedFromTaskID = task
				} else {
					child.RetryOfTaskID = task
				}
				r.tasks[startID] = child
				expectedHops, wantErr = 1, false
			case "retry input owner":
				current.ChatInputTaskID = uid()
				input.TaskID = current.ChatInputTaskID
				wantErr = false
			case "no exact message":
				message, wantErr = pgtype.UUID{}, false
			case "missing source":
				current.ChatSessionID = pgtype.UUID{}
			case "wrong agent":
				callerAgent = uid()
			case "missing originator":
				current.OriginatorUserID = pgtype.UUID{}
			case "owner changed":
				startID = uid()
				r.tasks[startID] = db.AgentTaskQueue{
					ID: startID, AgentID: agent, OriginatorUserID: uid(), Status: "running", DelegatedFromTaskID: task,
				}
			case "cross workspace agent":
				r.agents[agent] = db.Agent{ID: agent, WorkspaceID: uid()}
			case "cross workspace chat":
				source.WorkspaceID = uid()
			case "wrong chat creator":
				source.CreatorID = uid()
			case "wrong chat agent":
				source.AgentID = uid()
			case "archived":
				source.Status = "archived"
			case "different session message":
				input.ChatSessionID = uid()
			case "different turn message":
				input.TaskID = uid()
			case "assistant message":
				input.Role = "assistant"
			case "control message":
				input.MessageKind = "channel_command"
			case "missing message":
				message = uid()
			case "cycle":
				current.ChatSessionID = pgtype.UUID{}
				current.DelegatedFromTaskID = task
			case "manual rerun":
				current.ChatSessionID = pgtype.UUID{}
				current.RerunOfTaskID = uid()
			case "parent task":
				current.ChatSessionID = pgtype.UUID{}
				current.ParentTaskID = uid()
			case "terminal token":
				current.Status = "completed"
			case "database failure":
				r.err = errors.New("database offline")
			}
			r.tasks[task], r.sessions[session], r.messages[input.ID] = current, source, input
			got, err := Resolve(context.Background(), r, startID, callerAgent, ws, message)
			if wantErr {
				if err == nil {
					t.Fatalf("unexpected context: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Session.ID != session || got.Task.ID != task || got.Hops != expectedHops {
				t.Fatalf("incorrect source: %+v", got)
			}
			if message.Valid && (got.Message == nil || got.Message.ID != message) {
				t.Fatal("exact message not verified")
			}
			if !message.Valid && got.Message != nil {
				t.Fatal("unrequested source message selected")
			}
		})
	}
}

func TestResolveBoundsChain(t *testing.T) {
	agent, ws, owner := uid(), uid(), uid()
	r := &memoryReader{
		tasks:  map[pgtype.UUID]db.AgentTaskQueue{},
		agents: map[pgtype.UUID]db.Agent{agent: {ID: agent, WorkspaceID: ws}},
	}
	first, current := uid(), pgtype.UUID{}
	for i := 0; i <= MaxTaskHops; i++ {
		id := uid()
		if i == 0 {
			id = first
		} else {
			previous := r.tasks[current]
			previous.DelegatedFromTaskID = id
			r.tasks[current] = previous
		}
		r.tasks[id] = db.AgentTaskQueue{ID: id, AgentID: agent, OriginatorUserID: owner, Status: "running"}
		current = id
	}
	if _, err := Resolve(context.Background(), r, first, agent, ws, pgtype.UUID{}); !errors.Is(err, ErrInvalidChain) {
		t.Fatalf("overlong chain: %v", err)
	}
}

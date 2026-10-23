package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestChatSourceContext(t *testing.T) {
	for _, name := range []string{
		"direct", "delegated", "no exact message", "plain credential", "bad message id",
		"wrong task agent", "unrelated turn", "different owner", "revoked membership", "archived session",
		"revoked agent access", "IM conversation",
	} {
		t.Run(name, func(t *testing.T) {
			ownerID := dbfx.User(t, "Context owner", uuid.NewString()+"@example.invalid")
			dbfx.Member(t, testWorkspaceID, ownerID, "member")
			agentID := dbfx.Agent(t, "Context source agent", testRuntimeID, testutil.Cols{
				"owner_id": ownerID,
			})
			sessionID := dbfx.Insert(t, "chat_session", testutil.Cols{
				"workspace_id": testWorkspaceID, "agent_id": agentID,
				"creator_id": ownerID, "title": "Source", "status": "active",
			})
			sourceTaskID := dbfx.Task(t, agentID, testutil.Cols{
				"runtime_id": testRuntimeID, "chat_session_id": sessionID,
				"originator_user_id": ownerID, "accountable_user_id": ownerID,
				"originator_source": "direct_human", "status": "running",
			})
			messageID := dbfx.Insert(t, "chat_message", testutil.Cols{
				"chat_session_id": sessionID, "task_id": sourceTaskID,
				"role": "user", "message_kind": "message", "content": "private source text",
			})
			taskID, callerAgentID := sourceTaskID, agentID
			query := url.Values{"source_message_id": {messageID}}
			want := http.StatusOK
			switch name {
			case "delegated":
				callerAgentID = dbfx.Agent(t, "Context delegate", testRuntimeID)
				taskID = dbfx.Task(t, callerAgentID, testutil.Cols{
					"runtime_id": testRuntimeID, "issue_id": dbfx.Issue(t, "Delegated work"),
					"originator_user_id": ownerID, "accountable_user_id": ownerID,
					"delegated_from_task_id": sourceTaskID, "originator_source": "delegation",
					"status": "running",
				})
			case "no exact message":
				query = url.Values{}
			case "plain credential":
				want = http.StatusForbidden
			case "bad message id":
				query.Set("source_message_id", "not-a-uuid")
				want = http.StatusBadRequest
			case "wrong task agent":
				callerAgentID, want = uuid.NewString(), http.StatusNotFound
			case "unrelated turn":
				other := dbfx.Task(t, agentID, testutil.Cols{"runtime_id": testRuntimeID})
				dbfx.Exec(t, "UPDATE chat_message SET task_id = $1 WHERE id = $2", other, messageID)
				want = http.StatusNotFound
			case "different owner":
				dbfx.Exec(t, "UPDATE chat_session SET creator_id = $1 WHERE id = $2", testUserID, sessionID)
				want = http.StatusNotFound
			case "revoked membership":
				dbfx.Exec(t, "DELETE FROM member WHERE workspace_id = $1 AND user_id = $2", testWorkspaceID, ownerID)
				want = http.StatusNotFound
			case "archived session":
				dbfx.Exec(t, "UPDATE chat_session SET status = 'archived' WHERE id = $1", sessionID)
				want = http.StatusNotFound
			case "revoked agent access":
				dbfx.Exec(t, "UPDATE agent SET owner_id = $1, permission_mode = 'private' WHERE id = $2", testUserID, agentID)
				want = http.StatusNotFound
			case "IM conversation":
				dbfx.Insert(t, "channel_chat_session_binding", testutil.Cols{
					"chat_session_id": sessionID, "installation_id": uuid.NewString(),
					"channel_type": "slack", "channel_chat_id": uuid.NewString(), "chat_type": "p2p",
				})
				want = http.StatusNotFound
			}
			req := withChatTestWorkspaceCtx(t, newRequest(http.MethodGet, "/api/chat/context?"+query.Encode(), nil))
			req.Header.Set("X-Task-ID", taskID)
			req.Header.Set("X-Agent-ID", callerAgentID)
			if name != "plain credential" {
				req.Header.Set("X-Actor-Source", "task_token")
			}
			w := httptest.NewRecorder()
			testHandler.GetChatSourceContext(w, req)
			if w.Code != want {
				t.Fatalf("status = %d, want %d: %s", w.Code, want, w.Body.String())
			}
			if want != http.StatusOK {
				if strings.Contains(w.Body.String(), sessionID) || strings.Contains(w.Body.String(), ownerID) {
					t.Fatal("refusal leaked source identifiers")
				}
				return
			}
			var got ChatSourceContextResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.ChatSessionID != sessionID || got.SourceTaskID != sourceTaskID || got.OwnerID != ownerID {
				t.Fatalf("wrong binding: %+v", got)
			}
			if got.AuthorizationGranted || strings.Contains(w.Body.String(), "private source text") {
				t.Fatal("context response granted authority or exposed transcript content")
			}
			if got.SourceMessageID != query.Get("source_message_id") {
				t.Fatal("source message was not exactly caller-selected")
			}
		})
	}
}

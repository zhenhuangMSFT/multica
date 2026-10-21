package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func commentInboxItem(t *testing.T, issueID string, overrides testutil.Cols) string {
	t.Helper()
	cols := testutil.Cols{
		"workspace_id": testWorkspaceID, "issue_id": issueID,
		"recipient_type": "member", "recipient_id": testUserID,
		"type": "new_comment", "severity": "info", "title": "Unread reply",
		"created_at": testutil.Raw("now() - interval '1 minute'"),
	}
	for key, value := range overrides {
		cols[key] = value
	}
	return dbfx.Insert(t, "inbox_item", cols)
}

func TestCreateCommentMarksOwnIssueInboxRead(t *testing.T) {
	for _, mode := range []string{"top-level", "thread-reply", "attachment"} {
		t.Run(mode, func(t *testing.T) {
			issueID := dbfx.Issue(t, "Respond outside inbox")
			otherIssueID := dbfx.Issue(t, "Unrelated unread issue")
			otherUserID := dbfx.User(t, "Other recipient", uuid.NewString()+"@example.test")
			otherWorkspaceID := dbfx.Workspace(t, "Other inbox", "other-inbox-"+uuid.NewString())
			agentID := dbfx.Agent(t, "Inbox agent", testRuntimeID)
			readIDs := []string{
				commentInboxItem(t, issueID, nil),
				commentInboxItem(t, issueID, testutil.Cols{"type": "status_changed"}),
			}
			unreadIDs := []string{
				commentInboxItem(t, otherIssueID, nil),
				commentInboxItem(t, "", testutil.Cols{"issue_id": nil}),
				commentInboxItem(t, issueID, testutil.Cols{"recipient_id": otherUserID}),
				commentInboxItem(t, issueID, testutil.Cols{"recipient_type": "agent", "recipient_id": agentID}),
				commentInboxItem(t, issueID, testutil.Cols{"workspace_id": otherWorkspaceID}),
				commentInboxItem(t, issueID, testutil.Cols{"archived": true}),
				commentInboxItem(t, issueID, testutil.Cols{"created_at": testutil.Raw("now() + interval '1 minute'")}),
			}
			alreadyReadID := commentInboxItem(t, issueID, testutil.Cols{"read": true})
			body := map[string]any{"content": "Handled from the issue details"}
			switch mode {
			case "thread-reply":
				body["parent_id"] = dbfx.Comment(t, issueID, "Please respond")
			case "attachment":
				body["attachment_ids"] = []string{unlinkedIssueAttachment(t, issueID)}
			}

			h := *testHandler
			h.Bus = events.New()
			published := make(map[string]int)
			h.Bus.Subscribe(protocol.EventInboxRead, func(event events.Event) {
				payload, ok := event.Payload.(map[string]any)
				if !ok || event.WorkspaceID != testWorkspaceID || event.ActorType != "member" || event.ActorID != testUserID || payload["recipient_id"] != testUserID {
					t.Errorf("unexpected inbox read event: %+v", event)
					return
				}
				id, _ := payload["item_id"].(string)
				published[id]++
				if n := dbfx.Count(t, `SELECT count(*) FROM inbox_item WHERE id = $1 AND read = true`, id); n != 1 {
					t.Error("inbox read event was published before commit")
				}
			})
			request := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", body), "id", issueID)
			testutil.Call(t, h.CreateComment, request).Want(http.StatusCreated)

			for _, id := range readIDs {
				if n := dbfx.Count(t, `SELECT count(*) FROM inbox_item WHERE id = $1 AND read = true AND archived = false`, id); n != 1 {
					t.Errorf("responded issue notification %s remains unread", id)
				}
				if published[id] != 1 {
					t.Errorf("inbox read events for %s = %d, want 1", id, published[id])
				}
			}
			for _, id := range unreadIDs {
				if n := dbfx.Count(t, `SELECT count(*) FROM inbox_item WHERE id = $1 AND read = false`, id); n != 1 {
					t.Errorf("unrelated, archived, or newer notification %s was marked read", id)
				}
			}
			if len(published) != len(readIDs) || published[alreadyReadID] != 0 {
				t.Errorf("unexpected inbox read events: %v", published)
			}
		})
	}
}

type failCommentInboxReadStarter struct{ pool *pgxpool.Pool }
type failCommentInboxReadTx struct{ pgx.Tx }

func (s failCommentInboxReadStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return failCommentInboxReadTx{tx}, nil
}

func (tx failCommentInboxReadTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	if strings.Contains(query, "-- name: MarkInboxReadByIssue") {
		return nil, errors.New("injected inbox read failure")
	}
	return tx.Tx.Query(ctx, query, args...)
}

func TestCreateCommentInboxReadFailureRollsBack(t *testing.T) {
	for _, withAttachment := range []bool{false, true} {
		name := "plain"
		if withAttachment {
			name = "attachment"
		}
		t.Run(name, func(t *testing.T) {
			issueID := dbfx.Issue(t, "Atomic comment and inbox read")
			itemID := commentInboxItem(t, issueID, nil)
			body := map[string]any{"content": "Do not partially save"}
			if withAttachment {
				body["attachment_ids"] = []string{unlinkedIssueAttachment(t, issueID)}
			}
			h := *testHandler
			h.TxStarter = failCommentInboxReadStarter{testPool}
			h.Bus = events.New()
			var readEvents int
			h.Bus.Subscribe(protocol.EventInboxRead, func(events.Event) { readEvents++ })
			request := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", body), "id", issueID)
			testutil.Call(t, h.CreateComment, request).Want(http.StatusInternalServerError)
			if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1`, issueID); n != 0 {
				t.Errorf("failed inbox update left %d comments", n)
			}
			if n := dbfx.Count(t, `SELECT count(*) FROM inbox_item WHERE id = $1 AND read = false`, itemID); n != 1 || readEvents != 0 {
				t.Errorf("failed response changed inbox state or published read events: unread=%d events=%d", n, readEvents)
			}
		})
	}
}

func TestCreateCommentDoesNotReadInboxOnRejectedOrAgentResponse(t *testing.T) {
	for _, mode := range []string{"invalid-content", "invalid-parent", "failed-attachment", "agent"} {
		t.Run(mode, func(t *testing.T) {
			issueID := dbfx.Issue(t, "Preserve unread inbox")
			itemID := commentInboxItem(t, issueID, nil)
			body := map[string]any{"content": "A response"}
			wantStatus := http.StatusBadRequest
			switch mode {
			case "invalid-content":
				body["content"] = ""
			case "invalid-parent":
				body["parent_id"] = uuid.NewString()
			case "failed-attachment":
				body["attachment_ids"] = []string{unlinkedIssueAttachment(t, issueID)}
				wantStatus = http.StatusInternalServerError
			case "agent":
				wantStatus = http.StatusCreated
			}
			h := *testHandler
			h.Bus = events.New()
			if mode == "failed-attachment" {
				h.TxStarter = &failLinkAttachmentsTxStarter{delegate: testPool}
			}
			var readEvents int
			h.Bus.Subscribe(protocol.EventInboxRead, func(events.Event) { readEvents++ })
			request := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", body), "id", issueID)
			if mode == "agent" {
				request.Header.Set("X-Actor-Source", "task_token")
				request.Header.Set("X-Agent-ID", dbfx.Agent(t, "Responding agent", testRuntimeID))
			}
			testutil.Call(t, h.CreateComment, request).Want(wantStatus)
			if n := dbfx.Count(t, `SELECT count(*) FROM inbox_item WHERE id = $1 AND read = false`, itemID); n != 1 || readEvents != 0 {
				t.Errorf("rejected or agent response changed human inbox: unread=%d events=%d", n, readEvents)
			}
		})
	}
}

func TestCreateCommentKeepsConcurrentNotificationUnread(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	issueID := dbfx.Issue(t, "A reply arrives while posting")
	olderID := commentInboxItem(t, issueID, nil)
	holder, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	if _, err := holder.Exec(ctx, `UPDATE issue SET title = title WHERE id = $1`, issueID); err != nil {
		t.Fatal(err)
	}

	h := *testHandler
	h.Bus = events.New()
	request := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{
		"content": "Respond while another update arrives",
	}).WithContext(ctx), "id", issueID)
	done := make(chan error, 1)
	go func() {
		response := httptest.NewRecorder()
		h.CreateComment(response, request)
		if response.Code != http.StatusCreated {
			done <- fmt.Errorf("create comment: status %d: %s", response.Code, response.Body.String())
			return
		}
		done <- nil
	}()
	waitForBlockedQuery(t, done, "CreateComment")
	newerID := commentInboxItem(t, issueID, testutil.Cols{"created_at": testutil.Raw("clock_timestamp()")})
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM inbox_item WHERE id = $1 AND read = true`, olderID); n != 1 {
		t.Error("the notification preceding the response remains unread")
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM inbox_item WHERE id = $1 AND read = false`, newerID); n != 1 {
		t.Error("the notification arriving while posting was marked read")
	}
}

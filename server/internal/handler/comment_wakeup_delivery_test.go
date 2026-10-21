package handler

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestCreateComment_WakeupTaskChoosesCurrentDeliveryThread(t *testing.T) {
	for _, mode := range []string{"current-user-message", "explicit-pin", "top-level", "cross-issue-parent"} {
		t.Run(mode, func(t *testing.T) {
			issue := dbfx.Issue(t, "Wakeup delivery")
			agent := dbfx.Agent(t, "Wakeup author", testRuntimeID)
			original := dbfx.Comment(t, issue, "Original scheduling context")
			current := dbfx.Comment(t, issue, "New progress thread")
			wakeupID := uuid.NewString()
			task := dbfx.Task(t, agent, testutil.Cols{
				"issue_id": issue, "runtime_id": testRuntimeID, "status": "running",
				"trigger_comment_id": original, "trigger_evidence_kind": "issue_wakeup",
				"trigger_evidence_ref_id": wakeupID, "context": `{"wakeup_id":"` + wakeupID + `"}`,
			})
			body := map[string]any{"content": "Wakeup progress", "parent_id": current}
			want := http.StatusCreated
			switch mode {
			case "explicit-pin":
				body["parent_id"] = original
			case "top-level":
				delete(body, "parent_id")
			case "cross-issue-parent":
				body["parent_id"] = dbfx.Comment(t, dbfx.Issue(t, "Other issue"), "Not this issue")
				want = http.StatusBadRequest
			}
			request := withURLParam(newRequest(http.MethodPost, "/", body), "id", issue)
			request.Header.Set("X-Agent-ID", agent)
			request.Header.Set("X-Task-ID", task)
			var response CommentResponse
			result := testutil.Call(t, testHandler.CreateComment, request).Want(want)
			if want == http.StatusCreated {
				result.JSON(&response)
				if response.SourceTaskID == nil || *response.SourceTaskID != task {
					t.Fatal("wakeup reply lost its authoring run")
				}
				if expected, ok := body["parent_id"]; ok {
					if response.ParentID == nil || *response.ParentID != expected {
						t.Fatalf("reply parent = %v, want %s", response.ParentID, expected)
					}
				} else if response.ParentID != nil {
					t.Fatal("top-level update acquired a parent")
				}
			}
		})
	}
}

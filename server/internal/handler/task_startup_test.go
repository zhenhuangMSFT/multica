package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func startupRequest(id string, claim time.Time, phase string, elapsed int64) *http.Request {
	return withURLParam(newDaemonTokenRequest("POST", "/startup", taskStartupRequest{
		DispatchedAt: claim.Format(time.RFC3339Nano),
		Phase: protocol.TaskStartupPhase{
			Phase: phase, PhaseElapsedMS: elapsed, TotalElapsedMS: elapsed, ObservedAt: claim.Add(time.Duration(elapsed) * time.Millisecond),
		},
	}, testWorkspaceID, "startup-test"), "taskId", id)
}

func startupResult(t *testing.T, id string) map[string]json.RawMessage {
	t.Helper()
	var raw []byte
	dbfx.QueryRow(t, `SELECT result FROM agent_task_queue WHERE id=$1`, id).Scan(&raw)
	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestReportTaskStartupConcurrentAndCompletion(t *testing.T) {
	id, _, claim := startClaimFixture(t, "running")
	dbfx.Exec(t, `UPDATE agent_task_queue SET result='{"existing":"retained"}' WHERE id=$1`, id)
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for i, phase := range []string{"claimed", "first_output_received"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			testHandler.ReportTaskStartup(w, startupRequest(id, claim, phase, int64(i)*15000))
			results <- w
		}()
	}
	wg.Wait()
	close(results)
	for w := range results {
		if w.Code != http.StatusOK {
			t.Fatalf("report: %d %s", w.Code, w.Body.String())
		}
	}
	testutil.Call(t, testHandler.ReportTaskStartup, startupRequest(id, claim, "first_output_received", 99999)).Want(http.StatusOK)
	result := startupResult(t, id)
	if string(result["existing"]) != `"retained"` {
		t.Fatalf("lost existing result: %s", result)
	}
	var timing struct {
		Phases map[string]protocol.TaskStartupPhase `json:"phases"`
	}
	if err := json.Unmarshal(result["startup_timing"], &timing); err != nil {
		t.Fatal(err)
	}
	if len(timing.Phases) != 2 || timing.Phases["first_output_received"].TotalElapsedMS != 15000 {
		t.Fatalf("lost or replaced first observations: %#v", timing)
	}
	var taskID pgtype.UUID
	if err := taskID.Scan(id); err != nil {
		t.Fatal(err)
	}
	completed, err := testHandler.Queries.CompleteAgentTask(context.Background(), db.CompleteAgentTaskParams{
		ID: taskID, Result: []byte(`{"output":"unchanged"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var terminal map[string]json.RawMessage
	if err := json.Unmarshal(completed.Result, &terminal); err != nil {
		t.Fatal(err)
	}
	if string(terminal["output"]) != `"unchanged"` || string(terminal["startup_timing"]) != string(result["startup_timing"]) {
		t.Fatalf("completion lost output or timings: %s", completed.Result)
	}
	testutil.Call(t, testHandler.ReportTaskStartup, startupRequest(id, claim, "finished", 17000)).Want(http.StatusOK)
	terminal = startupResult(t, id)
	if string(terminal["output"]) != `"unchanged"` {
		t.Fatal("late report replaced terminal output")
	}
}

func TestReportTaskStartupFencesReclaimedGeneration(t *testing.T) {
	id, _, claim := startClaimFixture(t, "running")
	testutil.Call(t, testHandler.ReportTaskStartup, startupRequest(id, claim, "claimed", 0)).Want(http.StatusOK)
	nextClaim := claim.Add(time.Microsecond)
	dbfx.Exec(t, `UPDATE agent_task_queue SET dispatched_at=$2 WHERE id=$1`, id, nextClaim)
	testutil.Call(t, testHandler.ReportTaskStartup, startupRequest(id, claim, "first_output_received", 15000)).Want(http.StatusConflict)
	testutil.Call(t, testHandler.ReportTaskStartup, startupRequest(id, nextClaim, "runtime_started", 1000)).Want(http.StatusOK)
	var timing struct {
		DispatchedAt string                               `json:"dispatched_at"`
		Phases       map[string]protocol.TaskStartupPhase `json:"phases"`
	}
	if err := json.Unmarshal(startupResult(t, id)["startup_timing"], &timing); err != nil {
		t.Fatal(err)
	}
	if len(timing.Phases) != 1 || timing.DispatchedAt != nextClaim.Format(time.RFC3339Nano) {
		t.Fatalf("retained old claim's timings: %#v", timing)
	}
}

func TestReportTaskStartupValidationAndAuthorization(t *testing.T) {
	id, _, claim := startClaimFixture(t, "running")
	for _, req := range []*http.Request{
		startupRequest(id, claim, "unknown", 0),
		startupRequest(id, claim, "claimed", -1),
		startupRequest(id, claim.Add(time.Nanosecond), "claimed", 0),
		withURLParam(newDaemonTokenRequest("POST", "/startup", map[string]string{}, testWorkspaceID, "startup-test"), "taskId", id),
	} {
		testutil.Call(t, testHandler.ReportTaskStartup, req).Want(http.StatusBadRequest)
	}

	crossWorkspace := withURLParam(newDaemonTokenRequest("POST", "/startup", nil,
		"00000000-0000-0000-0000-000000000001", "startup-test"), "taskId", id)
	testutil.Call(t, testHandler.ReportTaskStartup, crossWorkspace).Want(http.StatusNotFound)
}

func TestReportTaskStartupListPreservesClaimPrecision(t *testing.T) {
	id, _, claim := startClaimFixture(t, "running")
	dbfx.Exec(t, `UPDATE agent_task_queue SET created_at=$2 WHERE id=$1`, id, claim.Add(-2*time.Second))
	testutil.Call(t, testHandler.ReportTaskStartup, startupRequest(id, claim, "first_output_received", 15000)).Want(http.StatusOK)
	var agentID string
	dbfx.QueryRow(t, `SELECT agent_id FROM agent_task_queue WHERE id=$1`, id).Scan(&agentID)
	req := withURLParam(newRequestAs(testUserID, "GET", "/api/agents/"+agentID+"/tasks", nil), "id", agentID)
	var tasks []AgentTaskResponse
	response := testutil.Call(t, testHandler.ListAgentTasks, req).Want(http.StatusOK).JSON(&tasks)
	for _, task := range tasks {
		if task.ID != id {
			continue
		}
		if task.DispatchedAt == nil {
			t.Fatal("API omitted dispatch claim")
		}
		apiClaim, err := time.Parse(time.RFC3339Nano, *task.DispatchedAt)
		if err != nil || !apiClaim.Equal(claim) {
			t.Fatalf("API rounded dispatch claim: %s, want instant %s", *task.DispatchedAt, claim.Format(time.RFC3339Nano))
		}
		var result struct {
			StartupTiming struct {
				DispatchedAt string `json:"dispatched_at"`
			} `json:"startup_timing"`
		}
		raw, err := json.Marshal(task.Result)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		diagnosticClaim, err := time.Parse(time.RFC3339Nano, result.StartupTiming.DispatchedAt)
		if err != nil || !diagnosticClaim.Equal(apiClaim) {
			t.Fatalf("API claim and diagnostic claim disagree: %s vs %s", *task.DispatchedAt, result.StartupTiming.DispatchedAt)
		}
		t.Logf("startup-api-response: %s", response.Body.String())
		return
	}
	t.Fatal("new task missing from API history")
}

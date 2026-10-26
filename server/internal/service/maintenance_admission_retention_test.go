package service

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestMaintenanceServerRetainsAgedWork(t *testing.T) {
	f := newMaintenanceFixture(t)
	ctx := context.Background()
	ids := map[string]string{}
	for _, status := range []string{"queued", "dispatched", "running", "waiting_local_directory"} {
		ids[status] = f.task(t, 0, testutil.Cols{"status": status, "created_at": testutil.Raw("now()-interval '2 days'"),
			"dispatched_at": testutil.Raw("now()-interval '2 days'"), "started_at": testutil.Raw("now()-interval '2 days'")})
	}
	parent := f.task(t, 1, testutil.Cols{"status": "failed", "failure_reason": "runtime_offline"})
	retry := f.task(t, 1, testutil.Cols{"status": "deferred", "parent_task_id": parent,
		"fire_at": testutil.Raw("now()-interval '2 days'")})
	ids["deferred"] = retry
	if err := f.repo.Prepare(ctx, f.delivery); err != nil {
		t.Fatal(err)
	}
	for i, rt := range f.runtimes {
		f.fixtures[i].Exec(t, `UPDATE agent_runtime SET status='offline', last_seen_at=now()-interval '2 days' WHERE id=$1`, rt)
	}
	runSweep := func() map[string]int {
		t.Helper()
		queued, err := f.q.ExpireStaleQueuedTasks(ctx, db.ExpireStaleQueuedTasksParams{ReconnectGraceSecs: 1, MaxPerTick: 100})
		if err != nil {
			t.Fatal(err)
		}
		retries, err := f.q.FailExpiredRuntimeReconnectRetries(ctx, db.FailExpiredRuntimeReconnectRetriesParams{
			ReconnectGraceSecs: 1, RuntimeStaleSecs: RuntimeClaimFreshnessSeconds, MaxPerTick: 100})
		if err != nil {
			t.Fatal(err)
		}
		stale, err := f.q.FailStaleTasks(ctx, db.FailStaleTasksParams{DispatchTimeoutSecs: 1, RunningTimeoutSecs: 1,
			RuntimeStaleSecs: RuntimeClaimFreshnessSeconds, RuntimeReconnectGraceSecs: 1})
		if err != nil {
			t.Fatal(err)
		}
		offline, err := f.q.FailTasksForOfflineRuntimes(ctx, db.FailTasksForOfflineRuntimesParams{ReconnectGraceSecs: 1, MaxPerTick: 100})
		if err != nil {
			t.Fatal(err)
		}
		return map[string]int{"queued": len(queued), "deferred": len(retries), "stale": len(stale), "offline": len(offline)}
	}
	for stage, count := range runSweep() {
		if count != 0 {
			t.Fatalf("held %s sweeper dropped %d tasks", stage, count)
		}
	}
	for status, id := range ids {
		task, err := f.q.GetAgentTask(ctx, util.MustParseUUID(id))
		if err != nil || task.Status != status {
			t.Fatalf("aged %s changed to %s: %v", status, task.Status, err)
		}
	}
	f.release(t)
	want := map[string]int{"queued": 1, "deferred": 1, "stale": 2, "offline": 1}
	for stage, count := range runSweep() {
		if count != want[stage] {
			t.Errorf("released %s sweeper failed to resume: got %d, want %d", stage, count, want[stage])
		}
	}
}

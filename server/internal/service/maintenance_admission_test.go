package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/daemoncontrol"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	lifecyclev1 "github.com/multica-ai/multica/server/pkg/lifecycle/v1"
	"github.com/redis/go-redis/v9"
)

func TestMaintenanceServerProtocolStores(t *testing.T) {
	url := os.Getenv("REDIS_MAINTENANCE_TEST_URL")
	if url == "" {
		t.Skip("REDIS_MAINTENANCE_TEST_URL not set; requires isolated Redis fixture")
	}
	f := newMaintenanceFixture(t)
	ctx := context.Background()
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { client.Close() })
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	store := daemoncontrol.NewFencedStore(daemoncontrol.NewRedisStore(client), f.repo)
	release := f.delivery
	release.Command.ID, release.Command.Operation, release.Command.Generation, release.Command.ExpiresAt = uuid.NewString(), "release", 1, time.Time{}
	t.Cleanup(func() {
		if err := client.Del(ctx, "mul:{maintenance}:request:"+f.delivery.Command.ID,
			"mul:{maintenance}:request:"+release.Command.ID, "mul:{maintenance}:active:"+f.delivery.Command.DaemonID).Err(); err != nil {
			t.Error(err)
		}
	})
	request, err := store.Create(ctx, f.delivery)
	if err != nil || request.State != "pending" {
		t.Fatalf("create: %+v, %v", request, err)
	}
	if open, err := f.q.RuntimeMaintenanceAdmissionOpen(ctx, f.runtimes[1]); err != nil || open {
		t.Fatalf("request became pending without server fence: %v, %v", open, err)
	}
	claimed, err := store.Deliver(ctx, f.delivery)
	if err != nil || !claimed {
		t.Fatalf("delivery: %v, %v", claimed, err)
	}
	claimed, err = store.Deliver(ctx, f.delivery)
	if err != nil || claimed {
		t.Fatalf("duplicate delivery: %v, %v", claimed, err)
	}
	acquired := f.result(f.delivery)
	if err := store.Complete(ctx, acquired); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, acquired); err != nil {
		t.Fatal("identical acquired result:", err)
	}
	if _, err := store.Create(ctx, release); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, f.result(release)); !errors.Is(err, daemoncontrol.ErrConflict) {
		t.Fatal("result accepted before authenticated command delivery:", err)
	}
	if delivered, err := store.Deliver(ctx, release); err != nil || !delivered {
		t.Fatalf("release delivery: %v, %v", delivered, err)
	}
	released := f.result(release)
	if err := store.Complete(ctx, released); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, released); err != nil {
		t.Fatal("identical released result:", err)
	}
	for _, rid := range f.runtimes {
		if open, err := f.q.RuntimeMaintenanceAdmissionOpen(ctx, rid); err != nil || !open {
			t.Fatalf("confirmed release failed to open server: %v, %v", open, err)
		}
	}
	request, err = store.Get(ctx, release.Command.ID)
	if err != nil || request.State != "completed" || request.Result == nil {
		t.Fatalf("bound stored result: %+v, %v", request, err)
	}
}

type maintenanceFixture struct {
	pool     *pgxpool.Pool
	q        *db.Queries
	service  *TaskService
	repo     *daemoncontrol.PostgresAdmission
	fixtures []*testutil.Fixture
	runtimes []pgtype.UUID
	agents   []string
	delivery lifecyclev1.MaintenanceDelivery
}

func newMaintenanceFixture(t *testing.T) *maintenanceFixture {
	t.Helper()
	pool := sharedTestPool(t)
	root := testutil.New(pool, "", "")
	user := root.User(t, "Maintenance fixture", uuid.NewString()+"@example.test")
	daemon := "maintenance-test-" + uuid.NewString()
	f := &maintenanceFixture{pool: pool, q: db.New(pool), repo: daemoncontrol.NewPostgresAdmission(pool)}
	f.service = NewTaskService(f.q, pool, nil, events.New())
	var parts []string
	for i := range 2 {
		ws := root.Workspace(t, "Maintenance fixture", uuid.NewString())
		root.Member(t, ws, user, "owner")
		fx := testutil.New(pool, ws, user)
		rt := fx.Runtime(t, fmt.Sprintf("Runtime %d", i), testutil.Cols{"daemon_id": daemon})
		f.fixtures = append(f.fixtures, fx)
		f.runtimes = append(f.runtimes, util.MustParseUUID(rt))
		f.agents = append(f.agents, fx.Agent(t, fmt.Sprintf("Agent %d", i), rt, testutil.Cols{"max_concurrent_tasks": 10}))
		parts = append(parts, rt+":"+ws+":"+user)
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	now := time.Now().UTC()
	f.delivery = lifecyclev1.MaintenanceDelivery{
		OwnerID: user, Scope: hex.EncodeToString(sum[:]),
		Command: lifecyclev1.MaintenanceCommand{ID: uuid.NewString(), Operation: "acquire", DaemonID: daemon,
			Host: "fixture-host", Profile: "fixture", Holder: "fixture-holder", IssuedAt: now, ExpiresAt: now.Add(time.Hour)},
	}
	root.Cleanup(t, `DELETE FROM daemon_maintenance_admission WHERE daemon_id=$1`, daemon)
	return f
}

func (f *maintenanceFixture) task(t *testing.T, runtime int, over testutil.Cols) string {
	t.Helper()
	cols := testutil.Cols{"runtime_id": util.UUIDToString(f.runtimes[runtime]),
		"issue_id": f.fixtures[runtime].Issue(t, uuid.NewString())}
	for k, v := range over {
		cols[k] = v
	}
	return f.fixtures[runtime].Task(t, f.agents[runtime], cols)
}

func (f *maintenanceFixture) result(d lifecyclev1.MaintenanceDelivery) lifecyclev1.MaintenanceResult {
	c := d.Command
	status := lifecyclev1.MaintenanceStatus{ObservedAt: time.Now().UTC(), Host: c.Host, Profile: c.Profile, Generation: c.Generation}
	if c.Operation != "release" {
		status.Blocked, status.Generation, status.Holder, status.ExpiresAt = true, c.Generation+1, c.Holder, c.ExpiresAt
	}
	return lifecyclev1.MaintenanceResult{Delivery: d, Code: "ok", Status: status}
}

func (f *maintenanceFixture) release(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if err := f.repo.Complete(ctx, f.result(f.delivery)); err != nil {
		t.Fatal(err)
	}
	release := f.delivery
	release.Command.ID, release.Command.Operation, release.Command.Generation, release.Command.ExpiresAt = uuid.NewString(), "release", 1, time.Time{}
	if err := f.repo.Prepare(ctx, release); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Complete(ctx, f.result(release)); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceServerQueuePreservation(t *testing.T) {
	f := newMaintenanceFixture(t)
	ctx := context.Background()
	var queued, deferred []string
	for i := range 2 {
		queued = append(queued, f.task(t, i, nil))
		deferred = append(deferred, f.task(t, i, testutil.Cols{"status": "deferred", "fire_at": testutil.Raw("now() - interval '1 minute'")}))
	}
	if err := f.repo.Prepare(ctx, f.delivery); err != nil {
		t.Fatal(err)
	}
	unrelated := f.fixtures[0].Runtime(t, "Unrelated runtime", testutil.Cols{"daemon_id": "unrelated-" + uuid.NewString()})
	agent := f.fixtures[0].Agent(t, "Unrelated agent", unrelated)
	other := f.fixtures[0].Task(t, agent, testutil.Cols{"runtime_id": unrelated, "issue_id": f.fixtures[0].Issue(t, "Unrelated work")})
	if task, err := f.service.ClaimTask(ctx, util.MustParseUUID(agent)); err != nil || task == nil || util.UUIDToString(task.ID) != other {
		t.Fatalf("daemon fence affected unrelated daemon: %+v, %v", task, err)
	}
	// A reconstructed repository still observes the committed fence.
	f.repo = daemoncontrol.NewPostgresAdmission(f.pool)
	if tasks, err := f.service.ClaimTasksForRuntimes(ctx, f.runtimes, 10); err != nil || len(tasks) != 0 {
		t.Fatalf("held batch dispatched: tasks=%v, err=%v", tasks, err)
	}
	for i, id := range queued {
		if task, err := f.service.ClaimTask(ctx, util.MustParseUUID(f.agents[i])); err != nil || task != nil {
			t.Fatalf("held singular dispatched: task=%v, err=%v", task, err)
		}
		row, err := f.q.GetAgentTask(ctx, util.MustParseUUID(id))
		if err != nil || row.Status != "queued" || row.DispatchedAt.Valid {
			t.Fatalf("queue changed: %+v, %v", row, err)
		}
		row, err = f.q.GetAgentTask(ctx, util.MustParseUUID(deferred[i]))
		if err != nil || row.Status != "deferred" {
			t.Fatalf("retry promoted or lost: %+v, %v", row, err)
		}
	}
	f.release(t)
	tasks, err := f.service.ClaimTasksForRuntimes(ctx, f.runtimes, 10)
	if err != nil {
		t.Fatal(err)
	}
	next, err := f.service.ClaimTasksForRuntimes(ctx, f.runtimes, 10)
	tasks = append(tasks, next...)
	if err != nil || len(tasks) != 4 {
		t.Fatalf("release lost queued/retry work: tasks=%d, err=%v", len(tasks), err)
	}
	seen := map[pgtype.UUID]bool{}
	for _, task := range tasks {
		if seen[task.ID] {
			t.Fatal("duplicate attempt in released batch")
		}
		seen[task.ID] = true
	}
}

func TestMaintenanceServerInflightReservation(t *testing.T) {
	f := newMaintenanceFixture(t)
	ctx := context.Background()
	id := f.task(t, 0, nil)
	task, err := f.service.ClaimTask(ctx, util.MustParseUUID(f.agents[0]))
	if err != nil || task == nil || util.UUIDToString(task.ID) != id {
		t.Fatalf("initial reservation: %+v, %v", task, err)
	}
	if err := f.repo.Prepare(ctx, f.delivery); err != nil {
		t.Fatal(err)
	}
	token := db.CreateTaskTokenParams{TokenHash: uuid.NewString(), TaskID: task.ID, AgentID: task.AgentID,
		WorkspaceID: util.MustParseUUID(f.fixtures[0].WorkspaceID), UserID: util.MustParseUUID(f.fixtures[0].UserID),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}
	if _, err := f.service.FinalizeTaskClaim(ctx, *task, token, nil, false, nil, nil); err == nil {
		t.Fatal("late delivery minted a token after acquisition")
	}
	if n := f.fixtures[0].Count(t, `SELECT count(*) FROM task_token WHERE task_id=$1`, id); n != 0 {
		t.Fatal("failed delivery retained a token")
	}
	if _, err := f.service.RequeueTaskAfterClaimFailure(ctx, *task); err != nil {
		t.Fatal(err)
	}
	row, err := f.q.GetAgentTask(ctx, task.ID)
	if err != nil || row.Status != "queued" {
		t.Fatalf("inflight reservation not preserved: %+v, %v", row, err)
	}
	// Already-owned active and resource-wait work can continue; neither a
	// held server fence nor retry polling cancels it or starts a second attempt.
	for i, status := range []string{"running", "waiting_local_directory", "dispatched"} {
		over := testutil.Cols{"status": status, "dispatched_at": testutil.Raw("now() - interval '10 minutes'")}
		if status == "running" {
			over["started_at"] = testutil.Raw("now()")
		}
		active := f.task(t, 1, over)
		if status == "dispatched" {
			f.fixtures[1].Exec(t, `UPDATE agent_task_queue SET prepare_lease_expires_at=now()-interval '1 minute' WHERE id=$1`, active)
			if reclaimed, err := f.service.ClaimTaskForRuntime(ctx, f.runtimes[1]); err != nil || reclaimed != nil {
				t.Fatalf("held stale reservation redelivered: %+v, %v", reclaimed, err)
			}
		} else if status == "waiting_local_directory" {
			if _, err := f.service.StartTask(ctx, util.MustParseUUID(active)); err != nil {
				t.Fatal("owned waiter prevented from continuing:", err)
			}
		}
		row, err := f.q.GetAgentTask(ctx, util.MustParseUUID(active))
		if err != nil || (i < 2 && row.Status != "running") || (i == 2 && row.Status != "dispatched") {
			t.Fatalf("owned work mutated unexpectedly: %+v, %v", row, err)
		}
	}
}

func TestMaintenanceServerAcquisitionWaitsForReservation(t *testing.T) {
	f := newMaintenanceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f.task(t, 0, nil)
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	task, err := f.q.WithTx(tx).ClaimAgentTask(ctx, db.ClaimAgentTaskParams{
		AgentID: util.MustParseUUID(f.agents[0]), RuntimeID: f.runtimes[0],
		PrepareLeaseSecs: prepareLeaseDuration.Seconds(), RuntimeStaleSecs: RuntimeClaimFreshnessSeconds,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- f.repo.Prepare(ctx, f.delivery) }()
	waitMaintenanceBlocked(t, ctx, f.pool, tx, done)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	row, err := f.q.GetAgentTask(ctx, task.ID)
	if err != nil || row.Status != "dispatched" {
		t.Fatalf("acquisition lost prior reservation: %+v, %v", row, err)
	}
}

func TestMaintenanceServerDispatchWaitsForAcquisition(t *testing.T) {
	f := newMaintenanceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f.task(t, 0, nil)
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	daemon := f.delivery.Command.DaemonID
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('maintenance:' || $1,0))`, daemon); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.q.ClaimAgentTask(ctx, db.ClaimAgentTaskParams{
			AgentID: util.MustParseUUID(f.agents[0]), RuntimeID: f.runtimes[0],
			PrepareLeaseSecs: prepareLeaseDuration.Seconds(), RuntimeStaleSecs: RuntimeClaimFreshnessSeconds,
		})
		done <- err
	}()
	waitMaintenanceBlocked(t, ctx, f.pool, tx, done)
	// This statement models the repository's fence commit while the dispatch
	// statement already has its original MVCC snapshot.
	if _, err := tx.Exec(ctx, `INSERT INTO daemon_maintenance_admission(daemon_id,state) VALUES($1,'{"closed":true}')`, daemon); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("waiting dispatch did not observe committed closure: %v", err)
	}
}

func waitMaintenanceBlocked(t *testing.T, ctx context.Context, pool *pgxpool.Pool, holder pgx.Tx, done <-chan error) {
	t.Helper()
	var pid int
	if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("operation crossed the uncommitted admission barrier: %v", err)
		default:
		}
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("operation did not reach the database admission barrier")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMaintenanceServerRepositoryFailures(t *testing.T) {
	f := newMaintenanceFixture(t)
	ctx := context.Background()
	if err := f.repo.Check(ctx, f.delivery); err == nil {
		t.Fatal("missing server intent authorized delivery")
	}
	if err := f.repo.Complete(ctx, f.result(f.delivery)); err == nil {
		t.Fatal("missing server intent accepted result")
	}
	if err := f.repo.Prepare(ctx, f.delivery); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Complete(ctx, f.result(f.delivery)); err != nil {
		t.Fatal(err)
	}
	release := f.delivery
	release.Command.ID, release.Command.Operation, release.Command.Generation, release.Command.ExpiresAt = uuid.NewString(), "release", 1, time.Time{}
	if err := f.repo.Prepare(ctx, release); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := f.repo.Complete(cancelled, f.result(release)); err == nil {
		t.Fatal("cancelled repository accepted release")
	}
	if open, err := f.q.RuntimeMaintenanceAdmissionOpen(ctx, f.runtimes[0]); err != nil || open {
		t.Fatalf("failed release opened fence: %v, %v", open, err)
	}
	stale := release
	stale.Command.ID, stale.Command.Generation = uuid.NewString(), 0
	if err := f.repo.Prepare(ctx, stale); err == nil {
		t.Fatal("stale generation accepted")
	}
	f.fixtures[0].Exec(t, `UPDATE daemon_maintenance_admission SET state='{}' WHERE daemon_id=$1`, release.Command.DaemonID)
	if open, err := f.q.RuntimeMaintenanceAdmissionOpen(ctx, f.runtimes[0]); err != nil || open {
		t.Fatalf("corrupt state opened fence: %v, %v", open, err)
	}
	f.fixtures[0].Exec(t, `UPDATE daemon_maintenance_admission SET state='{"version":1,"closed":false}' WHERE daemon_id=$1`, release.Command.DaemonID)
	if open, err := f.q.RuntimeMaintenanceAdmissionOpen(ctx, f.runtimes[0]); err != nil || open {
		t.Fatalf("partial released state opened fence: %v, %v", open, err)
	}
	if err := f.repo.Check(ctx, release); err == nil {
		t.Fatal("corrupt repository authorized delivery")
	}
}

func TestMaintenanceServerBoundGenerationsAndRegistration(t *testing.T) {
	f := newMaintenanceFixture(t)
	ctx := context.Background()
	if err := f.repo.Prepare(ctx, f.delivery); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Complete(ctx, f.result(f.delivery)); err != nil {
		t.Fatal(err)
	}
	rt := f.fixtures[0].Runtime(t, "New registration", testutil.Cols{"daemon_id": f.delivery.Command.DaemonID, "provider": "new-provider"})
	rtID := util.MustParseUUID(rt)
	if open, err := f.q.RuntimeMaintenanceAdmissionOpen(ctx, rtID); err != nil || open {
		t.Fatalf("new registration bypassed daemon fence: %v, %v", open, err)
	}
	rows, err := f.q.ListDaemonMaintenanceRegistrations(ctx, pgtype.Text{String: f.delivery.Command.DaemonID, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	var parts []string
	for _, row := range rows {
		parts = append(parts, util.UUIDToString(row.ID)+":"+util.UUIDToString(row.WorkspaceID)+":"+util.UUIDToString(row.OwnerID))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	recover := f.delivery
	recover.Scope = hex.EncodeToString(sum[:])
	recover.Command.ID, recover.Command.Operation, recover.Command.Generation, recover.Command.Holder = uuid.NewString(), "recover", 1, "new-holder"
	// Even a workspace admin cannot recover another registration owner's gate.
	otherOwner := f.fixtures[0].User(t, "Other owner", uuid.NewString()+"@example.test")
	f.fixtures[0].Exec(t, `UPDATE agent_runtime SET owner_id=$1 WHERE id=$2`, otherOwner, rt)
	if err := f.repo.Prepare(ctx, recover); err == nil {
		t.Fatal("mixed ownership accepted")
	}
	f.fixtures[0].Exec(t, `UPDATE agent_runtime SET owner_id=$1 WHERE id=$2`, f.delivery.OwnerID, rt)
	if err := f.repo.Prepare(ctx, recover); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Complete(ctx, f.result(recover)); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Complete(ctx, f.result(f.delivery)); err == nil {
		t.Fatal("late older result was accepted after recovery")
	}
	release := recover
	release.Command.ID, release.Command.Operation, release.Command.ExpiresAt = uuid.NewString(), "release", time.Time{}
	if err := f.repo.Prepare(ctx, release); err == nil {
		t.Fatal("stale release generation accepted")
	}
	release.Command.Generation = 2
	if err := f.repo.Prepare(ctx, release); err != nil {
		t.Fatal(err)
	}
	result := f.result(release)
	if err := f.repo.Complete(ctx, result); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Complete(ctx, result); err != nil {
		t.Fatal("identical completed result replay:", err)
	}
	for _, rid := range append(f.runtimes, rtID) {
		if open, err := f.q.RuntimeMaintenanceAdmissionOpen(ctx, rid); err != nil || !open {
			t.Fatalf("release failed to reopen exact generation: %v, %v", open, err)
		}
	}
}

func TestMaintenanceServerFailedFenceWrite(t *testing.T) {
	f := newMaintenanceFixture(t)
	ctx := context.Background()
	name := "maintenance_refuse_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	f.fixtures[0].Exec(t, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'isolated fixture write refusal'; END $$;
		CREATE TRIGGER %s BEFORE INSERT OR UPDATE ON daemon_maintenance_admission
		FOR EACH ROW WHEN (NEW.daemon_id = %s) EXECUTE FUNCTION %s();`,
		quoteIdent(name), quoteIdent(name), quoteLiteral(f.delivery.Command.DaemonID), quoteIdent(name)))
	drop := fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON daemon_maintenance_admission; DROP FUNCTION IF EXISTS %s();`, quoteIdent(name), quoteIdent(name))
	t.Cleanup(func() {
		if _, err := f.pool.Exec(context.Background(), drop); err != nil {
			t.Error(err)
		}
	})
	if err := f.repo.Prepare(ctx, f.delivery); err == nil {
		t.Fatal("first fence write refusal reported success")
	}
	if n := f.fixtures[0].Count(t, `SELECT count(*) FROM daemon_maintenance_admission WHERE daemon_id=$1`, f.delivery.Command.DaemonID); n != 0 {
		t.Fatal("refused transaction partially committed")
	}
	f.fixtures[0].Exec(t, drop)
	if err := f.repo.Prepare(ctx, f.delivery); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Complete(ctx, f.result(f.delivery)); err != nil {
		t.Fatal(err)
	}
	release := f.delivery
	release.Command.ID, release.Command.Operation, release.Command.Generation, release.Command.ExpiresAt = uuid.NewString(), "release", 1, time.Time{}
	if err := f.repo.Prepare(ctx, release); err != nil {
		t.Fatal(err)
	}
	f.fixtures[0].Exec(t, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'isolated fixture write refusal'; END $$;
		CREATE TRIGGER %s BEFORE UPDATE ON daemon_maintenance_admission
		FOR EACH ROW WHEN (NEW.daemon_id = %s) EXECUTE FUNCTION %s();`,
		quoteIdent(name), quoteIdent(name), quoteLiteral(f.delivery.Command.DaemonID), quoteIdent(name)))
	if err := f.repo.Complete(ctx, f.result(release)); err == nil {
		t.Fatal("failed repository release write reported success")
	}
	if open, err := f.q.RuntimeMaintenanceAdmissionOpen(ctx, f.runtimes[0]); err != nil || open {
		t.Fatalf("failed durable release reopened admission: %v, %v", open, err)
	}
	f.fixtures[0].Exec(t, drop)
	if err := f.repo.Complete(ctx, f.result(release)); err != nil {
		t.Fatal("failed commit prevented identical explicit result replay:", err)
	}
}

func TestMaintenanceServerRetryAndReclaimWaitForAcquisition(t *testing.T) {
	for _, operation := range []string{"single retry", "batch retry", "single reclaim", "batch reclaim", "delivery"} {
		t.Run(operation, func(t *testing.T) {
			f := newMaintenanceFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			status := "deferred"
			if strings.Contains(operation, "reclaim") || operation == "delivery" {
				status = "dispatched"
			}

			id := f.task(t, 0, testutil.Cols{"status": status, "fire_at": testutil.Raw("now()-interval '10 minutes'"),
				"dispatched_at": testutil.Raw("now()-interval '10 minutes'"), "prepare_lease_expires_at": testutil.Raw("now()-interval '1 minute'")})
			before, err := f.q.GetAgentTask(ctx, util.MustParseUUID(id))
			if err != nil {
				t.Fatal(err)
			}
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			daemon := f.delivery.Command.DaemonID
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('maintenance:' || $1,0))`, daemon); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				var count int
				var err error
				switch operation {
				case "single retry":
					var rows []db.AgentTaskQueue
					rows, err = f.q.PromoteDueDeferredTasksForRuntime(ctx, db.PromoteDueDeferredTasksForRuntimeParams{RuntimeID: f.runtimes[0], RuntimeStaleSecs: RuntimeClaimFreshnessSeconds})
					count = len(rows)
				case "batch retry":
					var rows []db.AgentTaskQueue
					rows, err = f.q.PromoteDueDeferredTasksForRuntimes(ctx, db.PromoteDueDeferredTasksForRuntimesParams{RuntimeIds: f.runtimes, RuntimeStaleSecs: RuntimeClaimFreshnessSeconds})
					count = len(rows)
				case "single reclaim":
					_, err = f.q.ReclaimStaleDispatchedTaskForRuntime(ctx, db.ReclaimStaleDispatchedTaskForRuntimeParams{RuntimeID: f.runtimes[0],
						RuntimeStaleSecs: RuntimeClaimFreshnessSeconds, ClaimRecoverySecs: 1, PrepareLeaseSecs: prepareLeaseDuration.Seconds()})
					if errors.Is(err, pgx.ErrNoRows) {
						err = nil
					} else if err == nil {
						count = 1
					}
				case "batch reclaim":
					var rows []db.AgentTaskQueue
					rows, err = f.q.ReclaimStaleDispatchedTasksForRuntimes(ctx, db.ReclaimStaleDispatchedTasksForRuntimesParams{RuntimeIds: f.runtimes,
						RuntimeStaleSecs: RuntimeClaimFreshnessSeconds, ClaimRecoverySecs: 1, PrepareLeaseSecs: prepareLeaseDuration.Seconds(), MaxTasks: 10})
					count = len(rows)
				case "delivery":
					_, err = f.service.FinalizeTaskClaim(ctx, before, db.CreateTaskTokenParams{TokenHash: uuid.NewString(), TaskID: before.ID,
						AgentID: before.AgentID, WorkspaceID: util.MustParseUUID(f.fixtures[0].WorkspaceID), UserID: util.MustParseUUID(f.fixtures[0].UserID),
						ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}, nil, false, nil, nil)
					if err == nil {
						count = 1
					} else if strings.Contains(err.Error(), "maintenance prevents new task delivery") {
						err = nil
					}
				}
				if err == nil && count != 0 {
					err = fmt.Errorf("%s admitted %d after closure", operation, count)
				}
				done <- err
			}()
			waitMaintenanceBlocked(t, ctx, f.pool, tx, done)
			if _, err := tx.Exec(ctx, `INSERT INTO daemon_maintenance_admission(daemon_id,state) VALUES($1,'{"closed":true}')`, daemon); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			after, err := f.q.GetAgentTask(ctx, before.ID)
			if err != nil || before.Status != after.Status || !before.DispatchedAt.Time.Equal(after.DispatchedAt.Time) {
				t.Fatalf("queue or reservation mutated: before=%+v, after=%+v, err=%v", before, after, err)
			}
		})
	}
}

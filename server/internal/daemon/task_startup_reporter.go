package daemon

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

const taskStartupQueueCapacity = 256

type taskStartupReport struct {
	taskID   string
	claim    string
	phase    protocol.TaskStartupPhase
	disabled *atomic.Bool
}

// One daemon-owned worker bounds reporting resources independently of task
// throughput. Diagnostics never hold an execution slot, even during an outage.
type taskStartupReporter struct {
	client *Client
	logger *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	closed  bool
	pending chan taskStartupReport
}

func newTaskStartupReporter(client *Client, logger *slog.Logger) *taskStartupReporter {
	ctx, cancel := context.WithCancel(context.Background())
	r := &taskStartupReporter{
		client: client, logger: logger, ctx: ctx, cancel: cancel,
		done: make(chan struct{}), pending: make(chan taskStartupReport, taskStartupQueueCapacity),
	}
	go r.run()
	return r
}

func (r *taskStartupReporter) enqueue(report taskStartupReport) {
	if report.disabled.Load() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		select {
		case r.pending <- report:
			return
		default:
		}
	}
	if report.disabled.CompareAndSwap(false, true) {
		r.logger.Warn("task startup reporting unavailable; discarding task diagnostics",
			"task_id", report.taskID, "phase", report.phase.Phase, "reporter_closed", r.closed)
	}
}

func (r *taskStartupReporter) run() {
	defer close(r.done)
	for report := range r.pending {
		if report.disabled.Load() {
			continue
		}
		ctx, cancel := context.WithTimeout(r.ctx, 5*time.Second)
		err := r.client.postJSON(ctx, "/api/daemon/tasks/"+report.taskID+"/startup", map[string]any{
			"dispatched_at": report.claim, "phase": report.phase,
		}, nil)
		cancel()
		if err != nil {
			r.logger.Warn("failed to report task startup phase",
				"task_id", report.taskID, "phase", report.phase.Phase, "error", err)
			var reqErr *requestError
			if r.ctx.Err() != nil || errors.As(err, &reqErr) && (reqErr.StatusCode == http.StatusNotFound ||
				reqErr.StatusCode == http.StatusUnauthorized || reqErr.StatusCode == http.StatusForbidden ||
				reqErr.StatusCode == http.StatusConflict) {
				report.disabled.Store(true)
			}
		}
	}
}

func (r *taskStartupReporter) close() {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.pending)
	}
	r.mu.Unlock()
	timer := time.AfterFunc(5*time.Second, r.cancel)
	<-r.done
	timer.Stop()
	r.cancel()
}

func (r *taskPhaseRecorder) startReporting(reporter *taskStartupReporter, task Task) {
	if reporter == nil || task.DispatchedAt == "" {
		return
	}
	disabled := new(atomic.Bool)
	r.report = func(phase protocol.TaskStartupPhase) {
		if phase.Valid() {
			reporter.enqueue(taskStartupReport{task.ID, task.DispatchedAt, phase, disabled})
		}
	}
}

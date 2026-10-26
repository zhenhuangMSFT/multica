package daemon

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestTaskPhaseRecorderReportsOriginalSamples(t *testing.T) {
	type report struct {
		DispatchedAt string                    `json:"dispatched_at"`
		Phase        protocol.TaskStartupPhase `json:"phase"`
	}
	reports := make(chan report, 9)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/daemon/tasks/task-1/startup" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body report
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		reports <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	now := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	recorder := newTaskPhaseRecorder(testTaskPhaseLogger(), func() time.Time { return now })
	claim := now.Add(-time.Second).Format(time.RFC3339Nano)
	reporter := newTaskStartupReporter(NewClient(server.URL), testTaskPhaseLogger())
	recorder.startReporting(reporter, Task{ID: "task-1", DispatchedAt: claim})
	recorder.Mark(taskPhaseClaimed)
	now = now.Add(15 * time.Second)
	recorder.Mark(taskPhaseFirstOutputReceived)
	recorder.Mark(taskPhaseFirstOutputReceived)
	reporter.close()
	recorder.Mark(taskPhaseFinished)
	if len(reports) != 2 {
		t.Fatalf("reports = %d, want 2", len(reports))
	}
	first, output := <-reports, <-reports
	if first.DispatchedAt != claim || first.Phase.Phase != "claimed" ||
		output.Phase.TotalElapsedMS != 15000 || !output.Phase.ObservedAt.Equal(now) {
		t.Fatalf("unexpected original samples: %#v %#v", first, output)
	}
}

func TestTaskPhaseRecorderReportingCompatibility(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
			}))
			defer server.Close()
			recorder := newTaskPhaseRecorder(testTaskPhaseLogger(), time.Now)
			reporter := newTaskStartupReporter(NewClient(server.URL), testTaskPhaseLogger())
			recorder.startReporting(reporter, Task{ID: "task-1", DispatchedAt: time.Now().Format(time.RFC3339Nano)})
			recorder.Mark(taskPhaseClaimed)
			recorder.Mark(taskPhaseFinished)
			reporter.close()
			want := int32(1)
			if status == http.StatusInternalServerError {
				want = 2
			}
			if calls.Load() != want {
				t.Fatalf("requests = %d, want %d", calls.Load(), want)
			}
		})
	}
	recorder := newTaskPhaseRecorder(testTaskPhaseLogger(), time.Now)
	recorder.startReporting(nil, Task{})
	recorder.Mark(taskPhaseClaimed)
}

func TestTaskPhaseRecorderReportingDoesNotBlockLaunch(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	defer close(release)
	recorder := newTaskPhaseRecorder(testTaskPhaseLogger(), time.Now)
	reporter := newTaskStartupReporter(NewClient(server.URL), testTaskPhaseLogger())
	recorder.startReporting(reporter, Task{ID: "task-1", DispatchedAt: time.Now().Format(time.RFC3339Nano)})
	recorder.Mark(taskPhaseClaimed)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("reporter did not start")
	}
	marked := make(chan struct{})
	go func() {
		for _, phase := range []taskPhase{
			taskPhasePrepareStarted, taskPhaseSkillsReady, taskPhaseEnvironmentReady, taskPhaseRuntimeStarted,
			taskPhaseFirstOutputReceived, taskPhaseFirstToolUse, taskPhaseTurnCompleted, taskPhaseFinished,
		} {
			recorder.Mark(phase)
		}
		close(marked)
	}()
	select {
	case <-marked:
	case <-time.After(time.Second):
		t.Fatal("startup marking blocked on reporting")
	}
	// A non-responsive server must not add nine request timeouts at task exit.
	start := time.Now()
	reporter.close()
	if elapsed := time.Since(start); elapsed > 7*time.Second {
		t.Fatalf("flush exceeded its total budget: %v", elapsed)
	}
}

func TestTaskStartupReporterBoundsQueueWithoutBlockingTasks(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	var logs bytes.Buffer
	reporter := newTaskStartupReporter(NewClient(server.URL), slog.New(slog.NewTextHandler(&logs, nil)))
	defer reporter.close()
	defer close(release)
	mark := func(id string) {
		recorder := newTaskPhaseRecorder(testTaskPhaseLogger(), time.Now)
		recorder.startReporting(reporter, Task{ID: id, DispatchedAt: time.Now().Format(time.RFC3339Nano)})
		recorder.Mark(taskPhaseClaimed)
	}
	mark("blocking")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("reporter did not start")
	}
	for range taskStartupQueueCapacity + 10 {
		mark("overflow")
	}
	if len(reporter.pending) != taskStartupQueueCapacity {
		t.Fatalf("queued %d samples, want bounded capacity %d", len(reporter.pending), taskStartupQueueCapacity)
	}
	if !strings.Contains(logs.String(), "discarding task diagnostics") {
		t.Fatal("queue overflow was not logged")
	}
}

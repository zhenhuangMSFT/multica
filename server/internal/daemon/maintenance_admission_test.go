package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var maintenanceTestNow = time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)

func maintenanceTestDaemon(t *testing.T) (*Daemon, *maintenanceFileStore) {
	t.Helper()
	store := &maintenanceFileStore{path: filepath.Join(t.TempDir(), "admission.jsonl"), host: "fixture-host", profile: "fixture"}
	d := &Daemon{}
	if err := d.initializeMaintenance(store); err != nil {
		t.Fatal(err)
	}
	return d, store
}

func acquireTestMaintenance(t *testing.T, d *Daemon, expected uint64) maintenanceRecord {
	t.Helper()
	record, err := d.acquireMaintenance("executor", expected, maintenanceTestNow.Add(time.Hour), maintenanceTestNow)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestMaintenanceAdmissionAcquireReleaseAndUpdateIsolation(t *testing.T) {
	d, _ := maintenanceTestDaemon(t)
	record := acquireTestMaintenance(t, d, 0)
	if record.Generation != 1 || !d.observeMaintenance().Blocked || d.tryEnterClaim() {
		t.Fatal("acquisition did not fence new claims")
	}
	if d.trySetClaimBarrier() || d.tryBeginServerUpdate(context.Background()) != serverUpdateRuntimeBusy {
		t.Fatal("update paths crossed maintenance ownership")
	}
	d.releaseClaimBarrier()
	if d.tryEnterClaim() {
		t.Fatal("transient barrier release cleared durable maintenance")
	}
	if err := d.releaseMaintenance("executor", 1, maintenanceTestNow); err != nil {
		t.Fatal(err)
	}
	if !d.tryEnterClaim() {
		t.Fatal("successful explicit durable release did not reopen")
	}
	d.exitClaim()
	if d.observeMaintenance().CompleteDrain {
		t.Fatal("local barrier claimed full drain")
	}
}

func TestMaintenanceAdmissionInflightAndResourceWaitPreserved(t *testing.T) {
	d, _ := maintenanceTestDaemon(t)
	if !d.tryEnterClaim() {
		t.Fatal("initial claim refused")
	}
	acquireTestMaintenance(t, d, 0)
	observation := d.observeMaintenance()
	if observation.ClaimsInFlight != 1 || observation.LocalQuiescent {
		t.Fatal("inflight claim disappeared at acquisition")
	}
	// Mirror the poller's ownership handoff: active increments BEFORE exitClaim.
	d.activeTasks.Add(1)
	d.exitClaim()
	d.resourceWaitTasks.Add(1)
	observation = d.observeMaintenance()
	if observation.ActiveTasks != 1 || observation.ResourceWait != 1 || observation.LocalQuiescent {
		t.Fatal("resource wait was mistaken for idle or cancelled")
	}
	d.resourceWaitTasks.Add(-1)
	d.runningTasks.Add(1)
	if d.observeMaintenance().LocalQuiescent {
		t.Fatal("resource-wait to provider transition opened a drain gap")
	}
	d.runningTasks.Add(-1)
	d.activeTasks.Add(-1)
	if !d.observeMaintenance().LocalQuiescent || d.tryEnterClaim() {
		t.Fatal("local quiescence must retain held admission")
	}
}

func TestMaintenanceAdmissionAcquireClaimRace(t *testing.T) {
	for i := 0; i < 100; i++ {
		d, _ := maintenanceTestDaemon(t)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var claimed bool
		var acquireErr error
		go func() { defer wg.Done(); <-start; claimed = d.tryEnterClaim() }()
		go func() {
			defer wg.Done()
			<-start
			_, acquireErr = d.acquireMaintenance("executor", 0, maintenanceTestNow.Add(time.Hour), maintenanceTestNow)
		}()
		close(start)
		wg.Wait()
		if acquireErr != nil || d.tryEnterClaim() {
			t.Fatalf("claim crossed completed acquire: %v", acquireErr)
		}
		want := 0
		if claimed {
			want = 1
		}
		if d.observeMaintenance().ClaimsInFlight != want {
			t.Fatal("pre-existing claim lost in race")
		}
		if claimed {
			d.exitClaim()
		}
	}
}

func TestMaintenanceAdmissionExpiryRecoveryAndStaleRelease(t *testing.T) {
	d, _ := maintenanceTestDaemon(t)
	acquireTestMaintenance(t, d, 0)
	if _, err := d.recoverMaintenance("next", 1, maintenanceTestNow.Add(time.Hour), maintenanceTestNow); err == nil {
		t.Fatal("unexpired holder stolen without restart")
	}
	later := maintenanceTestNow.Add(2 * time.Hour)
	if d.tryEnterClaim() {
		t.Fatal("expired holder silently reopened admission")
	}
	if err := d.releaseMaintenance("executor", 1, later); err == nil {
		t.Fatal("expired holder released admission instead of explicit generation recovery")
	}
	record, err := d.recoverMaintenance("next", 1, later.Add(time.Hour), later)
	if err != nil || record.Generation != 2 {
		t.Fatalf("explicit expiry recovery failed: %v", err)
	}
	for _, release := range []struct {
		holder string
		gen    uint64
	}{{"executor", 1}, {"executor", 2}, {"next", 1}} {
		if err := d.releaseMaintenance(release.holder, release.gen, later); err == nil {
			t.Fatal("stale holder/generation released maintenance")
		}
	}
	if err := d.releaseMaintenance("next", 2, later); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceAdmissionCrashReloadAndGenerationHistory(t *testing.T) {
	d, store := maintenanceTestDaemon(t)
	acquireTestMaintenance(t, d, 0)
	reloaded := &Daemon{}
	if err := reloaded.initializeMaintenance(store); err != nil || reloaded.tryEnterClaim() {
		t.Fatalf("restart reopened held admission: %v", err)
	}
	if err := reloaded.releaseMaintenance("executor", 1, maintenanceTestNow); err == nil {
		t.Fatal("restart allowed pre-crash holder release without explicit recovery")
	}
	record, err := reloaded.recoverMaintenance("recovery", 1, maintenanceTestNow.Add(time.Hour), maintenanceTestNow)
	if err != nil || record.Generation != 2 {
		t.Fatalf("restart recovery failed: %v", err)
	}
	if err := reloaded.releaseMaintenance("executor", 1, maintenanceTestNow); err == nil {
		t.Fatal("pre-crash generation retained release authority")
	}
	if err := reloaded.releaseMaintenance("recovery", 2, maintenanceTestNow); err != nil {
		t.Fatal(err)
	}
	open := &Daemon{}
	if err := open.initializeMaintenance(store); err != nil || open.observeMaintenance().Blocked {
		t.Fatalf("durable release did not survive reload: %v", err)
	}
	if _, err := open.acquireMaintenance("stale", 0, maintenanceTestNow.Add(time.Hour), maintenanceTestNow); err == nil {
		t.Fatal("generation reset on restart")
	}
	acquireTestMaintenance(t, open, 2)
}

type failingMaintenanceStore struct {
	maintenancePersistence
	fail string
}

func (s *failingMaintenanceStore) fence() error {
	if s.fail == "fence" {
		return errors.New("fixture fence failure")
	}
	return s.maintenancePersistence.fence()
}
func (s *failingMaintenanceStore) append(r maintenanceRecord) error {
	if s.fail == "append" {
		return errors.New("fixture persistence failure")
	}
	if s.fail == "after_append" {
		if err := s.maintenancePersistence.append(r); err != nil {
			return err
		}
		return errors.New("fixture ambiguous acknowledgement")
	}
	return s.maintenancePersistence.append(r)
}
func (s *failingMaintenanceStore) unfence() error {
	if s.fail == "unfence" {
		return errors.New("fixture unfence failure")
	}
	return s.maintenancePersistence.unfence()
}

func TestMaintenanceAdmissionFailedAcquisitionPersistence(t *testing.T) {
	for _, failure := range []string{"fence", "append", "after_append"} {
		t.Run(failure, func(t *testing.T) {
			d, store := maintenanceTestDaemon(t)
			d.maintenance.store = &failingMaintenanceStore{store, failure}
			if _, err := d.acquireMaintenance("executor", 0, maintenanceTestNow.Add(time.Hour), maintenanceTestNow); err == nil {
				t.Fatal("failed persistence acknowledged acquisition")
			}
			if d.tryEnterClaim() || d.observeMaintenance().PersistenceFault == nil || d.observeMaintenance().LocalQuiescent {
				t.Fatal("persistence failure did not latch closed")
			}
			d.releaseClaimBarrier()
			if d.tryEnterClaim() || d.releaseMaintenance("executor", 1, maintenanceTestNow) == nil {
				t.Fatal("failure latch bypassed")
			}
			if failure != "fence" {
				reloaded := &Daemon{}
				err := reloaded.initializeMaintenance(store)
				if err == nil && reloaded.tryEnterClaim() {
					t.Fatal("ambiguous write reopened admission on reload")
				}
			}
		})
	}
}

func TestMaintenanceAdmissionFailedReleasePersistence(t *testing.T) {
	for _, failure := range []string{"append", "after_append", "unfence"} {
		t.Run(failure, func(t *testing.T) {
			d, store := maintenanceTestDaemon(t)
			acquireTestMaintenance(t, d, 0)
			d.maintenance.store = &failingMaintenanceStore{store, failure}
			if d.releaseMaintenance("executor", 1, maintenanceTestNow) == nil || d.tryEnterClaim() {
				t.Fatal("failed release reopened admission")
			}
			reloaded := &Daemon{}
			if err := reloaded.initializeMaintenance(store); err != nil || reloaded.tryEnterClaim() {
				t.Fatalf("release failure fence lost on reload: %v", err)
			}
			record, err := reloaded.recoverMaintenance("repair", 1, maintenanceTestNow.Add(time.Hour), maintenanceTestNow)
			if err != nil || record.Generation != 2 {
				t.Fatalf("explicit recovery from durable fence failed: %v", err)
			}
		})
	}
}

func TestMaintenanceAdmissionRejectsInvalidJournalAndBinding(t *testing.T) {
	for _, failure := range []string{"torn", "host", "profile", "duplicate", "unknown", "missing", "generation", "oversized", "null", "casing", "empty"} {
		t.Run(failure, func(t *testing.T) {
			d, store := maintenanceTestDaemon(t)
			record := acquireTestMaintenance(t, d, 0)
			data, _ := json.Marshal(record)
			switch failure {
			case "torn":
				data = data[:len(data)/2]
			case "host":
				store.host = "other-host"
			case "profile":
				store.profile = "other-profile"
			case "duplicate":
				data = append([]byte(`{"held":false,`), data[1:]...)
			case "unknown":
				data = append([]byte(`{"extra":1,`), data[1:]...)
			case "missing":
				data = []byte(`{}`)
			case "generation":
				record.Generation = 3
				data, _ = json.Marshal(record)
			case "oversized":
				data = make([]byte, maintenanceJournalLimit+1)
			case "null":
				data = []byte(`{"version":1,"host":"fixture-host","profile":"fixture","holder":null,"generation":1,"held":false,"expiresAt":"0001-01-01T00:00:00Z"}`)
			case "casing":
				data = []byte(`{"Version":1,"host":"fixture-host","profile":"fixture","holder":"executor","generation":1,"held":true,"expiresAt":"2026-10-22T09:00:00Z"}`)
			case "empty":
				data = nil
			}

			if failure != "torn" && failure != "empty" {
				data = append(data, '\n')
			}
			if err := os.WriteFile(store.path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			reloaded := &Daemon{}
			if err := reloaded.initializeMaintenance(store); err == nil || reloaded.tryEnterClaim() {
				t.Fatal("invalid journal/binding failed open")
			}
		})
	}
}

func TestMaintenanceAdmissionCrashAtFenceBeforeJournal(t *testing.T) {
	d, store := maintenanceTestDaemon(t)
	if err := store.fence(); err != nil {
		t.Fatal(err)
	}
	reloaded := &Daemon{}
	if err := reloaded.initializeMaintenance(store); err == nil || reloaded.tryEnterClaim() {
		t.Fatal("fence with missing journal must require explicit repair, not initial-open fallback")
	}
	// A prior released history provides a known recovery generation.
	if err := store.unfence(); err != nil {
		t.Fatal(err)
	}
	acquireTestMaintenance(t, d, 0)
	if err := d.releaseMaintenance("executor", 1, maintenanceTestNow); err != nil {
		t.Fatal(err)
	}
	if err := store.fence(); err != nil {
		t.Fatal(err)
	}
	reloaded = &Daemon{}
	if err := reloaded.initializeMaintenance(store); err != nil || reloaded.tryEnterClaim() {
		t.Fatalf("pre-append crash opened prior released history: %v", err)
	}
	if _, err := reloaded.recoverMaintenance("recovery", 1, maintenanceTestNow.Add(time.Hour), maintenanceTestNow); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceAdmissionTransientBarrierAndInvalidInput(t *testing.T) {
	d, _ := maintenanceTestDaemon(t)
	d.pauseClaims = true
	if _, err := d.acquireMaintenance("executor", 0, maintenanceTestNow.Add(time.Hour), maintenanceTestNow); err == nil {
		t.Fatal("maintenance overlapped transient update barrier")
	}
	d.releaseClaimBarrier()
	for _, holder := range []string{"", "bad\nholder", " padded "} {
		if _, err := d.acquireMaintenance(holder, 0, maintenanceTestNow.Add(time.Hour), maintenanceTestNow); err == nil {
			t.Fatal("invalid holder accepted")
		}
	}
	if _, err := d.acquireMaintenance("executor", 0, maintenanceTestNow, maintenanceTestNow); err == nil {
		t.Fatal("expired acquisition accepted")
	}
	if d.observeMaintenance().Blocked {
		t.Fatal("invalid input mutated admission")
	}
}

func TestMaintenanceAdmissionQueuePreservation(t *testing.T) {
	requests := make(chan string, 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tasks":[]}`))
	}))
	defer server.Close()
	d := New(Config{ServerBaseURL: server.URL, MaxConcurrentTasks: 1, PollInterval: time.Millisecond},
		slog.New(slog.NewTextHandler(noopWriter{}, nil)))
	d.workspaces["fixture"] = &workspaceState{runtimeIDs: []string{"fixture-runtime"}}
	store := &maintenanceFileStore{path: filepath.Join(t.TempDir(), "admission.jsonl"), host: "fixture-host", profile: "fixture"}
	if err := d.initializeMaintenance(store); err != nil {
		t.Fatal(err)
	}
	acquireTestMaintenance(t, d, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var tasks sync.WaitGroup
	sem := newTaskSlotSemaphore(1)
	go func() {
		defer close(done)
		d.runBatchPoller(ctx, ctx, sem, make(chan struct{}, 1), &tasks)
	}()
	defer func() { cancel(); <-done; tasks.Wait() }()
	select {
	case path := <-requests:
		t.Fatalf("held poller contacted server (could claim/cancel/reassign queued work): %s", path)
	case <-time.After(100 * time.Millisecond):
	}
	if err := d.releaseMaintenance("executor", 1, maintenanceTestNow); err != nil {
		t.Fatal(err)
	}
	select {
	case path := <-requests:
		if path != "/api/daemon/tasks/claim" {
			t.Fatalf("unexpected server mutation: %s", path)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("real batch poller never resumed after durable release")
	}
}

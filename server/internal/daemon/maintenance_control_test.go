package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	lifecyclev1 "github.com/multica-ai/multica/server/pkg/lifecycle/v1"
)

func maintenanceControlFixture(t *testing.T) (*Daemon, *maintenanceFileStore, string) {
	t.Helper()
	d, store := maintenanceTestDaemon(t)
	d.cfg.DaemonID = "fixture-daemon"
	dir := filepath.Join(filepath.Dir(store.path), "commands")
	if err := d.loadMaintenanceReceipts(dir); err != nil {
		t.Fatal(err)
	}
	return d, store, dir
}

func maintenanceDelivery(operation string, generation uint64) lifecyclev1.MaintenanceDelivery {
	now := time.Now().UTC()
	command := lifecyclev1.MaintenanceCommand{ID: uuid.NewString(), DaemonID: "fixture-daemon", Host: "fixture-host",
		Profile: "fixture", Operation: operation, Generation: generation, IssuedAt: now}
	if operation != "status" {
		command.Holder = "executor"
	}
	if operation == "acquire" || operation == "recover" {
		command.ExpiresAt = now.Add(time.Hour)
	}
	return lifecyclev1.MaintenanceDelivery{Command: command, OwnerID: "22222222-2222-4222-8222-222222222222", Scope: strings.Repeat("a", 64)}
}

func TestMaintenanceControlDurableReplayAndStaleRelease(t *testing.T) {
	d, store, dir := maintenanceControlFixture(t)
	acquire := maintenanceDelivery("acquire", 0)
	result := d.executeMaintenance(acquire, time.Now())
	if result.Code != "ok" || result.Status.Generation != 1 || !result.Status.Blocked || result.Status.CompleteDrain {
		t.Fatalf("%+v", result)
	}
	if replay := d.executeMaintenance(acquire, time.Now()); replay != result {
		t.Fatal("replay advanced generation or changed result")
	}
	changed := acquire
	changed.Command.Holder = "other"
	if got := d.executeMaintenance(changed, time.Now()); got.Code != "rejected" || got.Status.Generation != 1 {
		t.Fatalf("changed replay: %+v", got)
	}
	restarted := &Daemon{cfg: d.cfg}
	if err := restarted.initializeMaintenance(store); err != nil {
		t.Fatal(err)
	}
	if err := restarted.loadMaintenanceReceipts(dir); err != nil {
		t.Fatal(err)
	}
	if replay := restarted.executeMaintenance(acquire, time.Now()); replay != result {
		t.Fatal("durable replay lost after restart")
	}
	release := maintenanceDelivery("release", 1)
	if got := restarted.executeMaintenance(release, time.Now()); got.Code != "rejected" || !got.Status.Blocked {
		t.Fatalf("unrecovered release: %+v", got)
	}
	recover := maintenanceDelivery("recover", 1)
	if got := restarted.executeMaintenance(recover, time.Now()); got.Code != "ok" || got.Status.Generation != 2 {
		t.Fatalf("recovery: %+v", got)
	}
	stale := maintenanceDelivery("release", 1)
	if got := restarted.executeMaintenance(stale, time.Now()); got.Code != "rejected" || !got.Status.Blocked {
		t.Fatalf("stale release: %+v", got)
	}
	release = maintenanceDelivery("release", 2)
	if got := restarted.executeMaintenance(release, time.Now()); got.Code != "ok" || got.Status.Blocked {
		t.Fatalf("release: %+v", got)
	}
	third := &Daemon{cfg: d.cfg}
	if err := third.initializeMaintenance(store); err != nil {
		t.Fatal(err)
	}
	if err := third.loadMaintenanceReceipts(dir); err != nil {
		t.Fatal(err)
	}
	if third.observeMaintenance().Blocked {
		t.Fatal("completed history reopened a superseded intent")
	}
}

func TestMaintenanceControlCrashWithUnresolvedIntent(t *testing.T) {
	for _, operation := range []string{"acquire", "release"} {
		t.Run(operation, func(t *testing.T) {
			d, store, dir := maintenanceControlFixture(t)
			generation := uint64(0)
			if operation == "release" {
				acquire := maintenanceDelivery("acquire", 0)
				if got := d.executeMaintenance(acquire, time.Now()); got.Code != "ok" {
					t.Fatal(got)
				}
				generation = 1
			}
			delivery := maintenanceDelivery(operation, generation)
			if err := d.maintenanceReceipts.write(".intent", maintenanceReceipt{Delivery: delivery}); err != nil {
				t.Fatal(err)
			}
			if operation == "release" {
				if err := d.releaseMaintenance("executor", 1, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			restarted := &Daemon{cfg: d.cfg}
			if err := restarted.initializeMaintenance(store); err != nil {
				t.Fatal(err)
			}
			if err := restarted.loadMaintenanceReceipts(dir); err != nil {
				t.Fatal(err)
			}
			if !restarted.observeMaintenance().Blocked || restarted.tryEnterClaim() {
				t.Fatal("unresolved intent reopened after crash")
			}
			if got := restarted.executeMaintenance(delivery, time.Now()); got.Code != "outcome_unknown" {
				t.Fatal(got)
			}
			recovery := maintenanceDelivery("recover", generation)
			if got := restarted.executeMaintenance(recovery, time.Now()); got.Code != "ok" || !got.Status.Blocked {
				t.Fatalf("explicit recovery: %+v", got)
			}
			release := maintenanceDelivery("release", generation+1)
			if got := restarted.executeMaintenance(release, time.Now()); got.Code != "ok" {
				t.Fatal(got)
			}
			third := &Daemon{cfg: d.cfg}
			if err := third.initializeMaintenance(store); err != nil {
				t.Fatal(err)
			}
			if err := third.loadMaintenanceReceipts(dir); err != nil {
				t.Fatal(err)
			}
			if third.observeMaintenance().Blocked {
				t.Fatal("explicit newer generation did not supersede old uncertain intent")
			}
		})
	}
}

func TestMaintenanceControlInputAndPersistenceFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*lifecyclev1.MaintenanceDelivery)
	}{
		{"host", func(d *lifecyclev1.MaintenanceDelivery) { d.Command.Host = "other" }},
		{"profile", func(d *lifecyclev1.MaintenanceDelivery) { d.Command.Profile = "other" }},
		{"daemon", func(d *lifecyclev1.MaintenanceDelivery) { d.Command.DaemonID = "other" }},
		{"owner", func(d *lifecyclev1.MaintenanceDelivery) { d.OwnerID = "not-a-uuid" }},
		{"scope", func(d *lifecyclev1.MaintenanceDelivery) { d.Scope = "" }},
		{"expired-command", func(d *lifecyclev1.MaintenanceDelivery) { d.Command.IssuedAt = time.Now().Add(-3 * time.Minute) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, _ := maintenanceControlFixture(t)
			delivery := maintenanceDelivery("acquire", 0)
			tc.edit(&delivery)
			if got := d.executeMaintenance(delivery, time.Now()); got.Code != "target_mismatch" {
				t.Fatal(got)
			}
			if d.observeMaintenance().Blocked || len(d.maintenanceReceipts.entries) != 0 {
				t.Fatal("invalid target changed state")
			}
		})
	}
	for _, suffix := range []string{".intent", ".result"} {
		t.Run("failed-"+suffix, func(t *testing.T) {
			d, store, dir := maintenanceControlFixture(t)
			delivery := maintenanceDelivery("acquire", 0)
			if err := os.MkdirAll(filepath.Join(dir, delivery.Command.ID+suffix), 0700); err != nil {
				t.Fatal(err)
			}
			got := d.executeMaintenance(delivery, time.Now())
			if got.Code != "persistence_fault" || !got.Status.Blocked || !got.Status.PersistenceFault || d.tryEnterClaim() {
				t.Fatalf("%+v", got)
			}
			restarted := &Daemon{cfg: d.cfg}
			if err := restarted.initializeMaintenance(store); err == nil {
				if err := restarted.loadMaintenanceReceipts(dir); err == nil {
					t.Fatal("corrupt receipt storage accepted on reload")
				}
			}
		})
	}
}

func TestMaintenanceControlPollBoundedAndBackendOutage(t *testing.T) {
	d, _, _ := maintenanceControlFixture(t)
	var claims, reports atomic.Int32
	delivery := maintenanceDelivery("acquire", 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/claim") {
			claims.Add(1)
			if r.Header.Get("Authorization") != "Bearer fixture-token" {
				t.Error("missing daemon channel authentication")
			}
			_ = json.NewEncoder(w).Encode(delivery)
		} else {
			reports.Add(1)
			var result lifecyclev1.MaintenanceResult
			if err := lifecyclev1.DecodeMaintenanceJSON(r.Body, &result); err != nil || result.Validate() != nil || result.Code != "ok" {
				t.Errorf("%+v %v", result, err)
			}
			_ = json.NewEncoder(w).Encode(lifecyclev1.MaintenanceAcknowledgment{ID: delivery.Command.ID, Accepted: true})
		}
	}))
	d.client = NewClient(server.URL)
	d.client.SetToken("fixture-token")
	d.logger = maintenanceLogger()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); d.pollMaintenance(context.Background(), "fixture-runtime") }()
	}
	wg.Wait()
	server.Close()
	if claims.Load() != 1 || reports.Load() != 1 || !d.observeMaintenance().Blocked {
		t.Fatalf("claims=%d reports=%d", claims.Load(), reports.Load())
	}
	d.lastMaintenancePoll = time.Time{}
	d.pollMaintenance(context.Background(), "fixture-runtime")
	if !d.observeMaintenance().Blocked || d.observeMaintenance().Record.Generation != 1 {
		t.Fatal("backend outage reopened admission")
	}
}

func maintenanceLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

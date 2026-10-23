package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMaintenanceWithoutOptionalExtension(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusNotFound, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			d, store, receipts := maintenanceControlFixture(t)
			d.logger = maintenanceLogger()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			defer server.Close()
			d.client = NewClient(server.URL)
			d.client.SetToken("fixture")
			d.pollMaintenance(context.Background(), "fixture")
			if !d.tryEnterClaim() {
				t.Fatal("missing optional extension or unavailable control stopped normal claims")
			}
			d.exitClaim()
			observation := d.observeMaintenance()
			if observation.Blocked || observation.Record.Generation != 0 || observation.CompleteDrain {
				t.Fatalf("unrequested maintenance changed ordinary behavior: %+v", observation)
			}

			delivery := maintenanceDelivery("acquire", 0)
			if result := d.executeMaintenance(delivery, time.Now()); result.Code != "ok" {
				t.Fatal(result)
			}
			d.activeTasks.Add(1)
			d.resourceWaitTasks.Add(1)
			d.lastMaintenancePoll = time.Time{}
			d.pollMaintenance(context.Background(), "fixture")
			observation = d.observeMaintenance()
			if d.tryEnterClaim() || observation.Record.Generation != 1 ||
				observation.ActiveTasks != 1 || observation.ResourceWait != 1 || observation.CompleteDrain {
				t.Fatalf("unavailable extension released gate or lost active work: %+v", observation)
			}

			restarted := &Daemon{cfg: d.cfg}
			if err := restarted.initializeMaintenance(store); err != nil {
				t.Fatal(err)
			}
			if err := restarted.loadMaintenanceReceipts(receipts); err != nil {
				t.Fatal(err)
			}
			// Without even a delivery client or extension, restart cannot release
			// a persisted holder or reset its generation.
			restarted.pollMaintenance(context.Background(), "fixture")
			if restarted.tryEnterClaim() || restarted.observeMaintenance().Record.Generation != 1 {
				t.Fatal("restart without optional extension reopened held admission")
			}
		})
	}
}

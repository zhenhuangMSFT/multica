package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/daemoncontrol"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	lifecyclev1 "github.com/multica-ai/multica/server/pkg/lifecycle/v1"
)

func TestMaintenanceRejectedExpectationReconciliation(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set; requires isolated PostgreSQL fixture")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	for _, successor := range []bool{false, true} {
		name := "rejected-first-acquire"
		if successor {
			name = "rejected-successor-recovery"
		}
		t.Run(name, func(t *testing.T) {
			d, localStore, receipts := maintenanceControlFixture(t)
			daemonID := "reconciliation-" + uuid.NewString()
			d.cfg.DaemonID = daemonID
			root := testutil.New(pool, "", "")
			owner := root.User(t, "Reconciliation fixture", uuid.NewString()+"@example.test")
			ws := root.Workspace(t, "Reconciliation fixture", uuid.NewString())
			root.Member(t, ws, owner, "owner")
			fx := testutil.New(pool, ws, owner)
			runtimeID := fx.Runtime(t, "Reconciliation fixture", testutil.Cols{"daemon_id": daemonID})
			fx.Cleanup(t, `DELETE FROM daemon_maintenance_admission WHERE daemon_id=$1`, daemonID)
			sum := sha256.Sum256([]byte(runtimeID + ":" + ws + ":" + owner))
			repo := daemoncontrol.NewPostgresAdmission(pool)
			delivery := func(operation string, generation uint64, age time.Duration) lifecyclev1.MaintenanceDelivery {
				request := maintenanceDelivery(operation, generation)
				request.Command.DaemonID = daemonID
				request.Command.IssuedAt = time.Now().UTC().Add(-age)
				if operation == "acquire" || operation == "recover" {
					request.Command.ExpiresAt = request.Command.IssuedAt.Add(time.Hour)
				}
				request.OwnerID, request.Scope = owner, hex.EncodeToString(sum[:])
				return request
			}
			closed := func() {
				t.Helper()
				open, err := db.New(pool).RuntimeMaintenanceAdmissionOpen(ctx, util.MustParseUUID(runtimeID))
				if err != nil || open {
					t.Fatalf("server admission must remain closed: open=%v, err=%v", open, err)
				}
			}
			confirmed := func(want uint64, pending lifecyclev1.MaintenanceDelivery) {
				t.Helper()
				var generation, expected uint64
				err := pool.QueryRow(ctx, `SELECT (state->>'generation')::numeric,
					(state->'delivery'->'command'->>'generation')::numeric
					FROM daemon_maintenance_admission WHERE daemon_id=$1`, daemonID).Scan(&generation, &expected)
				if err != nil || generation != want || expected != pending.Command.Generation {
					t.Fatalf("confirmed generation changed with pending expectation: confirmed=%d, pending=%d, err=%v", generation, expected, err)
				}
			}
			actual := uint64(0)
			if successor {
				first := delivery("acquire", 0, 24*time.Minute)
				if err := repo.Prepare(ctx, first); err != nil {
					t.Fatal(err)
				}
				got := d.executeMaintenance(first, first.Command.IssuedAt)
				if got.Code != "ok" || got.Status.Generation != 1 {
					t.Fatal(got)
				}
				if err := repo.Complete(ctx, got); err != nil {
					t.Fatal(err)
				}
				actual = 1
				restarted := &Daemon{cfg: d.cfg}
				if err := restarted.initializeMaintenance(localStore); err != nil {
					t.Fatal(err)
				}
				if err := restarted.loadMaintenanceReceipts(receipts); err != nil {
					t.Fatal(err)
				}
				d = restarted
			}
			operation := "acquire"
			if successor {
				// First make the confirmed holder uncertain without receiving a result.
				pending := delivery("recover", actual, 18*time.Minute)
				if err := repo.Prepare(ctx, pending); err != nil {
					t.Fatal(err)
				}
				operation = "recover"
			}
			rejected := delivery(operation, actual+1, 12*time.Minute)
			if err := repo.Prepare(ctx, rejected); err != nil {
				t.Fatal(err)
			}
			closed()
			confirmed(actual, rejected)
			got := d.executeMaintenance(rejected, rejected.Command.IssuedAt)
			if got.Code != "rejected" || got.Status.Generation != actual {
				t.Fatalf("expected bound local generation rejection: %+v", got)
			}
			if err := repo.Complete(ctx, got); err != nil {
				t.Fatal(err)
			}
			closed()
			confirmed(actual, rejected)
			// Reconstruct the backend repository as well as local admission.
			repo = daemoncontrol.NewPostgresAdmission(pool)
			restarted := &Daemon{cfg: d.cfg}
			if err := restarted.initializeMaintenance(localStore); err != nil {
				t.Fatal(err)
			}
			if err := restarted.loadMaintenanceReceipts(receipts); err != nil {
				t.Fatal(err)
			}
			d = restarted
			corrected := delivery(operation, actual, 0)
			if err := repo.Prepare(ctx, corrected); err != nil {
				t.Fatalf("rejected expectation poisoned reconciliation at actual generation %d: %v", actual, err)
			}
			closed()
			confirmed(actual, corrected)
			got = d.executeMaintenance(corrected, corrected.Command.IssuedAt)
			if got.Code != "ok" || got.Status.Generation != actual+1 || !got.Status.Blocked {
				t.Fatalf("actual generation failed reconciliation: %+v", got)
			}
			if err := repo.Complete(ctx, got); err != nil {
				t.Fatal(err)
			}
			closed()
			confirmed(actual+1, corrected)
			if err := repo.Complete(ctx, d.executeMaintenance(rejected, rejected.Command.IssuedAt)); !errors.Is(err, daemoncontrol.ErrConflict) {
				t.Fatalf("late rejected result replaced current binding: %v", err)
			}
			if actual > 0 {
				stale := delivery("release", actual, 0)
				if err := repo.Prepare(ctx, stale); !errors.Is(err, daemoncontrol.ErrConflict) {
					t.Fatalf("stale release crossed reconciled generation: %v", err)
				}
				if result := d.executeMaintenance(stale, stale.Command.IssuedAt); result.Code != "rejected" {
					t.Fatalf("local stale release accepted: %+v", result)
				}
			}
			release := delivery("release", actual+1, 0)
			if err := repo.Prepare(ctx, release); err != nil {
				t.Fatal(err)
			}
			got = d.executeMaintenance(release, release.Command.IssuedAt)
			if got.Code != "ok" || got.Status.Blocked {
				t.Fatal(got)
			}
			if err := repo.Complete(ctx, got); err != nil {
				t.Fatal(err)
			}
			if open, err := db.New(pool).RuntimeMaintenanceAdmissionOpen(ctx, util.MustParseUUID(runtimeID)); err != nil || !open {
				t.Fatalf("bound confirmed release did not reopen: open=%v, err=%v", open, err)
			}
		})
	}
}

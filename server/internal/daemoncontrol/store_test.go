package daemoncontrol

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	lifecyclev1 "github.com/multica-ai/multica/server/pkg/lifecycle/v1"
	"github.com/redis/go-redis/v9"
)

func redisFixture(t *testing.T) (*RedisStore, lifecyclev1.MaintenanceDelivery) {
	t.Helper()
	url := os.Getenv("REDIS_MAINTENANCE_TEST_URL")
	if url == "" {
		t.Skip("REDIS_MAINTENANCE_TEST_URL not set; requires an isolated Redis fixture")
	}
	options, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	command := commandFixture()
	command.ID, command.DaemonID = uuid.NewString(), "fixture-"+uuid.NewString()
	delivery := lifecyclev1.MaintenanceDelivery{Command: command, OwnerID: fixtureOwner, Scope: strings.Repeat("a", 64)}
	t.Cleanup(func() {
		if err := client.Del(context.Background(), requestKey(command.ID), activeKey(command.DaemonID)).Err(); err != nil {
			t.Error(err)
		}
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	return NewRedisStore(client), delivery
}

func TestMaintenanceRedisReplayAndConcurrentDelivery(t *testing.T) {
	store, delivery := redisFixture(t)
	ctx := context.Background()
	request, err := store.Create(ctx, delivery)
	if err != nil || request.State != "pending" {
		t.Fatalf("%+v %v", request, err)
	}
	other := delivery
	other.Command.Holder = "different-request"
	if _, err := store.Create(ctx, other); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed payload: %v", err)
	}
	if _, err := store.Create(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	other = delivery
	other.Command.ID = uuid.NewString()
	if _, err := store.Create(ctx, other); !errors.Is(err, ErrConflict) {
		t.Fatalf("overlap: %v", err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			delivered, err := store.Deliver(ctx, delivery)
			if err != nil {
				t.Error(err)
			}
			if delivered {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("delivered %d times", successes.Load())
	}
	if pending, err := store.Pending(ctx, delivery.Command.DaemonID); err != nil || pending != nil {
		t.Fatalf("redelivery: %+v %v", pending, err)
	}
	reloaded := NewRedisStore(store.client)
	request, err = reloaded.Get(ctx, delivery.Command.ID)
	if err != nil || request.State != "delivered" {
		t.Fatalf("lost persisted delivery: %+v %v", request, err)
	}
	result := lifecyclev1.MaintenanceResult{Delivery: delivery, Code: "ok", Status: lifecyclev1.MaintenanceStatus{
		Host: delivery.Command.Host, Profile: delivery.Command.Profile, ObservedAt: time.Now().UTC(),
	}}
	forged := result
	forged.Delivery.OwnerID = fixtureOther
	if err := store.Complete(ctx, forged); !errors.Is(err, ErrConflict) {
		t.Fatalf("forged result accepted: %v", err)
	}
	if err := store.Complete(ctx, result); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, result); err != nil {
		t.Fatalf("identical result replay: %v", err)
	}
	forged = result
	forged.Code = "rejected"
	if err := store.Complete(ctx, forged); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed result: %v", err)
	}
	request, err = reloaded.Get(ctx, delivery.Command.ID)
	if err != nil || request.Validate() != nil || request.State != "completed" {
		t.Fatalf("completed: %+v %v", request, err)
	}
	if _, err := store.Create(ctx, other); err != nil {
		t.Fatalf("slot not released: %v", err)
	}
	t.Cleanup(func() { _ = store.client.Del(ctx, requestKey(other.Command.ID)).Err() })
}

func TestMaintenanceRedisExpiryAndPersistenceFailure(t *testing.T) {
	store, delivery := redisFixture(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	if err := store.client.HSet(ctx, requestKey(delivery.Command.ID), "state", "nonsense").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, delivery.Command.ID); err == nil {
		t.Fatal("corrupt persisted state accepted")
	}
	if err := store.client.HSet(ctx, requestKey(delivery.Command.ID), "state", "delivered").Err(); err != nil {
		t.Fatal(err)
	}
	delivery.Command.IssuedAt = time.Now().UTC().Add(-3 * time.Minute)
	payload := mustJSON(t, delivery)
	if err := store.client.HSet(ctx, requestKey(delivery.Command.ID), "delivery", payload).Err(); err != nil {
		t.Fatal(err)
	}
	request, err := store.Get(ctx, delivery.Command.ID)
	if err != nil || request.State != "indeterminate" {
		t.Fatalf("expired delivery: %+v %v", request, err)
	}
	if _, err := store.Create(ctx, delivery); !errors.Is(err, ErrConflict) {
		t.Fatal("expired command recreated")
	}
	if err := store.client.HSet(ctx, requestKey(delivery.Command.ID), "delivery", `{"unexpected":true}`).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, delivery.Command.ID); err == nil {
		t.Fatal("malformed persisted delivery accepted")
	}
	broken := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 50 * time.Millisecond})
	defer broken.Close()
	delivery.Command.IssuedAt = time.Now().UTC()
	failed := NewRedisStore(broken)
	if _, err := failed.Create(ctx, delivery); err == nil {
		t.Fatal("failed persistence acknowledged")
	}
	if _, err := failed.Deliver(ctx, delivery); err == nil {
		t.Fatal("failed delivery acknowledged")
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMaintenanceDaemonTokenScope(t *testing.T) {
	store, _ := redisFixture(t)
	cache := auth.NewDaemonTokenCache(store.client)
	control := NewControl(directoryFixture(), &fixtureStore{})
	for _, tc := range []struct {
		name, daemon, workspace string
		status                  int
	}{
		{"exact", "fixture-daemon", fixtureWorkspace, 204},
		{"wrong-daemon", "other-daemon", fixtureWorkspace, 403},
		{"wrong-workspace", "fixture-daemon", fixtureOther, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := "mdt_" + uuid.NewString()
			hash := auth.HashToken(token)
			cache.Set(context.Background(), hash, auth.DaemonTokenIdentity{WorkspaceID: tc.workspace, DaemonID: tc.daemon}, time.Minute)
			defer cache.Invalidate(context.Background(), hash)
			routes := chi.NewRouter()
			routes.Use(middleware.DaemonAuth(nil, nil, cache, nil))
			routes.Post("/api/daemon/runtimes/{runtimeId}/maintenance/claim", control.Claim)
			request := httptest.NewRequest("POST", "/api/daemon/runtimes/"+fixtureRuntime+"/maintenance/claim", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			routes.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("%d: %s", response.Code, response.Body)
			}
		})
	}
}

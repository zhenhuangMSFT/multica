package daemoncontrol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const fixtureOwner = "22222222-2222-4222-8222-222222222222"
const fixtureOther = "33333333-3333-4333-8333-333333333333"
const fixtureRuntime = "44444444-4444-4444-8444-444444444444"
const fixtureWorkspace = "55555555-5555-4555-8555-555555555555"

type fixtureDirectory struct {
	rows                []db.ListDaemonMaintenanceRegistrationsRow
	memberErr, queryErr error
}

func directoryFixture() *fixtureDirectory {
	return &fixtureDirectory{rows: []db.ListDaemonMaintenanceRegistrationsRow{{
		ID: util.MustParseUUID(fixtureRuntime), WorkspaceID: util.MustParseUUID(fixtureWorkspace),
		OwnerID: util.MustParseUUID(fixtureOwner), DaemonID: pgtype.Text{String: "fixture-daemon", Valid: true},
	}}}
}

func (d *fixtureDirectory) GetAgentRuntime(_ context.Context, id pgtype.UUID) (db.AgentRuntime, error) {
	row := d.rows[0]
	if row.ID != id {
		return db.AgentRuntime{}, pgx.ErrNoRows
	}
	return db.AgentRuntime{ID: row.ID, WorkspaceID: row.WorkspaceID, OwnerID: row.OwnerID, DaemonID: row.DaemonID}, d.queryErr
}
func (d *fixtureDirectory) ListDaemonMaintenanceRegistrations(context.Context, pgtype.Text) ([]db.ListDaemonMaintenanceRegistrationsRow, error) {
	return d.rows, d.queryErr
}
func (d *fixtureDirectory) GetMemberByUserAndWorkspace(_ context.Context, input db.GetMemberByUserAndWorkspaceParams) (db.Member, error) {
	return db.Member{UserID: input.UserID, WorkspaceID: input.WorkspaceID, Role: "admin"}, d.memberErr
}

type fixtureStore struct {
	created int
	request *protocol.MaintenanceRequest
	err     error
}

func (s *fixtureStore) Create(_ context.Context, d protocol.MaintenanceDelivery) (*protocol.MaintenanceRequest, error) {
	s.created++
	s.request = &protocol.MaintenanceRequest{Delivery: d, State: "pending"}
	return s.request, s.err
}
func (s *fixtureStore) Get(context.Context, string) (*protocol.MaintenanceRequest, error) {
	return s.request, s.err
}
func (s *fixtureStore) Pending(context.Context, string) (*protocol.MaintenanceRequest, error) {
	return s.request, s.err
}
func (s *fixtureStore) Deliver(context.Context, protocol.MaintenanceDelivery) (bool, error) {
	return true, s.err
}
func (s *fixtureStore) Complete(context.Context, protocol.MaintenanceResult) error { return s.err }

func commandFixture() protocol.MaintenanceCommand {
	return protocol.MaintenanceCommand{ID: "11111111-1111-4111-8111-111111111111", Operation: "status",
		DaemonID: "fixture-daemon", Host: "fixture-host", Profile: "fixture", IssuedAt: time.Now().UTC()}
}
func userToken(t *testing.T, user string) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": user, "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(auth.JWTSecret())
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// This DBTX supplies only a validated task-token row to the real Auth middleware.
// The maintenance directory and store remain isolated and have no live DB.
type taskAuthDB struct{}

func (taskAuthDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected exec")
}
func (taskAuthDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected query")
}
func (taskAuthDB) QueryRow(context.Context, string, ...any) pgx.Row { return taskAuthRow{} }

type taskAuthRow struct{}

func (taskAuthRow) Scan(dest ...any) error {
	if len(dest) != 8 {
		return errors.New("unexpected task-token projection")
	}
	for _, i := range []int{0, 2, 3, 4, 5} {
		*dest[i].(*pgtype.UUID) = util.MustParseUUID(fixtureOwner)
	}
	*dest[1].(*string) = "fixture-hash"
	*dest[6].(*pgtype.Timestamptz) = pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}
	*dest[7].(*pgtype.Timestamptz) = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	return nil
}

type fixtureDeadlineWriter struct{ http.ResponseWriter }

func (w fixtureDeadlineWriter) SetReadDeadline(deadline time.Time) error {
	if _, ok := w.ResponseWriter.(*httptest.ResponseRecorder); ok {
		return nil // Recorder has no socket; real-server fixtures use the actual deadline.
	}
	return http.NewResponseController(w.ResponseWriter).SetReadDeadline(deadline)
}

func controlRoutes(control *Control) http.Handler {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(db.New(taskAuthDB{}), nil, nil, nil))
		r.Post("/api/runtimes/{runtimeId}/maintenance", control.Submit)
		r.Get("/api/runtimes/{runtimeId}/maintenance/{requestId}", control.Get)
	})
	r.Group(func(r chi.Router) {
		r.Use(middleware.DaemonAuth(nil, nil, nil, nil))
		r.Post("/api/daemon/runtimes/{runtimeId}/maintenance/claim", control.Claim)
		r.Post("/api/daemon/runtimes/{runtimeId}/maintenance/{requestId}/result", control.Report)
	})
	logged := middleware.RequestLogger(r)
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		logged.ServeHTTP(fixtureDeadlineWriter{w}, request)
	})
}

func TestMaintenanceSlowBodyDeadline(t *testing.T) {
	store := &fixtureStore{}
	routes := controlRoutes(NewControl(directoryFixture(), store))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 50*time.Millisecond)
		defer cancel()
		routes.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer server.Close()
	connection, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = fmt.Fprintf(connection, "POST /api/runtimes/%s/maintenance HTTP/1.1\r\nHost: fixture\r\nAuthorization: Bearer %s\r\nContent-Length: 1000\r\nConnection: close\r\n\r\n{", fixtureRuntime, userToken(t, fixtureOwner))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 400 || time.Since(started) > time.Second || store.created != 0 {
		t.Fatalf("slow body escaped deadline: HTTP %d elapsed %s writes %d", response.StatusCode, time.Since(started), store.created)
	}
}

func TestMaintenanceAuthenticatedOwnerBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, token string
		status      int
	}{
		{"missing", "", http.StatusUnauthorized},
		{"invalid", "not-a-token", http.StatusUnauthorized},
		{"task-owner", "mat_fixture", http.StatusForbidden},
		{"cloud", "mcn_fixture", http.StatusUnauthorized},
		{"admin-not-owner", userToken(t, fixtureOther), http.StatusForbidden},
		{"owner", userToken(t, fixtureOwner), http.StatusAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fixtureStore{}
			body, _ := json.Marshal(commandFixture())
			request := httptest.NewRequest(http.MethodPost, "/api/runtimes/"+fixtureRuntime+"/maintenance", bytes.NewReader(body))
			if tc.token != "" {
				request.Header.Set("Authorization", "Bearer "+tc.token)
			}
			request.Header.Set("X-User-ID", fixtureOwner)
			request.Header.Set("X-Actor-Source", "member")
			response := httptest.NewRecorder()
			controlRoutes(NewControl(directoryFixture(), store)).ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("%d: %s", response.Code, response.Body)
			}
			if (store.created == 1) != (tc.status == http.StatusAccepted) {
				t.Fatal("unauthorized store access")
			}
		})
	}
}

func TestMaintenanceOwnershipAcrossWorkspaces(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(*fixtureDirectory)
		status int
	}{
		{"same-owner", func(d *fixtureDirectory) {}, http.StatusAccepted},
		{"mixed", func(d *fixtureDirectory) { d.rows[1].OwnerID = util.MustParseUUID(fixtureOther) }, http.StatusForbidden},
		{"unresolved", func(d *fixtureDirectory) { d.rows[1].OwnerID = pgtype.UUID{} }, http.StatusForbidden},
		{"missing-membership", func(d *fixtureDirectory) { d.memberErr = pgx.ErrNoRows }, http.StatusForbidden},
		{"database-error", func(d *fixtureDirectory) { d.queryErr = errors.New("private backend error") }, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := directoryFixture()
			other := directory.rows[0]
			other.ID, other.WorkspaceID = util.MustParseUUID(fixtureOther), util.MustParseUUID(fixtureOther)
			directory.rows = append(directory.rows, other)
			tc.edit(directory)
			body, _ := json.Marshal(commandFixture())
			request := httptest.NewRequest("POST", "/api/runtimes/"+fixtureRuntime+"/maintenance", bytes.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+userToken(t, fixtureOwner))
			response := httptest.NewRecorder()
			controlRoutes(NewControl(directory, &fixtureStore{})).ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("%d: %s", response.Code, response.Body)
			}
			if strings.Contains(response.Body.String(), "private backend") {
				t.Fatal("raw backend error disclosed")
			}
		})
	}
}

func TestMaintenanceCLIToAuthenticatedHandler(t *testing.T) {
	store := &fixtureStore{}
	server := httptest.NewServer(controlRoutes(NewControl(directoryFixture(), store)))
	defer server.Close()
	client := cli.NewAPIClient(server.URL, fixtureWorkspace, userToken(t, fixtureOwner))
	command := commandFixture()
	got, err := client.Maintenance(context.Background(), fixtureRuntime, "", &command)
	if err != nil || got == nil || got.State != "pending" || store.created != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	if got.Delivery.OwnerID != fixtureOwner || len(got.Delivery.Scope) != 64 {
		t.Fatal("missing authenticated binding")
	}
	got, err = client.Maintenance(context.Background(), fixtureRuntime, command.ID, nil)
	if err != nil || got.Delivery.Command != command {
		t.Fatalf("get: %+v %v", got, err)
	}
	store.err = errors.New("storage offline")
	if _, err := client.Maintenance(context.Background(), fixtureRuntime, "", &command); err == nil {
		t.Fatal("persistence failure acknowledged")
	}
}

func TestMaintenanceHandlerStrictInputAndStoreFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   func(string) string
		store  Store
		status int
	}{
		{"duplicate", func(s string) string { return strings.Replace(s, `"host":`, `"host":"other","host":`, 1) }, &fixtureStore{}, 400},
		{"unknown", func(s string) string { return strings.Replace(s, `"host":`, `"unknown":true,"host":`, 1) }, &fixtureStore{}, 400},
		{"missing", func(string) string { return `{"operation":"status"}` }, &fixtureStore{}, 400},
		{"oversize", func(string) string { return strings.Repeat(" ", protocol.MaintenanceBodyLimit+1) }, &fixtureStore{}, 400},
		{"unconfigured", func(s string) string { return s }, nil, 503},
		{"failed-write", func(s string) string { return s }, &fixtureStore{err: errors.New("private storage error")}, 503},
		{"conflict", func(s string) string { return s }, &fixtureStore{err: ErrConflict}, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := json.Marshal(commandFixture())
			request := httptest.NewRequest("POST", "/api/runtimes/"+fixtureRuntime+"/maintenance", strings.NewReader(tc.body(string(data))))
			request.Header.Set("Authorization", "Bearer "+userToken(t, fixtureOwner))
			response := httptest.NewRecorder()
			controlRoutes(NewControl(directoryFixture(), tc.store)).ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("%d: %s", response.Code, response.Body)
			}
			if strings.Contains(response.Body.String(), "private storage") {
				t.Fatal("storage detail leaked")
			}
		})
	}
}

func TestMaintenanceDaemonAndResultBinding(t *testing.T) {
	directory, store := directoryFixture(), &fixtureStore{}
	control := NewControl(directory, store)
	scope, err := control.ownerScope(context.Background(), "fixture-daemon", fixtureOwner)
	if err != nil {
		t.Fatal(err)
	}
	delivery := protocol.MaintenanceDelivery{Command: commandFixture(), OwnerID: fixtureOwner, Scope: scope}
	store.request = &protocol.MaintenanceRequest{Delivery: delivery, State: "pending"}
	routes := controlRoutes(control)
	for _, tc := range []struct {
		name, token string
		status      int
	}{
		{"wrong-owner", userToken(t, fixtureOther), http.StatusForbidden},
		{"no-auth", "", http.StatusUnauthorized},
		{"owner-daemon", userToken(t, fixtureOwner), http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/api/daemon/runtimes/"+fixtureRuntime+"/maintenance/claim", nil)
			if tc.token != "" {
				request.Header.Set("Authorization", "Bearer "+tc.token)
			}
			response := httptest.NewRecorder()
			routes.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("%d: %s", response.Code, response.Body)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		edit   func(*protocol.MaintenanceResult)
		status int
	}{
		{"valid", func(r *protocol.MaintenanceResult) {}, 200},
		{"drain", func(r *protocol.MaintenanceResult) { r.Status.CompleteDrain = true }, 400},
		{"wrong-host", func(r *protocol.MaintenanceResult) { r.Status.Host = "other" }, 400},
		{"wrong-owner", func(r *protocol.MaintenanceResult) { r.Delivery.OwnerID = fixtureOther }, 403},
		{"wrong-scope", func(r *protocol.MaintenanceResult) { r.Delivery.Scope = strings.Repeat("a", 64) }, 400},
		{"wrong-id", func(r *protocol.MaintenanceResult) { r.Delivery.Command.ID = fixtureOther }, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := protocol.MaintenanceResult{Delivery: delivery, Code: "ok", Status: protocol.MaintenanceStatus{
				Host: delivery.Command.Host, Profile: delivery.Command.Profile, ObservedAt: time.Now().UTC(),
			}}
			tc.edit(&result)
			body, _ := json.Marshal(result)
			request := httptest.NewRequest("POST", "/api/daemon/runtimes/"+fixtureRuntime+"/maintenance/"+delivery.Command.ID+"/result", bytes.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+userToken(t, fixtureOwner))
			response := httptest.NewRecorder()
			routes.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("%d: %s", response.Code, response.Body)
			}
		})
	}
}

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const probeOwner = "11111111-1111-4111-8111-111111111111"
const probeWorkspace = "22222222-2222-4222-8222-222222222222"
const probeToken = "mul_fake_probe_fixture"

func probeFixtureServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+probeToken ||
			r.Header.Get("X-Workspace-ID") != probeWorkspace || r.Header.Get("Accept") != "application/json" {
			t.Error("probe request did not preserve method/auth/workspace binding")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func writeProbeIdentity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	id := probeOwner
	if r.URL.Path == "/api/workspaces/"+probeWorkspace {
		id = probeWorkspace
	}
	_, _ = fmt.Fprintf(w, `{"id":%q,"name":"ignored server metadata"}`, id)
}

func TestOwnerProbeAuthenticatedIdentityAndNoProxy(t *testing.T) {
	var paths []string
	server := probeFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		writeProbeIdentity(w, r)
	})
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:1")
	// The general client's transport/timeout must not govern this bounded probe.
	client := NewAPIClient(server.URL, probeWorkspace, probeToken)
	client.HTTPClient = &http.Client{Timeout: time.Nanosecond}
	result, err := client.ProbeOwnerAPI(context.Background(), server.URL, probeOwner, probeWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "authenticated" || !result.APIAuthenticated || result.AuthorizesMutation ||
		result.Endpoint != server.URL || result.OwnerID != probeOwner || result.WorkspaceID != probeWorkspace ||
		result.ObservedAtUTC == "" || result.ObservationID == "" {
		t.Fatalf("unexpected observation: %+v", result)
	}
	if len(paths) != 2 || paths[0] != "/api/me" || paths[1] != "/api/workspaces/"+probeWorkspace {
		t.Fatalf("unexpected read sequence: %v", paths)
	}
	data, _ := json.Marshal(result)
	if len(data) > 16384 || strings.Contains(string(data), probeToken) || strings.Contains(string(data), "ignored server") {
		t.Fatal("probe emitted unbounded or sensitive detail")
	}
	localhost := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	localhostClient := NewAPIClient(localhost, probeWorkspace, probeToken)
	if result, err := localhostClient.ProbeOwnerAPI(context.Background(), localhost, probeOwner, probeWorkspace); err != nil || !result.APIAuthenticated {
		t.Fatalf("localhost must use the approved IPv4 listener: %v", err)
	}
}

func TestOwnerProbeRejectsBindingsBeforeNetwork(t *testing.T) {
	var calls atomic.Int64
	server := probeFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeProbeIdentity(w, r)
	})
	for _, endpoint := range []string{
		"https://127.0.0.1:8080", "http://example.invalid", "http://localhost:8080/api",
		"http://localhost:8080/?token=secret", "http://localhost:8080/#fragment",
		"http://user:secret@localhost:8080", "http://localhost:0", "http://localhost:00", "http://localhost:65536",
		"http://localhost:", "http://localhost:port", "http://[::1]:8080", "http://localhost:8080?",
	} {
		t.Run(endpoint, func(t *testing.T) {
			client := NewAPIClient(endpoint, probeWorkspace, probeToken)
			result, err := client.ProbeOwnerAPI(context.Background(), endpoint, probeOwner, probeWorkspace)
			if err == nil {
				t.Fatal("invalid endpoint accepted")
			}
			data, _ := json.Marshal(result)
			if strings.Contains(string(data), "secret") {
				t.Fatal("invalid credential-bearing endpoint leaked into observation")
			}
		})
	}
	for _, field := range []string{"base", "workspace", "owner", "empty_owner", "empty_token", "task_token", "agent", "task"} {
		t.Run(field, func(t *testing.T) {
			client := NewAPIClient(server.URL, probeWorkspace, probeToken)
			owner := probeOwner
			switch field {
			case "base":
				client.BaseURL += "/other"
			case "workspace":
				client.WorkspaceID = probeOwner
			case "owner":
				owner = "not-a-uuid"
			case "empty_owner":
				owner = "00000000-0000-0000-0000-000000000000"
			case "empty_token":
				client.Token = ""
			case "task_token":
				client.Token = "mat_fixture"
			case "agent":
				client.AgentID = "fixture"
			case "task":
				client.TaskID = "fixture"
			}
			if _, err := client.ProbeOwnerAPI(context.Background(), server.URL, owner, probeWorkspace); err == nil {
				t.Fatal("invalid binding/identity accepted")
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input sent credentials over the network")
	}
}

func TestOwnerProbeRejectsProtocolFailuresAndClassifiesStatus(t *testing.T) {
	cases := []struct {
		name, body, contentType, state, reason string
		status                                 int
	}{
		{"html", "<html>login</html>", "text/html", "failed", "protocol_error", 200},
		{"json_html", "<html>login</html>", "application/json", "failed", "protocol_error", 200},
		{"missing_id", `{}`, "application/json", "failed", "protocol_error", 200},
		{"wrong_type", `{"id":123}`, "application/json", "failed", "protocol_error", 200},
		{"null_id", `{"id":null}`, "application/json", "failed", "protocol_error", 200},
		{"array", `[]`, "application/json", "failed", "protocol_error", 200},
		{"multiple_objects", `{"id":"` + probeOwner + `"} {}`, "application/json", "failed", "protocol_error", 200},
		{"duplicate_id", `{"id":"` + probeOwner + `","id":"` + probeOwner + `"}`, "application/json", "failed", "protocol_error", 200},
		{"wrong_id", `{"id":"33333333-3333-4333-8333-333333333333"}`, "application/json", "failed", "identity_mismatch", 200},
		{"oversized", `{"id":"` + probeOwner + `","extra":"` + strings.Repeat("x", ownerProbeResponseLimit) + `"}`, "application/json", "failed", "protocol_error", 200},
		{"unauthenticated", "private secret response", "application/json", "reauth_required", "authentication_expired", 401},
		{"forbidden", "private secret response", "application/json", "failed", "access_denied", 403},
		{"not_found", "private secret response", "application/json", "failed", "protocol_error", 404},
		{"unavailable", "private secret response", "application/json", "waiting_dependency", "dependency_unavailable", 503},
		{"no_content", "", "application/json", "failed", "protocol_error", 204},
	}
	for _, at := range []string{"/api/me", "/api/workspaces/" + probeWorkspace} {
		for _, test := range cases {
			t.Run(at+"/"+test.name, func(t *testing.T) {
				server := probeFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != at {
						writeProbeIdentity(w, r)
						return
					}
					w.Header().Set("Content-Type", test.contentType)
					w.WriteHeader(test.status)
					_, _ = w.Write([]byte(test.body))
				})
				result, err := NewAPIClient(server.URL, probeWorkspace, probeToken).ProbeOwnerAPI(
					context.Background(), server.URL, probeOwner, probeWorkspace)
				if err == nil || result.State != test.state || result.ReasonCode != test.reason || result.APIAuthenticated {
					t.Fatalf("got %+v, %v", result, err)
				}
				if strings.Contains(err.Error(), "private secret") {
					t.Fatal("response body leaked into error")
				}
			})
		}
	}
}

func TestOwnerProbeNeverFollowsRedirect(t *testing.T) {
	var targetCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		writeProbeIdentity(w, r)
	}))
	defer target.Close()
	for _, code := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			server := probeFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+"/api/me", code)
			})
			result, err := NewAPIClient(server.URL, probeWorkspace, probeToken).ProbeOwnerAPI(context.Background(), server.URL, probeOwner, probeWorkspace)
			if err == nil || result.ReasonCode != "protocol_error" {
				t.Fatalf("redirect accepted: %+v %v", result, err)
			}
		})
	}
	if targetCalls.Load() != 0 {
		t.Fatal("redirect target received a probe request")
	}
}

func TestOwnerProbeOneHardBudgetAcrossReads(t *testing.T) {
	server := probeFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(6 * time.Second):
			writeProbeIdentity(w, r)
		case <-r.Context().Done():
		}
	})
	start := time.Now()
	result, err := NewAPIClient(server.URL, probeWorkspace, probeToken).ProbeOwnerAPI(context.Background(), server.URL, probeOwner, probeWorkspace)
	if !errors.Is(err, context.DeadlineExceeded) || result.ReasonCode != "timeout" || result.APIAuthenticated {
		t.Fatalf("ten-second shared budget not enforced: %+v %v", result, err)
	}
	if elapsed := time.Since(start); elapsed < 9*time.Second || elapsed > 11*time.Second {
		t.Fatalf("total elapsed %s, expected a single ten-second deadline", elapsed)
	}
}

func TestOwnerProbeBodyReadHonorsCallerDeadline(t *testing.T) {
	server := probeFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":`))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result, err := NewAPIClient(server.URL, probeWorkspace, probeToken).ProbeOwnerAPI(ctx, server.URL, probeOwner, probeWorkspace)
	if !errors.Is(err, context.DeadlineExceeded) || result.ReasonCode != "timeout" {
		t.Fatalf("stalled body ignored cancellation: %+v %v", result, err)
	}
}

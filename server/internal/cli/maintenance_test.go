package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestMaintenanceClientRejectsMalformedAndRedirectedResponses(t *testing.T) {
	command := protocol.MaintenanceCommand{ID: "11111111-1111-4111-8111-111111111111", Operation: "status",
		DaemonID: "fixture-daemon", Host: "fixture-host", Profile: "fixture", IssuedAt: time.Now().UTC()}
	request := protocol.MaintenanceRequest{Delivery: protocol.MaintenanceDelivery{
		Command: command, OwnerID: "22222222-2222-4222-8222-222222222222", Scope: strings.Repeat("a", 64),
	}, State: "pending"}
	data, _ := json.Marshal(request)
	for name, body := range map[string]string{
		"wrong-id":  strings.Replace(string(data), command.ID, "33333333-3333-4333-8333-333333333333", 1),
		"duplicate": strings.Replace(string(data), `"state":"pending"`, `"state":"pending","state":"completed"`, 1),
		"missing":   `{"state":"pending"}`, "oversize": strings.Repeat(" ", protocol.MaintenanceBodyLimit+1),
		"unknown-state": strings.Replace(string(data), `"state":"pending"`, `"state":"success"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			client := NewAPIClient(server.URL, "", "fixture-token")
			if _, err := client.Maintenance(context.Background(), "44444444-4444-4444-8444-444444444444", "", &command); err == nil {
				t.Fatal("malformed response accepted")
			}
		})
	}
	var redirected bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected = true
	}))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "", "fixture-token")
	if _, err := client.Maintenance(context.Background(), "44444444-4444-4444-8444-444444444444", "", &command); err == nil || redirected {
		t.Fatalf("redirect followed or accepted: %v", err)
	}
}

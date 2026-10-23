package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	lifecyclev1 "github.com/multica-ai/multica/server/pkg/lifecycle/v1"
)

func TestMaintenanceCapabilitiesClient(t *testing.T) {
	const runtimeID = "44444444-4444-4444-8444-444444444444"
	capabilities := lifecyclev1.MaintenanceCapabilities{
		Contract: lifecyclev1.Contract, DaemonID: "fixture", Scope: strings.Repeat("a", 64),
		BodyLimit: lifecyclev1.MaintenanceBodyLimit, BackendAdmission: lifecyclev1.CoverageSupported,
		DaemonAdmission: lifecyclev1.CoverageUnknown, SupervisorLaunch: lifecyclev1.CoverageUnsupported,
	}
	data, _ := json.Marshal(capabilities)
	for name, body := range map[string]string{
		"valid":      string(data),
		"major":      strings.Replace(string(data), "multica-lifecycle/v1", "multica-lifecycle/v2", 1),
		"missing":    `{"contract":"multica-lifecycle/v1"}`,
		"duplicate":  strings.Replace(string(data), `"complete_drain":false`, `"complete_drain":false,"complete_drain":true`, 1),
		"drain":      strings.Replace(string(data), `"complete_drain":false`, `"complete_drain":true`, 1),
		"coverage":   strings.Replace(string(data), `"unknown"`, `"open"`, 1),
		"oversize":   strings.Repeat(" ", lifecyclev1.MaintenanceBodyLimit+1),
		"wrong-case": strings.Replace(string(data), `"contract":`, `"Contract":`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/api/runtimes/"+runtimeID+"/maintenance/capabilities" {
					t.Errorf("unexpected discovery request: %s %s", r.Method, r.URL.Path)
				}
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			client := NewAPIClient(server.URL, "", "fixture-token")
			got, err := client.MaintenanceCapabilities(context.Background(), runtimeID)
			if name == "valid" {
				if err != nil || got == nil || *got != capabilities {
					t.Fatalf("discovery result: %+v %v", got, err)
				}
			} else if err == nil {
				t.Fatal("malformed or unsupported discovery accepted")
			}
			if _, err := client.MaintenanceCapabilities(context.Background(), "invalid"); err == nil || calls != 1 {
				t.Fatal("invalid runtime contacted backend")
			}
		})
	}
}

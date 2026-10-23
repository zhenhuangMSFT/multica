package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
	lifecyclev1 "github.com/multica-ai/multica/server/pkg/lifecycle/v1"
)

func TestRuntimeMaintenanceCapabilities(t *testing.T) {
	isolateAuthProbeTest(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/maintenance/capabilities") {
			t.Errorf("discovery mutated backend: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(lifecyclev1.MaintenanceCapabilities{
			Contract: lifecyclev1.Contract, DaemonID: "fixture", Scope: strings.Repeat("a", 64),
			BodyLimit: lifecyclev1.MaintenanceBodyLimit, BackendAdmission: lifecyclev1.CoverageSupported,
			DaemonAdmission: lifecyclev1.CoverageUnknown, SupervisorLaunch: lifecyclev1.CoverageUnsupported,
		})
	}))
	defer server.Close()
	if err := cli.SaveCLIConfigForProfile(cli.CLIConfig{Token: "fixture-token"}, "maintenance-fixture"); err != nil {
		t.Fatal(err)
	}
	cmd := newRuntimeMaintenanceCommand()
	cmd.SetContext(context.Background())
	for key, value := range map[string]string{"server-url": server.URL, "workspace-id": "55555555-5555-4555-8555-555555555555", "profile": "maintenance-fixture"} {
		cmd.Flags().String(key, value, "")
		_ = cmd.Flags().Set(key, value)
	}
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := runRuntimeMaintenance(cmd, []string{"capabilities", "44444444-4444-4444-8444-444444444444"}); err != nil {
		t.Fatal(err)
	}
	var got lifecyclev1.MaintenanceCapabilities
	if err := lifecyclev1.DecodeMaintenanceJSON(&output, &got); err != nil || got.Validate() != nil {
		t.Fatalf("invalid typed CLI discovery: %+v %v", got, err)
	}
	_ = cmd.Flags().Set("holder", "not-a-grant")
	if err := runRuntimeMaintenance(cmd, []string{"capabilities", "44444444-4444-4444-8444-444444444444"}); err == nil || calls != 1 {
		t.Fatal("discovery accepted mutation flags")
	}
}

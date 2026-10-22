package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	lifecyclev1 "github.com/multica-ai/multica/server/pkg/lifecycle/v1"
)

func TestRuntimeMaintenanceRejectsTaskContextBeforeConfiguration(t *testing.T) {
	t.Setenv("MULTICA_TASK_ID", "fixture-task")
	cmd := newRuntimeMaintenanceCommand()
	if err := runRuntimeMaintenance(cmd, []string{"status", "fixture-runtime"}); err == nil || !strings.Contains(err.Error(), "daemon-managed task") {
		t.Fatalf("task identity escaped local guard: %v", err)
	}
}

func TestRuntimeMaintenanceExplicitInputsAndTypedOutput(t *testing.T) {
	isolateAuthProbeTest(t)
	const runtimeID = "44444444-4444-4444-8444-444444444444"
	const commandID = "11111111-1111-4111-8111-111111111111"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("fixture credential missing")
		}

		var command lifecyclev1.MaintenanceCommand
		if err := lifecyclev1.DecodeMaintenanceJSON(r.Body, &command); err != nil {
			t.Error(err)
		}
		result := lifecyclev1.MaintenanceRequest{Delivery: lifecyclev1.MaintenanceDelivery{
			Command: command, OwnerID: "22222222-2222-4222-8222-222222222222", Scope: strings.Repeat("a", 64),
		}, State: "pending"}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(result)
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
	for key, value := range map[string]string{
		"command-id": commandID, "issued-at": time.Now().UTC().Format(time.RFC3339),
		"expect-daemon": "fixture-daemon", "expect-host": "fixture-host", "target-profile": "fixture",
	} {
		_ = cmd.Flags().Set(key, value)
	}
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := runRuntimeMaintenance(cmd, []string{"status", runtimeID}); err != nil {
		t.Fatal(err)
	}
	var result lifecyclev1.MaintenanceRequest
	if err := lifecyclev1.DecodeMaintenanceJSON(&output, &result); err != nil || result.Delivery.Command.ID != commandID {
		t.Fatalf("%+v %v", result, err)
	}
	_ = cmd.Flags().Set("holder", "executor")
	_ = cmd.Flags().Set("expires-at", time.Now().UTC().Add(time.Hour).Format(time.RFC3339))
	if err := runRuntimeMaintenance(cmd, []string{"acquire", runtimeID}); err == nil || !strings.Contains(err.Error(), "--generation") {
		t.Fatalf("implicit generation: %v", err)
	}
	if calls != 1 {
		t.Fatalf("malformed input contacted backend %d times", calls)
	}
	if err := runRuntimeMaintenance(cmd, []string{"get", runtimeID, commandID}); err == nil {
		t.Fatal("get accepted mutation flags")
	}
}

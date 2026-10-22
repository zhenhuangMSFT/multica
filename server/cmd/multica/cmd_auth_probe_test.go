package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

func newAuthProbeTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "probe"}
	for _, name := range []string{"profile", "server-url", "workspace-id", "expected-endpoint", "expected-owner-id", "expected-workspace-id", "expected-host"} {
		cmd.Flags().String(name, "", "")
	}
	return cmd
}

func isolateAuthProbeTest(t *testing.T) {
	t.Helper()
	redirectTestHome(t, t.TempDir())
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	for _, key := range []string{"MULTICA_TASK_ID", "MULTICA_AGENT_ID", "MULTICA_TASK_CONFIG_ROOT", "MULTICA_DAEMON_PORT",
		"MULTICA_TOKEN", "MULTICA_SERVER_URL", "MULTICA_WORKSPACE_ID", "MULTICA_PROFILE"} {
		t.Setenv(key, "")
	}
}

func TestAuthProbeRejectsManagedContextBeforeProfileAccess(t *testing.T) {
	t.Setenv("MULTICA_TASK_ID", "fixture-task")
	cmd := newAuthProbeTestCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	err := runAuthProbe(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not available inside a daemon-managed task") || output.Len() != 0 {
		t.Fatalf("probe escaped task boundary: output=%q err=%v", output.String(), err)
	}
}

func TestAuthProbeRequiresExplicitBindingsAndCleanOwnerContext(t *testing.T) {
	isolateAuthProbeTest(t)
	cmd := newAuthProbeTestCmd()
	if err := runAuthProbe(cmd, nil); err == nil || !strings.Contains(err.Error(), "explicit --profile") {
		t.Fatalf("missing profile not rejected: %v", err)
	}
	_ = cmd.Flags().Set("profile", "fixture")
	if err := runAuthProbe(cmd, nil); err == nil || !strings.Contains(err.Error(), "expected-endpoint") {
		t.Fatalf("missing expected bindings not rejected: %v", err)
	}
	for _, key := range []string{"MULTICA_TOKEN", "MULTICA_SERVER_URL", "MULTICA_WORKSPACE_ID", "MULTICA_DAEMON_PORT", "MULTICA_PROFILE"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "fixture")
			if err := runAuthProbe(cmd, nil); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("ambient override not rejected: %v", err)
			}
		})
	}
	_ = cmd.Flags().Set("server-url", "http://localhost:8080")
	if err := runAuthProbe(cmd, nil); err == nil || !strings.Contains(err.Error(), "not server/workspace overrides") {
		t.Fatalf("endpoint override not rejected: %v", err)
	}
	if _, err := os.Stat("ownership-state.json"); !os.IsNotExist(err) {
		t.Fatal("read-only probe created lifecycle state")
	}
}

func TestAuthProbeSavedFixtureProfileAndJSONOutput(t *testing.T) {
	isolateAuthProbeTest(t)
	const owner = "11111111-1111-4111-8111-111111111111"
	const workspace = "22222222-2222-4222-8222-222222222222"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+"mul_fixture_only" {
			t.Error("saved fixture credential not used")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		id := owner
		if r.URL.Path == "/api/workspaces/"+workspace {
			id = workspace
		}
		_, _ = fmt.Fprintf(w, `{"id":%q}`, id)
	}))
	defer server.Close()
	if err := cli.SaveCLIConfigForProfile(cli.CLIConfig{
		ServerURL: server.URL, WorkspaceID: workspace, Token: "mul_fixture_only",
	}, "probe-fixture"); err != nil {
		t.Fatal(err)
	}
	cmd := newAuthProbeTestCmd()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"profile": "probe-fixture", "expected-endpoint": server.URL,
		"expected-owner-id": owner, "expected-workspace-id": workspace, "expected-host": host} {
		_ = cmd.Flags().Set(key, value)
	}
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := runAuthProbe(cmd, nil); err != nil {
		t.Fatal(err)
	}
	var result cli.OwnerProbeResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.State != "authenticated" || !result.APIAuthenticated || result.AuthorizesMutation ||
		strings.Contains(output.String(), "mul_fixture_only") {
		t.Fatalf("unexpected or sensitive output: %s", output.String())
	}
	var observation map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &observation); err != nil {
		t.Fatal(err)
	}
	if len(observation["host"]) == 0 || len(observation["profile"]) == 0 ||
		string(observation["forwardedEndpointVerified"]) != "false" {
		t.Fatal("partial API health lacks target binding or overclaims forwarding")
	}
	_ = cmd.Flags().Set("expected-host", "wrong-computer.invalid")
	output.Reset()
	if err := runAuthProbe(cmd, nil); err == nil || !strings.Contains(err.Error(), "target host") || output.Len() != 0 {
		t.Fatalf("wrong-host invocation was not refused before collection: %v", err)
	}
}

func TestAuthProbeDoesNotCreateProfileOnMissingConfiguration(t *testing.T) {
	isolateAuthProbeTest(t)
	path, err := cli.CLIConfigPathForProfile("missing-probe-profile")
	if err != nil {
		t.Fatal(err)
	}
	cmd := newAuthProbeTestCmd()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"profile": "missing-probe-profile", "expected-endpoint": "http://localhost:8080",
		"expected-owner-id":     "11111111-1111-4111-8111-111111111111",
		"expected-workspace-id": "22222222-2222-4222-8222-222222222222", "expected-host": host} {
		_ = cmd.Flags().Set(key, value)
	}
	cmd.SetOut(&bytes.Buffer{})
	if err := runAuthProbe(cmd, nil); err == nil {
		t.Fatal("missing saved profile was treated as authenticated")
	}
	if _, err := os.Stat(filepath.Clean(path)); !os.IsNotExist(err) {
		t.Fatal("fixture profile unexpectedly exists")
	}
}

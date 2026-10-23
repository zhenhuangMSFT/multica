package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var authProbeCmd = &cobra.Command{
	Use:   "probe",
	Short: "Probe owner-profile API identity with a bounded loopback-only read",
	Long: "Read the selected owner's API identity and expected workspace without redirects or proxies, within ten seconds.\n" +
		"Requires explicit profile and expected bindings. Does not verify the tunnel, providers, or authorize admission.\n" +
		"Available only outside managed agent tasks. Outputs bounded JSON; errors also return a nonzero process exit.",
	Args: cobra.NoArgs,
	RunE: runAuthProbe,
}

func init() {
	authProbeCmd.Flags().String("expected-endpoint", "", "Exact approved loopback API endpoint")
	authProbeCmd.Flags().String("expected-owner-id", "", "Expected authenticated owner UUID")
	authProbeCmd.Flags().String("expected-workspace-id", "", "Expected accessible workspace UUID")
	authProbeCmd.Flags().String("expected-host", "", "Expected Windows worker computer name")
	authCmd.AddCommand(authProbeCmd)
}

func runAuthProbe(cmd *cobra.Command, _ []string) error {
	if err := requireHumanLocalCommand("auth probe"); err != nil {
		return err
	}
	// Reject weak task signals and ambient overrides too: a collector must
	// qualify its saved owner profile, not a task or another endpoint's token.
	for _, key := range []string{"MULTICA_DAEMON_PORT", "MULTICA_TOKEN", "MULTICA_SERVER_URL", "MULTICA_WORKSPACE_ID", "MULTICA_PROFILE"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return fmt.Errorf("auth probe requires a clean owner terminal without %s", key)
		}
	}
	profile := resolveProfile(cmd)
	if profile == "" {
		return fmt.Errorf("auth probe requires an explicit --profile")
	}
	if cmd.Flags().Changed("server-url") || cmd.Flags().Changed("workspace-id") {
		return fmt.Errorf("auth probe uses the saved profile; use expected bindings, not server/workspace overrides")
	}
	endpoint, _ := cmd.Flags().GetString("expected-endpoint")
	owner, _ := cmd.Flags().GetString("expected-owner-id")
	workspace, _ := cmd.Flags().GetString("expected-workspace-id")
	expectedHost, _ := cmd.Flags().GetString("expected-host")
	if endpoint == "" || owner == "" || workspace == "" || expectedHost == "" {
		return fmt.Errorf("auth probe requires --expected-endpoint, --expected-owner-id, --expected-workspace-id and --expected-host")
	}
	host, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("resolve probe host: %w", err)
	}
	if !strings.EqualFold(host, expectedHost) {
		return fmt.Errorf("auth probe target host does not match this computer")
	}
	cfg, err := cli.LoadCLIConfigForProfile(profile)
	if err != nil {
		return fmt.Errorf("load probe profile: %w", err)
	}
	client := cli.NewAPIClient(cfg.ServerURL, cfg.WorkspaceID, cfg.Token)
	result, probeErr := client.ProbeOwnerAPI(context.Background(), endpoint, owner, workspace)
	observation := struct {
		cli.OwnerProbeResult
		Host                      string `json:"host"`
		Profile                   string `json:"profile"`
		Component                 string `json:"component"`
		ExpiresAtUTC              string `json:"expiresAtUtc"`
		ForwardedEndpointVerified bool   `json:"forwardedEndpointVerified"`
	}{
		OwnerProbeResult: result, Host: host, Profile: profile, Component: "api",
		ExpiresAtUTC: time.Now().UTC().Add(45 * time.Second).Format(time.RFC3339Nano),
	}
	if err := cli.PrintJSON(cmd.OutOrStdout(), observation); err != nil {
		return err
	}
	return probeErr
}

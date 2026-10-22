package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/spf13/cobra"
)

func newRuntimeMaintenanceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "maintenance <status|acquire|recover|release|get> <runtime-id> [request-id]",
		Short: "Control daemon-wide local admission through the authenticated backend",
		Args:  cobra.RangeArgs(2, 3),
		RunE:  runRuntimeMaintenance,
	}
	cmd.Flags().String("command-id", "", "Explicit UUID for deduplication; reuse only with the identical request")
	cmd.Flags().String("issued-at", "", "Explicit UTC RFC3339 command time (two-minute delivery deadline)")
	cmd.Flags().String("expect-daemon", "", "Exact daemon ID")
	cmd.Flags().String("expect-host", "", "Exact OS host name, not device display name")
	cmd.Flags().String("target-profile", "", "Exact target daemon profile (empty means default)")
	cmd.Flags().String("holder", "", "Maintenance holder identity")
	cmd.Flags().Uint64("generation", 0, "Expected durable generation (required for mutations)")
	cmd.Flags().String("expires-at", "", "UTC RFC3339 holder expiry for acquire/recover; expiry never reopens admission")
	return cmd
}

func init() { runtimeCmd.AddCommand(newRuntimeMaintenanceCommand()) }

func runRuntimeMaintenance(cmd *cobra.Command, args []string) error {
	if err := requireHumanLocalCommand("runtime maintenance"); err != nil {
		return err
	}
	var command *protocol.MaintenanceCommand
	requestID := ""
	if args[0] == "get" {
		if len(args) != 3 {
			return errors.New("get requires a request ID")
		}
		requestID = args[2]
		for _, name := range []string{"command-id", "issued-at", "expect-daemon", "expect-host", "target-profile", "holder", "generation", "expires-at"} {
			if cmd.Flags().Changed(name) {
				return fmt.Errorf("get does not accept --%s", name)
			}
		}
	} else {
		if len(args) != 2 {
			return errors.New("submit does not accept a positional request ID")
		}
		value := func(name string) string { s, _ := cmd.Flags().GetString(name); return s }
		command = &protocol.MaintenanceCommand{
			ID: value("command-id"), Operation: args[0], DaemonID: value("expect-daemon"),
			Host: value("expect-host"), Profile: value("target-profile"), Holder: value("holder"),
		}
		var err error
		command.IssuedAt, err = time.Parse(time.RFC3339, value("issued-at"))
		if err != nil {
			return errors.New("--issued-at must be UTC RFC3339")
		}
		if command.IssuedAt.Location() != time.UTC {
			return errors.New("--issued-at must use UTC Z")
		}
		if value("expires-at") != "" {
			command.ExpiresAt, err = time.Parse(time.RFC3339, value("expires-at"))
			if err != nil || command.ExpiresAt.Location() != time.UTC {
				return errors.New("--expires-at must be UTC RFC3339")
			}
		}
		command.Generation, _ = cmd.Flags().GetUint64("generation")
		if command.Operation != "status" && !cmd.Flags().Changed("generation") {
			return errors.New("--generation is required for maintenance mutations")
		}
		if err := command.Validate(time.Now()); err != nil {
			return err
		}
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	result, err := client.Maintenance(cmd.Context(), args[1], requestID, command)
	if err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
}

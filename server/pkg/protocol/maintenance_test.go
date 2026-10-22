package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func maintenanceFixture() MaintenanceCommand {
	return MaintenanceCommand{ID: "11111111-1111-4111-8111-111111111111", Operation: "acquire",
		DaemonID: "fixture-daemon", Host: "fixture-host", Profile: "fixture", Holder: "operator",
		IssuedAt: time.Now().UTC().Truncate(time.Second), ExpiresAt: time.Now().UTC().Add(time.Hour).Truncate(time.Second)}
}

func TestMaintenanceCommandValidation(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name string
		edit func(*MaintenanceCommand)
	}{
		{"nil-id", func(c *MaintenanceCommand) { c.ID = "00000000-0000-0000-0000-000000000000" }},
		{"noncanonical-id", func(c *MaintenanceCommand) { c.ID = "11111111111141118111111111111111" }},
		{"unknown-operation", func(c *MaintenanceCommand) { c.Operation = "renew" }},
		{"missing-host", func(c *MaintenanceCommand) { c.Host = "" }},
		{"control-holder", func(c *MaintenanceCommand) { c.Holder = "a\nb" }},
		{"oversize-profile", func(c *MaintenanceCommand) { c.Profile = strings.Repeat("x", 65) }},
		{"stale", func(c *MaintenanceCommand) { c.IssuedAt = now.Add(-2 * time.Minute) }},
		{"future", func(c *MaintenanceCommand) { c.IssuedAt = now.Add(6 * time.Second) }},
		{"non-utc", func(c *MaintenanceCommand) { c.IssuedAt = c.IssuedAt.In(time.FixedZone("other", 3600)) }},
		{"expired-holder", func(c *MaintenanceCommand) { c.ExpiresAt = now }},
		{"unbounded-holder", func(c *MaintenanceCommand) { c.ExpiresAt = c.IssuedAt.Add(24*time.Hour + time.Second) }},
		{"generation-overflow", func(c *MaintenanceCommand) { c.Generation = ^uint64(0) }},
		{"status-transition-fields", func(c *MaintenanceCommand) { c.Operation = "status" }},
		{"release-zero-generation", func(c *MaintenanceCommand) { c.Operation, c.ExpiresAt = "release", time.Time{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := maintenanceFixture()
			tc.edit(&command)
			if err := command.Validate(now); err == nil {
				t.Fatal("invalid command accepted")
			}
		})
	}
	for _, operation := range []string{"status", "acquire", "recover", "release"} {
		command := maintenanceFixture()
		command.Operation = operation
		if operation == "status" {
			command.Holder, command.ExpiresAt = "", time.Time{}
		}
		if operation == "release" {
			command.Generation, command.ExpiresAt = 1, time.Time{}
		}
		if err := command.Validate(now); err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
	}
}

func TestMaintenanceStrictJSON(t *testing.T) {
	command := maintenanceFixture()
	data, _ := json.Marshal(command)
	valid := string(data)
	for name, body := range map[string]string{
		"duplicate":  strings.Replace(valid, `"operation":`, `"host":"other","operation":`, 1),
		"wrong-case": strings.Replace(valid, `"host":`, `"Host":`, 1),
		"unknown":    strings.Replace(valid, `"host":`, `"surprise":0,"host":`, 1),
		"null":       strings.Replace(valid, `"profile":"fixture"`, `"profile":null`, 1),
		"missing":    strings.Replace(valid, `"profile":"fixture",`, ``, 1),
		"array":      "[]", "trailing": valid + "{}", "oversize": strings.Repeat(" ", MaintenanceBodyLimit+1),
		"fractional-generation": strings.Replace(valid, `"generation":0`, `"generation":0.1`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			var got MaintenanceCommand
			if err := DecodeMaintenanceJSON(strings.NewReader(body), &got); err == nil {
				t.Fatal("malformed input accepted")
			}
		})
	}
	var got MaintenanceCommand
	if err := DecodeMaintenanceJSON(strings.NewReader(valid), &got); err != nil || got != command {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	exactLimit := valid + strings.Repeat(" ", MaintenanceBodyLimit-len(valid))
	if err := DecodeMaintenanceJSON(strings.NewReader(exactLimit), &got); err != nil {
		t.Fatalf("exact 16-KiB message refused: %v", err)
	}
}

func TestMaintenanceResultValidation(t *testing.T) {
	delivery := MaintenanceDelivery{Command: maintenanceFixture(), OwnerID: "22222222-2222-4222-8222-222222222222", Scope: strings.Repeat("a", 64)}
	result := MaintenanceResult{Delivery: delivery, Code: "ok", Status: MaintenanceStatus{
		Host: delivery.Command.Host, Profile: delivery.Command.Profile, ObservedAt: time.Now().UTC(),
		Blocked: true, LocalQuiescent: true,
	}}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*MaintenanceResult)
	}{
		{"complete-drain", func(r *MaintenanceResult) { r.Status.CompleteDrain = true }},
		{"negative-counts", func(r *MaintenanceResult) { r.Status.ActiveTasks = -1 }},
		{"false-quiescence", func(r *MaintenanceResult) { r.Status.ClaimsInFlight = 1 }},
		{"wrong-host", func(r *MaintenanceResult) { r.Status.Host = "other" }},
		{"invalid-owner", func(r *MaintenanceResult) { r.Delivery.OwnerID = "not-owner" }},
		{"invalid-scope", func(r *MaintenanceResult) { r.Delivery.Scope = strings.Repeat("z", 64) }},
		{"unknown-code", func(r *MaintenanceResult) { r.Code = "success-ish" }},
		{"missing-observed-time", func(r *MaintenanceResult) { r.Status.ObservedAt = time.Time{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := result
			tc.edit(&copy)
			if err := copy.Validate(); err == nil {
				t.Fatal("invalid result accepted")
			}
		})
	}
	request := MaintenanceRequest{Delivery: delivery, State: "completed", Result: &result}
	data, _ := json.Marshal(request)
	nestedDuplicate := strings.Replace(string(data), `"code":"ok"`, `"code":"ok","code":"rejected"`, 1)
	if err := DecodeMaintenanceJSON(strings.NewReader(nestedDuplicate), &request); err == nil {
		t.Fatal("nested duplicate accepted")
	}
}

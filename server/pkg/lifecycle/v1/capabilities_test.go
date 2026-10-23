package lifecyclev1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLifecycleV1Capabilities(t *testing.T) {
	valid := MaintenanceCapabilities{Contract: Contract, DaemonID: "fixture",
		Scope: strings.Repeat("a", 64), BodyLimit: MaintenanceBodyLimit,
		BackendAdmission: CoverageSupported, DaemonAdmission: CoverageUnknown,
		SupervisorLaunch: CoverageUnsupported}
	for _, tc := range []struct {
		name string
		edit func(*MaintenanceCapabilities)
	}{
		{"major", func(c *MaintenanceCapabilities) { c.Contract = "multica-lifecycle/v2" }},
		{"missing-contract", func(c *MaintenanceCapabilities) { c.Contract = "" }},
		{"missing-binding", func(c *MaintenanceCapabilities) { c.DaemonID = "" }},
		{"bad-scope", func(c *MaintenanceCapabilities) { c.Scope = "" }},
		{"limit", func(c *MaintenanceCapabilities) { c.BodyLimit++ }},
		{"unknown-enum", func(c *MaintenanceCapabilities) { c.DaemonAdmission = "ready" }},
		{"complete-drain", func(c *MaintenanceCapabilities) { c.CompleteDrain = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := valid
			tc.edit(&got)
			if got.Validate() == nil {
				t.Fatal("unsupported capability accepted")
			}
		})
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"valid":     string(data),
		"missing":   strings.Replace(string(data), `"contract":"multica-lifecycle/v1",`, "", 1),
		"duplicate": strings.Replace(string(data), `"contract":`, `"contract":"multica-lifecycle/v1","contract":`, 1),
		"unknown":   strings.Replace(string(data), `"scope":`, `"extra":true,"scope":`, 1),
		"null":      strings.Replace(string(data), `"daemon_admission":"unknown"`, `"daemon_admission":null`, 1),
	} {
		t.Run("decode-"+name, func(t *testing.T) {
			var got MaintenanceCapabilities
			err := DecodeMaintenanceJSON(strings.NewReader(body), &got)
			if name == "valid" {
				if err != nil || got != valid {
					t.Fatalf("capability roundtrip: %+v %v", got, err)
				}
			} else if err == nil {
				t.Fatal("malformed capabilities accepted")
			}
		})
	}
}

func TestLifecycleV1CommandWireCompatibility(t *testing.T) {
	// Moving the DTO package must not change stored intents, Redis payloads or
	// installed CLI requests. Version negotiation is additive discovery.
	const wire = `{"id":"11111111-1111-4111-8111-111111111111","operation":"status","daemon_id":"fixture","host":"host","profile":"","holder":"","generation":0,"expires_at":"0001-01-01T00:00:00Z","issued_at":"2026-10-22T08:00:00Z"}`
	var command MaintenanceCommand
	if err := DecodeMaintenanceJSON(strings.NewReader(wire), &command); err != nil {
		t.Fatal(err)
	}
	if err := command.Validate(time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(command)
	if err != nil || string(data) != wire {
		t.Fatalf("v1 command bytes changed: %s %v", data, err)
	}
}

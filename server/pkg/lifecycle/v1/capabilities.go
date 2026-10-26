// Package lifecyclev1 defines the public, platform-independent lifecycle control
// contract. It contains no scheduler, host executor or deployment policy.
package lifecyclev1

import "errors"

const Contract = "multica-lifecycle/v1"

const (
	CoverageSupported   = "supported"
	CoverageUnsupported = "unsupported"
	CoverageUnknown     = "unknown"
)

// MaintenanceCapabilities describes this backend, not a fleet-wide attestation
// or permission to launch. Daemon/OS coverage needs independent bound evidence.
type MaintenanceCapabilities struct {
	Contract         string `json:"contract"`
	DaemonID         string `json:"daemon_id"`
	Scope            string `json:"scope"`
	BodyLimit        int    `json:"body_limit"`
	BackendAdmission string `json:"backend_admission"`
	DaemonAdmission  string `json:"daemon_admission"`
	SupervisorLaunch string `json:"supervisor_launch"`
	CompleteDrain    bool   `json:"complete_drain"`
}

func (c MaintenanceCapabilities) Validate() error {
	if c.Contract != Contract || !maintenanceText(c.DaemonID, 128, false) ||
		!maintenanceScope(c.Scope) || c.BodyLimit != MaintenanceBodyLimit || c.CompleteDrain {
		return errors.New("unsupported lifecycle contract or invalid capability binding")
	}
	for _, coverage := range []string{c.BackendAdmission, c.DaemonAdmission, c.SupervisorLaunch} {
		switch coverage {
		case CoverageSupported, CoverageUnsupported, CoverageUnknown:
		default:
			return errors.New("invalid lifecycle capability coverage")
		}
	}
	return nil
}

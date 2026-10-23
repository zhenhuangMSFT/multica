package executor

import (
	"context"
	"errors"
	"io"
	"regexp"
	"time"

	"github.com/google/uuid"
	lifecyclev1 "github.com/multica-ai/multica/server/pkg/lifecycle/v1"
)

const Contract = "runtime-manager-executor/v1"

var (
	ErrDenied      = errors.New("executor request denied")
	ErrUnavailable = errors.New("production executor adapter unavailable")
	ErrUnknown     = errors.New("child outcome unknown; exact reconciliation required")
	digestPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Binding is enrollment configuration, not caller-supplied authorization.
type Binding struct {
	Installation string `json:"installation"`
	Host         string `json:"host"`
	OwnerID      string `json:"owner_id"`
	OwnerSID     string `json:"owner_sid"`
	Session      uint32 `json:"session"`
	Profile      string `json:"profile"`
	DaemonID     string `json:"daemon_id"`
	Scope        string `json:"scope"`
}

type Definition struct {
	Component string `json:"component"`
	Artifact  string `json:"artifact_sha256"`
	Config    string `json:"config_sha256"`
	Launch    string `json:"launch_sha256"`
}

type Request struct {
	Contract    string                         `json:"contract"`
	ID          string                         `json:"id"`
	Binding     Binding                        `json:"binding"`
	Incarnation string                         `json:"incarnation"`
	Operation   string                         `json:"operation"`
	Reason      string                         `json:"reason"`
	Generation  uint64                         `json:"generation"`
	Epoch       uint64                         `json:"epoch"`
	Expires     time.Time                      `json:"expires"`
	Definition  Definition                     `json:"definition"`
	Transaction string                         `json:"transaction"`
	Attempt     string                         `json:"attempt"`
	Core        *lifecyclev1.MaintenanceResult `json:"core"`
}

func DecodeRequest(reader io.Reader) (Request, error) {
	var request Request
	err := lifecyclev1.DecodeMaintenanceJSON(reader, &request)
	return request, err
}

func identifier(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func (b Binding) valid() bool {
	return identifier(b.Installation) && identifier(b.OwnerID) && b.Host != "" &&
		b.OwnerSID != "" && b.Session > 0 && b.DaemonID != "" && digestPattern.MatchString(b.Scope)
}

func (d Definition) valid() bool {
	return (d.Component == "daemon" || d.Component == "tunnel") &&
		digestPattern.MatchString(d.Artifact) && digestPattern.MatchString(d.Config) && digestPattern.MatchString(d.Launch)
}

func (r Request) validate(now time.Time) error {
	if err := r.validateHistorical(); err != nil {
		return err
	}
	if !r.Expires.After(now) || r.Expires.After(now.Add(time.Minute)) {
		return ErrDenied
	}
	if r.Core != nil {
		s := r.Core.Status
		if s.ObservedAt.After(now) || now.Sub(s.ObservedAt) > time.Minute ||
			(r.Operation != "open" && !s.ExpiresAt.After(now)) {
			return ErrDenied
		}
	}
	return nil
}

// Receipt integrity has no live authorization clock. Retained proofs must not
// become invalid merely because their observation or hold has since expired.
func (r Request) validateHistorical() error {
	if r.Contract != Contract || !identifier(r.ID) || !r.Binding.valid() || !identifier(r.Incarnation) ||
		r.Expires.IsZero() || r.Expires.Location() != time.UTC {
		return ErrDenied
	}
	switch r.Operation {
	case "launch":
		if !r.Definition.valid() || (r.Reason != "initial" && r.Reason != "scheduler_retry" && r.Reason != "component_respawn") ||
			r.Core != nil || r.Transaction != "" || r.Attempt != "" {
			return ErrDenied
		}
	case "replace":
		if !r.Definition.valid() || r.Reason != "replacement" || !identifier(r.Transaction) || r.Attempt != "" || r.Core == nil {
			return ErrDenied
		}
	case "close", "open":
		if r.Definition != (Definition{}) || r.Reason != "" || r.Transaction != "" || r.Attempt != "" || (r.Operation == "close" && r.Core == nil) {
			return ErrDenied
		}
	case "observe":
		if !r.Definition.valid() || !identifier(r.Attempt) || r.Reason != "" || r.Transaction != "" || r.Core != nil {
			return ErrDenied
		}
	default:
		return ErrDenied
	}
	if r.Core != nil {
		proof := r.Core
		c, s := proof.Delivery.Command, proof.Status
		if proof.Validate() != nil || proof.Code != "ok" || s.PersistenceFault || s.RecoveryRequired ||
			proof.Delivery.OwnerID != r.Binding.OwnerID || proof.Delivery.Scope != r.Binding.Scope ||
			c.Host != r.Binding.Host || c.Profile != r.Binding.Profile || c.DaemonID != r.Binding.DaemonID ||
			s.Generation != r.Generation || !s.ObservedAt.Before(r.Expires) {
			return ErrDenied
		}
		if r.Operation == "open" {
			if c.Operation != "release" || c.Generation != r.Generation || s.Blocked || s.Holder != "" || !s.ExpiresAt.IsZero() {
				return ErrDenied
			}
		} else if (c.Operation != "acquire" && c.Operation != "recover") || !s.Blocked ||
			s.Generation != c.Generation+1 || s.Holder != c.Holder || s.Holder == "" || !s.ExpiresAt.Equal(c.ExpiresAt) ||
			!s.ExpiresAt.After(s.ObservedAt) {
			return ErrDenied
		}
	}
	return nil
}

func (r Request) authorizedContext(ctx context.Context, now time.Time) (context.Context, context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := r.validate(now); err != nil {
		return nil, nil, err
	}
	deadline := r.Expires
	if r.Core != nil {
		if observationDeadline := r.Core.Status.ObservedAt.Add(time.Minute); observationDeadline.Before(deadline) {
			deadline = observationDeadline
		}
		if r.Operation != "open" && r.Core.Status.ExpiresAt.Before(deadline) {
			deadline = r.Core.Status.ExpiresAt
		}
	}
	if !deadline.After(now) {
		return nil, nil, ErrDenied
	}
	// Translate the injected clock's remaining budget to a real context timer.
	// Parent cancellation/deadlines can shorten it, never extend it.
	bounded, cancel := context.WithTimeout(ctx, deadline.Sub(now))
	return bounded, cancel, nil
}

// Authority must authenticate the manager channel AND its committed human/policy
// decision. A structurally valid core result or actor field alone is no grant.
type Authority interface {
	Verify(context.Context, Request) error
}

type Child struct {
	PID         uint32    `json:"pid"`
	Created     time.Time `json:"created"`
	Image       string    `json:"image_sha256"`
	OwnerSID    string    `json:"owner_sid"`
	Session     uint32    `json:"session"`
	Containment string    `json:"containment"`
}

func (c Child) valid(b Binding, d Definition) bool {
	return c.PID > 0 && !c.Created.IsZero() && c.Created.Location() == time.UTC &&
		c.Image == d.Artifact && c.OwnerSID == b.OwnerSID && c.Session == b.Session && identifier(c.Containment)
}

// CreateSuspended must resolve only the enrolled definition. No command strings
// or executable paths are accepted from a request. Unknown is not process exit.
// Adapters must honor cancellation and recheck the supplied context immediately
// before each OS side effect; a context cannot forcibly stop an ignoring adapter.
type Processes interface {
	CreateSuspended(context.Context, Definition, string) (Child, error)
	Resume(context.Context, Child) error
	Observe(context.Context, string, Child) (string, Child, error)
}

type UnavailableAuthority struct{}

func (UnavailableAuthority) Verify(context.Context, Request) error { return ErrUnavailable }

type UnavailableProcesses struct{}

func (UnavailableProcesses) CreateSuspended(context.Context, Definition, string) (Child, error) {
	return Child{}, ErrUnavailable
}
func (UnavailableProcesses) Resume(context.Context, Child) error { return ErrUnavailable }
func (UnavailableProcesses) Observe(context.Context, string, Child) (string, Child, error) {
	return "unknown", Child{}, ErrUnavailable
}

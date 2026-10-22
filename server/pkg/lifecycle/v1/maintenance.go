package lifecyclev1

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
)

const MaintenanceBodyLimit = 16 << 10

// MaintenanceCommand is an owner request, not a complete-drain lease.
type MaintenanceCommand struct {
	ID         string    `json:"id"`
	Operation  string    `json:"operation"`
	DaemonID   string    `json:"daemon_id"`
	Host       string    `json:"host"`
	Profile    string    `json:"profile"`
	Holder     string    `json:"holder"`
	Generation uint64    `json:"generation"`
	ExpiresAt  time.Time `json:"expires_at"`
	IssuedAt   time.Time `json:"issued_at"`
}

func (c MaintenanceCommand) Validate(now time.Time) error {
	if !maintenanceUUID(c.ID) || !maintenanceText(c.DaemonID, 128, false) ||
		!maintenanceText(c.Host, 255, false) || !maintenanceText(c.Profile, 64, true) ||
		c.IssuedAt.IsZero() || c.IssuedAt.Location() != time.UTC || c.ExpiresAt.Location() != time.UTC ||
		c.IssuedAt.After(now.Add(5*time.Second)) || !now.Before(c.IssuedAt.Add(2*time.Minute)) {
		return errors.New("invalid maintenance identity, generation or command deadline")
	}
	switch c.Operation {
	case "status":
		if c.Holder != "" || c.Generation != 0 || !c.ExpiresAt.IsZero() {
			return errors.New("status must not carry transition fields")
		}
	case "acquire", "recover":
		if c.Generation == math.MaxUint64 || !maintenanceText(c.Holder, 128, false) || !c.ExpiresAt.After(now) || c.ExpiresAt.After(c.IssuedAt.Add(24*time.Hour)) {
			return errors.New("invalid maintenance holder or expiry")
		}
	case "release":
		if !maintenanceText(c.Holder, 128, false) || c.Generation == 0 || !c.ExpiresAt.IsZero() {
			return errors.New("invalid maintenance release")
		}
	default:
		return errors.New("unsupported maintenance operation")
	}
	return nil
}

func maintenanceUUID(s string) bool {
	id, err := uuid.Parse(s)
	return err == nil && id != uuid.Nil && id.String() == s
}

func maintenanceText(s string, max int, empty bool) bool {
	if (!empty && s == "") || len(s) > max || s != strings.TrimSpace(s) {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

type MaintenanceDelivery struct {
	Command MaintenanceCommand `json:"command"`
	OwnerID string             `json:"owner_id"`
	Scope   string             `json:"scope"`
}

type MaintenanceStatus struct {
	ObservedAt       time.Time `json:"observed_at"`
	Host             string    `json:"host"`
	Profile          string    `json:"profile"`
	Holder           string    `json:"holder"`
	Generation       uint64    `json:"generation"`
	ExpiresAt        time.Time `json:"expires_at"`
	Blocked          bool      `json:"blocked"`
	ClaimsInFlight   int       `json:"claims_in_flight"`
	ActiveTasks      int64     `json:"active_tasks"`
	RunningTasks     int64     `json:"running_tasks"`
	ResourceWait     int64     `json:"resource_wait"`
	LocalQuiescent   bool      `json:"local_quiescent"`
	CompleteDrain    bool      `json:"complete_drain"`
	PersistenceFault bool      `json:"persistence_fault"`
	RecoveryRequired bool      `json:"recovery_required"`
}

type MaintenanceResult struct {
	Delivery MaintenanceDelivery `json:"delivery"`
	Code     string              `json:"code"`
	Status   MaintenanceStatus   `json:"status"`
}

type MaintenanceRequest struct {
	Delivery MaintenanceDelivery `json:"delivery"`
	State    string              `json:"state"`
	Result   *MaintenanceResult  `json:"result"`
}

type MaintenanceAcknowledgment struct {
	ID       string `json:"id"`
	Accepted bool   `json:"accepted"`
}

func (r MaintenanceResult) Validate() error {
	c := r.Delivery.Command
	s := r.Status
	if c.Validate(c.IssuedAt) != nil || !maintenanceUUID(r.Delivery.OwnerID) ||
		!maintenanceScope(r.Delivery.Scope) || s.Host != c.Host || s.Profile != c.Profile ||
		s.ObservedAt.IsZero() || s.ObservedAt.Location() != time.UTC || s.CompleteDrain || s.ClaimsInFlight < 0 || s.ActiveTasks < 0 || s.RunningTasks < 0 || s.ResourceWait < 0 ||
		(s.LocalQuiescent && (!s.Blocked || s.PersistenceFault || s.ClaimsInFlight != 0 || s.ActiveTasks != 0 || s.RunningTasks != 0 || s.ResourceWait != 0)) {
		return errors.New("invalid maintenance result binding or status")
	}
	switch r.Code {
	case "ok", "rejected", "target_mismatch", "persistence_fault", "outcome_unknown":
		return nil
	default:
		return errors.New("invalid maintenance result code")
	}
}

func (r MaintenanceRequest) Validate() error {
	if r.Delivery.Command.Validate(r.Delivery.Command.IssuedAt) != nil ||
		!maintenanceUUID(r.Delivery.OwnerID) || !maintenanceScope(r.Delivery.Scope) {
		return errors.New("invalid maintenance delivery")
	}
	switch r.State {
	case "pending", "delivered", "expired", "indeterminate":
		if r.Result != nil {
			return errors.New("unexpected maintenance result")
		}
	case "completed":
		if r.Result == nil || r.Result.Delivery != r.Delivery || r.Result.Validate() != nil {
			return errors.New("invalid completed maintenance result")
		}
	default:
		return errors.New("invalid maintenance request state")
	}
	return nil
}

func maintenanceScope(scope string) bool {
	b, err := hex.DecodeString(scope)
	return err == nil && len(b) == 32 && strings.ToLower(scope) == scope
}

// DecodeMaintenanceJSON rejects duplicates, wrong-case keys, nulls and trailing
// data as well as unknown fields. Control messages have only nested objects.
func DecodeMaintenanceJSON(r io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(r, MaintenanceBodyLimit+1))
	if err != nil || len(data) > MaintenanceBodyLimit {
		return errors.New("maintenance message exceeds limit or cannot be read")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := maintenanceObject(dec, reflect.TypeOf(target).Elem()); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing maintenance data")
	}
	return json.Unmarshal(data, target)
}

func maintenanceObject(dec *json.Decoder, typ reflect.Type) error {
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return errors.New("maintenance object required")
	}
	fields := make(map[string]reflect.Type)
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		fields[strings.Split(field.Tag.Get("json"), ",")[0]] = field.Type
	}
	seen := make(map[string]bool)
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return errors.New("invalid maintenance key")
		}
		name, ok := key.(string)
		field, exists := fields[name]
		if !ok || !exists || seen[name] {
			return errors.New("duplicate or unknown maintenance key")
		}
		seen[name] = true
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		if field.Kind() == reflect.Pointer {
			if bytes.Equal(raw, []byte("null")) {
				continue // The optional request result is the sole nullable field.
			}
			field = field.Elem()
		}
		if bytes.Equal(raw, []byte("null")) {
			return errors.New("null maintenance field")
		}
		if field.Kind() == reflect.Struct && field != reflect.TypeOf(time.Time{}) {
			if err := maintenanceObject(json.NewDecoder(bytes.NewReader(raw)), field); err != nil {
				return err
			}
		}
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	if len(seen) != len(fields) {
		return errors.New("missing maintenance field")
	}
	return nil
}

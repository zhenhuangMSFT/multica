package daemoncontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	lifecyclev1 "github.com/multica-ai/multica/server/pkg/lifecycle/v1"
)

// Admission is a complementary server reservation fence, not a qualified
// external maintenance authority or proof that the daemon has drained.
type Admission interface {
	Prepare(context.Context, lifecyclev1.MaintenanceDelivery) error
	Check(context.Context, lifecyclev1.MaintenanceDelivery) error
	Complete(context.Context, lifecyclev1.MaintenanceResult) error
}

type admissionTxStarter interface {
	Begin(context.Context) (pgx.Tx, error)
}

type PostgresAdmission struct{ pool admissionTxStarter }

func NewPostgresAdmission(pool admissionTxStarter) *PostgresAdmission {
	return &PostgresAdmission{pool: pool}
}

type admissionState struct {
	Version    int                             `json:"version"`
	Closed     bool                            `json:"closed"`
	Generation uint64                          `json:"generation"`
	Uncertain  bool                            `json:"uncertain"`
	Delivery   lifecyclev1.MaintenanceDelivery `json:"delivery"`
	Result     *lifecyclev1.MaintenanceResult  `json:"result"`
}

func (s *admissionState) sameTarget(d lifecyclev1.MaintenanceDelivery) bool {
	a, b := s.Delivery, d
	return a.OwnerID == b.OwnerID && a.Command.DaemonID == b.Command.DaemonID &&
		a.Command.Host == b.Command.Host && a.Command.Profile == b.Command.Profile
}

func (s *admissionState) prepare(d lifecyclev1.MaintenanceDelivery, now time.Time) error {
	c := d.Command
	if !s.sameTarget(d) {
		// An unconfirmed/mistargeted intent must not permanently poison this
		// daemon's target. A new exact-owner request may correct it only while
		// closed, after the result window, and never by releasing admission.
		if !s.Closed || !s.Uncertain || s.Delivery.OwnerID != d.OwnerID ||
			s.Delivery.Command.DaemonID != c.DaemonID || now.Before(s.Delivery.Command.IssuedAt.Add(5*time.Minute)) ||
			(c.Operation != "acquire" && c.Operation != "recover") {
			return ErrConflict
		}
	}
	if s.Delivery.Scope != d.Scope && c.Operation != "recover" && (c.Operation != "acquire" || s.Closed) {
		return ErrConflict
	}
	if s.Delivery == d {
		return nil
	}
	// An uncertain prior request must leave its bounded result/delivery window
	// before a different command can supersede it. Never roll back closure.
	if s.Uncertain && now.Before(s.Delivery.Command.IssuedAt.Add(5*time.Minute)) {
		return ErrConflict
	}
	switch c.Operation {
	case "acquire":
		if c.Generation != s.Generation || (s.Closed && !s.Uncertain) {
			return ErrConflict
		}
	case "recover":
		if !s.Closed || c.Generation < s.Generation ||
			(c.Generation != s.Generation && (!s.Uncertain || s.Generation == math.MaxUint64 || c.Generation != s.Generation+1)) {
			return ErrConflict
		}
	case "release":
		if !s.Closed || s.Uncertain || c.Generation != s.Generation || c.Holder != s.Delivery.Command.Holder {
			return ErrConflict
		}
	default:
		return ErrConflict
	}
	s.Closed, s.Uncertain, s.Generation, s.Delivery, s.Result = true, true, c.Generation, d, nil
	return nil
}

func (s *admissionState) complete(r lifecyclev1.MaintenanceResult) error {
	if s.Delivery != r.Delivery {
		return ErrConflict
	}
	if s.Result != nil {
		if *s.Result == r {
			return nil
		}
		return ErrConflict
	}
	c, status := r.Delivery.Command, r.Status
	// Only a bound positive acknowledgment changes the known generation.
	// Rejections, persistence faults and uncertain outcomes keep the fence.
	if r.Code == "ok" && !status.PersistenceFault && !status.RecoveryRequired {
		switch c.Operation {
		case "acquire", "recover":
			if !status.Blocked || status.Generation != c.Generation+1 || status.Holder != c.Holder || !status.ExpiresAt.Equal(c.ExpiresAt) {
				return ErrConflict
			}
			s.Generation, s.Uncertain = status.Generation, false
		case "release":
			if status.Blocked || status.Generation != c.Generation || status.Holder != "" || !status.ExpiresAt.IsZero() {
				return ErrConflict
			}
			s.Closed, s.Uncertain = false, false
		}
	}
	s.Result = &r
	return nil
}

func (p *PostgresAdmission) update(ctx context.Context, d lifecyclev1.MaintenanceDelivery, fn func(*admissionState) error, create, write bool) error {
	if err := (lifecyclev1.MaintenanceRequest{Delivery: d, State: "pending"}).Validate(); err != nil {
		return err
	}
	if p.pool == nil {
		return errors.New("maintenance admission repository missing")
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	daemon := d.Command.DaemonID
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('maintenance:' || $1, 0))`, daemon); err != nil {
		return err
	}
	scope, err := (&Control{directory: db.New(tx)}).ownerScope(ctx, daemon, d.OwnerID)
	if err != nil {
		return err
	}
	if scope != d.Scope {
		return ErrConflict
	}
	var data []byte
	err = tx.QueryRow(ctx, `SELECT state FROM daemon_maintenance_admission WHERE daemon_id=$1`, daemon).Scan(&data)
	state := admissionState{Version: 1, Generation: d.Command.Generation, Delivery: d}
	if errors.Is(err, pgx.ErrNoRows) {
		if d.Command.Operation != "status" && (!create || (d.Command.Operation != "acquire" && d.Command.Operation != "recover")) {
			return ErrConflict
		}
		if d.Command.Operation != "status" {
			state.Closed, state.Uncertain = true, true
		}
	} else if err != nil {
		return err
	} else {
		if err := lifecyclev1.DecodeMaintenanceJSON(bytes.NewReader(data), &state); err != nil {
			return fmt.Errorf("decode server maintenance fence: %w", err)
		}
		if state.Version != 1 || (lifecyclev1.MaintenanceRequest{Delivery: state.Delivery, State: "pending"}).Validate() != nil ||
			(state.Result != nil && (state.Result.Delivery != state.Delivery || state.Result.Validate() != nil)) ||
			(!state.Closed && (state.Uncertain || state.Result == nil || state.Result.Code != "ok" || state.Delivery.Command.Operation != "release")) {
			return errors.New("invalid server maintenance fence")
		}
	}
	if d.Command.Operation != "status" {
		if err := fn(&state); err != nil {
			return err
		}
	}
	if write && d.Command.Operation != "status" {
		data, err = json.Marshal(state)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO daemon_maintenance_admission(daemon_id,state) VALUES($1,$2)
			ON CONFLICT (daemon_id) DO UPDATE SET state=EXCLUDED.state`, daemon, data)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (p *PostgresAdmission) Prepare(ctx context.Context, d lifecyclev1.MaintenanceDelivery) error {
	return p.update(ctx, d, func(s *admissionState) error { return s.prepare(d, time.Now()) }, true, true)
}
func (p *PostgresAdmission) Check(ctx context.Context, d lifecyclev1.MaintenanceDelivery) error {
	return p.update(ctx, d, func(s *admissionState) error {
		if !s.Closed || s.Delivery != d || s.Result != nil {
			return ErrConflict
		}
		return nil
	}, false, false)
}
func (p *PostgresAdmission) Complete(ctx context.Context, r lifecyclev1.MaintenanceResult) error {
	if err := r.Validate(); err != nil {
		return err
	}
	return p.update(ctx, r.Delivery, func(s *admissionState) error { return s.complete(r) }, false, true)
}

type fencedStore struct {
	Store
	admission Admission
}

func NewFencedStore(store Store, admission Admission) Store {
	return &fencedStore{Store: store, admission: admission}
}

func (s *fencedStore) Create(ctx context.Context, d lifecyclev1.MaintenanceDelivery) (*lifecyclev1.MaintenanceRequest, error) {
	if s.Store == nil || s.admission == nil {
		return nil, errors.New("maintenance delivery/admission store missing")
	}
	if err := s.admission.Prepare(ctx, d); err != nil {
		return nil, err
	}
	// Failed or conflicting delivery storage never undoes the persisted fence.
	return s.Store.Create(ctx, d)
}
func (s *fencedStore) Deliver(ctx context.Context, d lifecyclev1.MaintenanceDelivery) (bool, error) {
	if err := s.admission.Check(ctx, d); err != nil {
		return false, err
	}
	return s.Store.Deliver(ctx, d)
}
func (s *fencedStore) Complete(ctx context.Context, r lifecyclev1.MaintenanceResult) error {
	// Validate the Redis request binding before the repository can reopen.
	request, err := s.Store.Get(ctx, r.Delivery.Command.ID)
	if err != nil {
		return err
	}
	if request == nil || request.Delivery != r.Delivery ||
		(request.State != "delivered" && request.State != "indeterminate" && request.State != "completed") ||
		(request.Result != nil && *request.Result != r) {
		return ErrConflict
	}
	if err := s.admission.Complete(ctx, r); err != nil {
		return err
	}
	return s.Store.Complete(ctx, r)
}

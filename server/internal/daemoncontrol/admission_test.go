package daemoncontrol

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	lifecyclev1 "github.com/multica-ai/multica/server/pkg/lifecycle/v1"
)

func admissionDelivery(operation string, generation uint64) lifecyclev1.MaintenanceDelivery {
	c := commandFixture()
	c.ID, c.Operation, c.Generation = uuid.NewString(), operation, generation
	c.Holder = "fixture-holder"
	if operation == "acquire" || operation == "recover" {
		c.ExpiresAt = c.IssuedAt.Add(time.Hour)
	}
	return lifecyclev1.MaintenanceDelivery{Command: c, OwnerID: fixtureOwner, Scope: strings.Repeat("a", 64)}
}

func admissionResult(d lifecyclev1.MaintenanceDelivery) lifecyclev1.MaintenanceResult {
	c := d.Command
	s := lifecyclev1.MaintenanceStatus{ObservedAt: time.Now().UTC(), Host: c.Host, Profile: c.Profile,
		Generation: c.Generation, Blocked: c.Operation != "release"}
	if s.Blocked {
		s.Generation++
		s.Holder, s.ExpiresAt = c.Holder, c.ExpiresAt
	}
	return lifecyclev1.MaintenanceResult{Delivery: d, Code: "ok", Status: s}
}

func confirmedAdmission() admissionState {
	d := admissionDelivery("acquire", 0)
	r := admissionResult(d)
	return admissionState{Version: 1, Closed: true, Generation: 1, Delivery: d, Result: &r}
}

func TestMaintenanceAdmissionTransitions(t *testing.T) {
	now := time.Now().UTC()
	for _, field := range []string{"owner", "scope", "daemon", "host", "profile", "holder", "generation"} {
		t.Run("release rejects wrong "+field, func(t *testing.T) {
			s := confirmedAdmission()
			d := admissionDelivery("release", 1)
			switch field {
			case "owner":
				d.OwnerID = fixtureOther
			case "scope":
				d.Scope = strings.Repeat("b", 64)
			case "daemon":
				d.Command.DaemonID = "other"
			case "host":
				d.Command.Host = "other"
			case "profile":
				d.Command.Profile = "other"
			case "holder":
				d.Command.Holder = "other"
			case "generation":
				d.Command.Generation = 0
			}
			if !errors.Is(s.prepare(d, now), ErrConflict) || !s.Closed {
				t.Fatal("mismatched release changed admission")
			}
		})
	}
	t.Run("release replay then stale result after recovery", func(t *testing.T) {
		s := confirmedAdmission()
		release := admissionDelivery("release", 1)
		if err := s.prepare(release, now); err != nil {
			t.Fatal(err)
		}
		r := admissionResult(release)
		if err := s.complete(r); err != nil || s.Closed {
			t.Fatalf("release: %v, state=%+v", err, s)
		}
		if err := s.complete(r); err != nil {
			t.Fatal("identical release replay rejected:", err)
		}
		acquire := admissionDelivery("acquire", 1)
		if err := s.prepare(acquire, now); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(s.complete(r), ErrConflict) || !s.Closed {
			t.Fatal("old release reopened a newer acquisition")
		}
	})
	t.Run("uncertain bounded generation recovery", func(t *testing.T) {
		for _, generation := range []uint64{0, 1} {
			s := confirmedAdmission()
			s.Generation, s.Uncertain, s.Result = 0, true, nil
			s.Delivery.Command.IssuedAt = now.Add(-6 * time.Minute)
			d := admissionDelivery("recover", generation)
			if err := s.prepare(d, now); err != nil {
				t.Fatal(err)
			}
			if err := s.complete(admissionResult(d)); err != nil || s.Generation != generation+1 || s.Uncertain || !s.Closed {
				t.Fatalf("recovery: %v, state=%+v", err, s)
			}
		}
	})
	t.Run("uncertain generations cannot jump or release", func(t *testing.T) {
		for _, d := range []lifecyclev1.MaintenanceDelivery{admissionDelivery("recover", 3), admissionDelivery("release", 1)} {
			s := confirmedAdmission()
			s.Uncertain, s.Result = true, nil
			s.Delivery.Command.IssuedAt = now.Add(-6 * time.Minute)
			if !errors.Is(s.prepare(d, now), ErrConflict) {
				t.Fatal("uncertain gate accepted release or generation jump")
			}
		}
	})
	t.Run("inflight command cannot be superseded", func(t *testing.T) {
		s := confirmedAdmission()
		s.Uncertain, s.Result = true, nil
		if !errors.Is(s.prepare(admissionDelivery("recover", 1), now), ErrConflict) {
			t.Fatal("superseded a bounded inflight command")
		}
	})
	t.Run("overflow cannot wrap recovery generation", func(t *testing.T) {
		s := confirmedAdmission()
		s.Generation, s.Uncertain = math.MaxUint64, true
		s.Delivery.Command.IssuedAt = now.Add(-6 * time.Minute)
		if !errors.Is(s.prepare(admissionDelivery("recover", 0), now), ErrConflict) {
			t.Fatal("generation wrapped")
		}
	})
	t.Run("scope change requires explicit recovery while held", func(t *testing.T) {
		s := confirmedAdmission()
		d := admissionDelivery("recover", 1)
		d.Scope = strings.Repeat("b", 64)
		if err := s.prepare(d, now); err != nil {
			t.Fatal(err)
		}
		if err := s.complete(admissionResult(d)); err != nil || !s.Closed || s.Generation != 2 {
			t.Fatalf("new scope recovery: %v, %+v", err, s)
		}
	})
	t.Run("uncertain target correction never releases admission", func(t *testing.T) {
		s := confirmedAdmission()
		s.Uncertain, s.Result = true, nil
		d := admissionDelivery("recover", 1)
		d.Command.Host = "corrected-host"
		if !errors.Is(s.prepare(d, now), ErrConflict) {
			t.Fatal("unexpired intent target was replaced")
		}
		s.Delivery.Command.IssuedAt = now.Add(-6 * time.Minute)
		if err := s.prepare(d, now); err != nil || !s.Closed {
			t.Fatalf("explicit uncertain target correction: %v, %+v", err, s)
		}
	})
	t.Run("invalid positive result cannot acknowledge transition", func(t *testing.T) {
		s := confirmedAdmission()
		d := admissionDelivery("release", 1)
		if err := s.prepare(d, now); err != nil {
			t.Fatal(err)
		}
		r := admissionResult(d)
		r.Status.Generation = 0
		if !errors.Is(s.complete(r), ErrConflict) || !s.Closed || s.Result != nil {
			t.Fatal("invalid positive result acknowledged")
		}
	})
	for _, code := range []string{"rejected", "persistence_fault", "outcome_unknown", "target_mismatch", "ok"} {
		t.Run("nonpositive release stays closed "+code, func(t *testing.T) {
			s := confirmedAdmission()
			d := admissionDelivery("release", 1)
			if err := s.prepare(d, now); err != nil {
				t.Fatal(err)
			}
			r := admissionResult(d)
			r.Code = code
			if code == "ok" {
				r.Status.PersistenceFault = true
			}
			if err := s.complete(r); err != nil || !s.Closed || !s.Uncertain {
				t.Fatalf("failure reopened gate: %v, %+v", err, s)
			}
		})
	}
}

type admissionSpy struct {
	prepared, checked, completed int
	err                          error
}

func (s *admissionSpy) Prepare(context.Context, lifecyclev1.MaintenanceDelivery) error {
	s.prepared++
	return s.err
}
func (s *admissionSpy) Check(context.Context, lifecyclev1.MaintenanceDelivery) error {
	s.checked++
	return s.err
}
func (s *admissionSpy) Complete(context.Context, lifecyclev1.MaintenanceResult) error {
	s.completed++
	return s.err
}

func TestMaintenanceFencedStoreFailures(t *testing.T) {
	ctx := context.Background()
	d := admissionDelivery("acquire", 0)
	backend := &fixtureStore{}
	repository := &admissionSpy{err: errors.New("repository unavailable")}
	store := NewFencedStore(backend, repository)
	if _, err := store.Create(ctx, d); err == nil || backend.created != 0 {
		t.Fatal("delivered without persisting server closure")
	}
	if delivered, err := store.Deliver(ctx, d); err == nil || delivered {
		t.Fatal("delivery bypassed repository failure")
	}
	repository.err = nil
	backend.err = errors.New("Redis unavailable")
	if _, err := store.Create(ctx, d); err == nil || repository.prepared != 2 {
		t.Fatal("Redis failure was hidden or repository fence not attempted")
	}
	r := admissionResult(d)
	if err := store.Complete(ctx, r); err == nil || repository.completed != 0 {
		t.Fatal("result acceptance bypassed unavailable request binding")
	}
	backend.err = nil
	backend.request = &lifecyclev1.MaintenanceRequest{Delivery: d, State: "pending"}
	if !errors.Is(store.Complete(ctx, r), ErrConflict) || repository.completed != 0 {
		t.Fatal("an undelivered command result reached admission")
	}
	backend.request.State = "delivered"
	repository.err = errors.New("repository commit refused")
	if err := store.Complete(ctx, r); err == nil {
		t.Fatal("repository completion failure was hidden")
	}
	repository.err = nil
	if err := store.Complete(ctx, r); err != nil {
		t.Fatal(err)
	}
}

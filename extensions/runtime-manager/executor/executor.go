package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"time"
)

type Outcome struct {
	RequestID     string `json:"request_id"`
	RequestDigest string `json:"request_digest"`
	Incarnation   string `json:"incarnation"`
	State         string `json:"state"`
	Generation    uint64 `json:"generation"`
	Epoch         uint64 `json:"epoch"`
	Child         Child  `json:"child"`
	CompleteDrain bool   `json:"complete_drain"`
	Production    bool   `json:"production_transactions"`
	Historical    bool   `json:"historical"`
	Coverage      string `json:"remote_coverage"`
}

func requestDigest(r Request) string {
	data, _ := json.Marshal(r)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func (o Outcome) Validate(r Request) error {
	if o.RequestID != r.ID || o.RequestDigest != requestDigest(r) || o.Incarnation != r.Incarnation ||
		o.Generation != r.Generation || o.Epoch != r.Epoch || o.CompleteDrain || o.Production || o.Coverage != "unknown" {
		return ErrDenied
	}
	switch o.State {
	case "open", "closed":
		if ((o.State == "closed" && r.Operation == "close") || (o.State == "open" && r.Operation == "open")) && o.Child == (Child{}) {
			return nil
		}
	case "unknown":
		if r.Operation == "launch" || r.Operation == "replace" || r.Operation == "observe" {
			return nil
		}
	case "running", "exited":
		if (r.Operation == "launch" || r.Operation == "replace" || r.Operation == "observe") && o.Child.valid(r.Binding, r.Definition) {
			return nil
		}
	default:
	}
	return ErrDenied
}

type receipt struct {
	Request Request `json:"request"`
	Outcome Outcome `json:"outcome"`
}

type attempt struct {
	ID         string     `json:"id"`
	Definition Definition `json:"definition"`
	State      string     `json:"state"`
	Child      Child      `json:"child"`
}

type state struct {
	Contract     string             `json:"contract"`
	Binding      Binding            `json:"binding"`
	Definitions  [2]Definition      `json:"definitions"`
	Incarnation  string             `json:"incarnation"`
	Generation   uint64             `json:"generation"`
	Epoch        uint64             `json:"epoch"`
	Closed       bool               `json:"closed"`
	Holder       string             `json:"holder"`
	HoldID       string             `json:"hold_id"`
	HoldExpires  time.Time          `json:"hold_expires"`
	Receipts     map[string]receipt `json:"receipts"`
	Attempts     map[string]attempt `json:"attempts"`
	Replacements map[string]string  `json:"replacements"`
}

// Store owns exclusive serialization for its entire lifetime, not one append.
// Commit errors are ambiguous and latch the executor unavailable until reload.
type Store interface {
	Load() ([]byte, error)
	Commit([]byte) error
	Close() error
}

type Executor struct {
	mu        sync.Mutex
	store     Store
	authority Authority
	processes Processes
	now       func() time.Time
	state     state
	fault     error
}

func New(store Store, authority Authority, processes Processes, binding Binding, definitions [2]Definition, incarnation string, now func() time.Time) (*Executor, error) {
	if store == nil || authority == nil || processes == nil || now == nil || !binding.valid() || !identifier(incarnation) ||
		!definitions[0].valid() || !definitions[1].valid() || definitions[0].Component == definitions[1].Component {
		return nil, ErrDenied
	}
	e := &Executor{store: store, authority: authority, processes: processes, now: now}
	e.state = state{Contract: Contract, Binding: binding, Definitions: definitions, Closed: true,
		Receipts: map[string]receipt{}, Attempts: map[string]attempt{}, Replacements: map[string]string{}}
	data, err := store.Load()
	if err != nil {
		return nil, err
	}
	if len(data) != 0 {
		e.state = state{}
		if err := decodeStored(data, &e.state); err != nil {
			return nil, err
		}
		if err := e.validateState(binding, definitions); err != nil {
			return nil, err
		}
		// A restart never inherits an old open decision. Existing child identity
		// is retained, but all nonterminal outcomes require exact observation.
		e.state.Closed = true
		for component, a := range e.state.Attempts {
			if a.State != "exited" {
				a.State = "unknown"
				e.state.Attempts[component] = a
			}
		}
	}
	e.state.Incarnation = incarnation
	if err := e.persist(); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Executor) validateState(binding Binding, definitions [2]Definition) error {
	s := e.state
	if s.Contract != Contract || s.Binding != binding || s.Definitions != definitions || !identifier(s.Incarnation) ||
		s.Receipts == nil || s.Attempts == nil || s.Replacements == nil || len(s.Receipts) > 1024 || len(s.Attempts) > 2 {
		return ErrDenied
	}
	if (s.Holder == "") != (s.HoldID == "") || (s.Holder == "") != s.HoldExpires.IsZero() ||
		(s.Holder != "" && (!s.Closed || s.Generation == 0 || !identifier(s.HoldID))) {
		return ErrDenied
	}
	for id, receipt := range s.Receipts {
		if !identifier(id) || receipt.Request.ID != id || receipt.Request.Binding != binding ||
			receipt.Request.validate(receipt.Request.Expires.Add(-time.Nanosecond)) != nil || receipt.Outcome.Validate(receipt.Request) != nil {
			return ErrDenied
		}
	}
	for component, a := range s.Attempts {
		if !identifier(a.ID) || component != a.Definition.Component || !e.approved(a.Definition) ||
			(a.Child != (Child{}) && !a.Child.valid(binding, a.Definition)) ||
			(a.State != "intent" && a.State != "created" && a.State != "running" && a.State != "unknown" && a.State != "exited") {
			return ErrDenied
		}
		r, exists := s.Receipts[a.ID]
		if !exists || r.Request.Definition != a.Definition || (r.Request.Operation != "launch" && r.Request.Operation != "replace") {
			return ErrDenied
		}
	}
	for key, id := range s.Replacements {
		r, exists := s.Receipts[id]
		if !exists || r.Request.Operation != "replace" || key != r.Request.Transaction+":"+r.Request.Definition.Component {
			return ErrDenied
		}
	}
	return nil
}

func (e *Executor) approved(definition Definition) bool {
	return definition == e.state.Definitions[0] || definition == e.state.Definitions[1]
}

func (e *Executor) persist() error {
	data, err := json.Marshal(e.state)
	if err == nil {
		err = e.store.Commit(data)
	}
	if err != nil {
		e.fault = err
	}
	return err
}

func (e *Executor) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.fault = ErrUnavailable
	return e.store.Close()
}

func sameRequest(a, b Request) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func (e *Executor) result(r Request, status string, child Child) Outcome {
	return Outcome{RequestID: r.ID, RequestDigest: requestDigest(r), Incarnation: r.Incarnation,
		State: status, Child: child, Generation: e.state.Generation, Epoch: e.state.Epoch, Coverage: "unknown"}
}

func (e *Executor) finish(r Request, status string, child Child) (Outcome, error) {
	out := e.result(r, status, child)
	e.state.Receipts[r.ID] = receipt{r, out}
	return out, e.persist()
}

// Authentication may block, so it happens outside the local launch serializer.
// All deadlines/incarnation/epochs are rechecked after it returns and under mu.
func (e *Executor) Execute(ctx context.Context, r Request) (Outcome, error) {
	if r.Core != nil {
		proof := *r.Core
		r.Core = &proof
	}
	if err := r.validate(e.now()); err != nil {
		return Outcome{}, err
	}
	if err := e.authority.Verify(ctx, r); err != nil {
		return Outcome{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fault != nil {
		return Outcome{}, errors.Join(ErrUnavailable, e.fault)
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if r.Binding != e.state.Binding || r.validate(e.now()) != nil {
		return Outcome{}, ErrDenied
	}
	if old, exists := e.state.Receipts[r.ID]; exists {
		if !sameRequest(old.Request, r) {
			return Outcome{}, ErrDenied
		}
		out := old.Outcome
		out.Historical = true
		if out.State == "unknown" {
			return out, ErrUnknown
		}
		return out, nil
	}
	if r.Incarnation != e.state.Incarnation || len(e.state.Receipts) >= 1024 {
		return Outcome{}, ErrDenied
	}
	switch r.Operation {
	case "close":
		if r.Generation <= e.state.Generation || e.state.Epoch == math.MaxUint64 || r.Epoch != e.state.Epoch+1 {
			return Outcome{}, ErrDenied
		}
		e.state.Closed, e.state.Generation, e.state.Epoch, e.state.Holder = true, r.Generation, r.Epoch, r.Core.Status.Holder
		e.state.HoldID, e.state.HoldExpires = r.Core.Delivery.Command.ID, r.Core.Status.ExpiresAt
		return e.finish(r, "closed", Child{})
	case "open":
		if !e.state.Closed || r.Generation != e.state.Generation || e.state.Epoch == math.MaxUint64 || r.Epoch != e.state.Epoch+1 ||
			(r.Core == nil && (r.Generation != 0 || e.state.Holder != "")) ||
			(r.Core != nil && r.Core.Delivery.Command.Holder != e.state.Holder) || e.unknown() {
			return Outcome{}, ErrDenied
		}
		e.state.Closed, e.state.Epoch, e.state.Holder = false, r.Epoch, ""
		e.state.HoldID, e.state.HoldExpires = "", time.Time{}
		return e.finish(r, "open", Child{})
	case "launch", "replace":
		return e.launch(ctx, r)
	case "observe":
		if r.Generation != e.state.Generation || r.Epoch != e.state.Epoch || !e.approved(r.Definition) {
			return Outcome{}, ErrDenied
		}
		a, exists := e.state.Attempts[r.Definition.Component]
		if !exists || a.ID != r.Attempt || a.Definition != r.Definition || a.State == "exited" {
			return Outcome{}, ErrDenied
		}
		status, child, err := e.processes.Observe(ctx, a.ID, a.Child)
		if err != nil || (status != "running" && status != "exited") || !child.valid(e.state.Binding, a.Definition) ||
			(a.Child != (Child{}) && a.Child != child) {
			a.State = "unknown"
			e.state.Attempts[r.Definition.Component] = a
			out, saveErr := e.finish(r, "unknown", a.Child)
			return out, errors.Join(ErrUnknown, err, saveErr)
		}
		a.State, a.Child = status, child
		e.state.Attempts[r.Definition.Component] = a
		return e.finish(r, status, child)
	default:
		return Outcome{}, ErrDenied
	}
}

func (e *Executor) unknown() bool {
	for _, a := range e.state.Attempts {
		if a.State != "running" && a.State != "exited" {
			return true
		}
	}
	return false
}

func (e *Executor) launch(ctx context.Context, r Request) (Outcome, error) {
	if r.Generation != e.state.Generation || r.Epoch != e.state.Epoch || !e.approved(r.Definition) || e.unknown() {
		return Outcome{}, ErrDenied
	}
	if r.Operation == "replace" {
		if !e.state.Closed || r.Core.Status.Holder != e.state.Holder ||
			r.Core.Delivery.Command.ID != e.state.HoldID || !r.Core.Status.ExpiresAt.Equal(e.state.HoldExpires) {
			return Outcome{}, ErrDenied
		}
		if _, used := e.state.Replacements[r.Transaction+":"+r.Definition.Component]; used {
			return Outcome{}, ErrDenied
		}
	} else if e.state.Closed {
		return Outcome{}, ErrDenied
	}
	if old, exists := e.state.Attempts[r.Definition.Component]; exists && old.State != "exited" {
		return Outcome{}, ErrDenied
	}
	a := attempt{ID: r.ID, Definition: r.Definition, State: "intent"}
	e.state.Attempts[r.Definition.Component] = a
	if r.Operation == "replace" {
		e.state.Replacements[r.Transaction+":"+r.Definition.Component] = r.ID
	}
	// The durable intent consumes replacement authorization before any OS call.
	if _, err := e.finish(r, "unknown", Child{}); err != nil {
		return Outcome{}, err
	}
	child, err := e.processes.CreateSuspended(ctx, r.Definition, r.ID)
	if err != nil || !child.valid(e.state.Binding, r.Definition) {
		a.State = "unknown"
		e.state.Attempts[r.Definition.Component] = a
		out, saveErr := e.finish(r, "unknown", Child{})
		return out, errors.Join(ErrUnknown, err, saveErr)
	}
	a.Child, a.State = child, "created"
	e.state.Attempts[r.Definition.Component] = a
	if err := e.persist(); err != nil {
		return Outcome{}, err // Never resume after an ambiguous child receipt write.
	}
	if ctx.Err() != nil || !r.Expires.After(e.now()) {
		a.State = "unknown"
		err = ErrDenied
	} else {
		err = e.processes.Resume(ctx, child)
		a.State = "running"
		if err != nil {
			a.State = "unknown"
		}
	}
	e.state.Attempts[r.Definition.Component] = a
	out, saveErr := e.finish(r, a.State, child)
	if err != nil {
		return out, errors.Join(ErrUnknown, err, saveErr)
	}
	return out, saveErr
}

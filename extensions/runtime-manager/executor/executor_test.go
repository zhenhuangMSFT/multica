package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	lifecyclev1 "github.com/multica-ai/multica/server/pkg/lifecycle/v1"
)

type authorityFunc func(context.Context, Request) error

func (f authorityFunc) Verify(ctx context.Context, r Request) error { return f(ctx, r) }

type fakeProcesses struct {
	creates, resumes int
	create           func()
	createError      error
	resumeError      error
	status           string
	observed         Child
	observeError     error
}

func (p *fakeProcesses) CreateSuspended(_ context.Context, d Definition, _ string) (Child, error) {
	p.creates++
	if p.create != nil {
		p.create()
	}
	return Child{PID: 42, Created: time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC),
		Image: d.Artifact, OwnerSID: "fixture-sid", Session: 1, Containment: "22222222-2222-4222-8222-222222222222"}, p.createError
}
func (p *fakeProcesses) Resume(context.Context, Child) error { p.resumes++; return p.resumeError }
func (p *fakeProcesses) Observe(_ context.Context, _ string, child Child) (string, Child, error) {
	if p.observed != (Child{}) {
		child = p.observed
	}
	return p.status, child, p.observeError
}

type fixture struct {
	e        *Executor
	dir      string
	now      time.Time
	binding  Binding
	defs     [2]Definition
	instance string
	process  *fakeProcesses
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{dir: t.TempDir(), now: time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC), instance: uuid.NewString(), process: &fakeProcesses{status: "exited"}}
	f.binding = Binding{Installation: uuid.NewString(), Host: "fixture-host", OwnerID: uuid.NewString(),
		OwnerSID: "fixture-sid", Session: 1, Profile: "fixture", DaemonID: "fixture-daemon", Scope: strings.Repeat("a", 64)}
	f.defs = [2]Definition{
		{Component: "daemon", Artifact: strings.Repeat("b", 64), Config: strings.Repeat("c", 64), Launch: strings.Repeat("d", 64)},
		{Component: "tunnel", Artifact: strings.Repeat("e", 64), Config: strings.Repeat("f", 64), Launch: strings.Repeat("a", 64)},
	}
	f.reload(t)
	t.Cleanup(func() {
		if err := f.e.Close(); err != nil {
			t.Error(err)
		}
	})
	return f
}

func (f *fixture) reload(t *testing.T) {
	t.Helper()
	if f.e != nil {
		if err := f.e.Close(); err != nil {
			t.Fatal(err)
		}
		f.instance = uuid.NewString()
	}
	journal, err := OpenFileJournal(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	f.e, err = New(journal, authorityFunc(func(context.Context, Request) error { return nil }), f.process,
		f.binding, f.defs, f.instance, func() time.Time { return f.now })
	if err != nil {
		journal.Close()
		t.Fatal(err)
	}
}

func (f *fixture) request(operation string) Request {
	r := Request{Contract: Contract, ID: uuid.NewString(), Binding: f.binding, Incarnation: f.instance,
		Operation: operation, Generation: f.e.state.Generation, Epoch: f.e.state.Epoch, Expires: f.now.Add(30 * time.Second)}
	if operation == "launch" || operation == "replace" || operation == "observe" {
		r.Definition = f.defs[0]
	}
	if operation == "launch" {
		r.Reason = "initial"
	}
	if operation == "replace" {
		r.Reason, r.Transaction = "replacement", uuid.NewString()
		r.Core = f.proof("acquire", r.Generation)
		r.Core.Delivery.Command.ID = f.e.state.HoldID
		r.Core.Delivery.Command.ExpiresAt = f.e.state.HoldExpires
		r.Core.Status.ExpiresAt = f.e.state.HoldExpires
	}
	if operation == "open" || operation == "close" {
		r.Epoch++
		if operation == "close" {
			r.Generation++
			r.Core = f.proof("acquire", r.Generation)
		} else if r.Generation > 0 {
			r.Core = f.proof("release", r.Generation)
		}
	}
	if operation == "observe" {
		r.Attempt = f.e.state.Attempts[r.Definition.Component].ID
	}
	return r
}

func (f *fixture) proof(operation string, generation uint64) *lifecyclev1.MaintenanceResult {
	c := lifecyclev1.MaintenanceCommand{ID: uuid.NewString(), Operation: operation, Host: f.binding.Host,
		Profile: f.binding.Profile, DaemonID: f.binding.DaemonID, Holder: "fixture-holder", Generation: generation, IssuedAt: f.now}
	s := lifecyclev1.MaintenanceStatus{ObservedAt: f.now, Host: c.Host, Profile: c.Profile, Generation: generation}
	if operation != "release" {
		c.Generation--
		c.ExpiresAt = f.now.Add(time.Hour)
		s.Blocked, s.Holder, s.ExpiresAt = true, c.Holder, c.ExpiresAt
	}
	return &lifecyclev1.MaintenanceResult{Delivery: lifecyclev1.MaintenanceDelivery{Command: c, OwnerID: f.binding.OwnerID, Scope: f.binding.Scope}, Code: "ok", Status: s}
}

func (f *fixture) execute(t *testing.T, r Request) Outcome {
	t.Helper()
	out, err := f.e.Execute(context.Background(), r)
	if err != nil || out.Validate(r) != nil {
		t.Fatalf("%s: %+v, %v", r.Operation, out, err)
	}
	return out
}

func TestExecutorOneUseReplacementAndReplay(t *testing.T) {
	f := newFixture(t)
	f.execute(t, f.request("open"))
	launch := f.request("launch")
	out := f.execute(t, launch)
	if out.State != "running" || f.process.creates != 1 || f.process.resumes != 1 {
		t.Fatal(out)
	}
	replay := f.execute(t, launch)
	if !replay.Historical || f.process.creates != 1 {
		t.Fatal("replay created another child or claimed fresh evidence")
	}
	changed := launch
	changed.Reason = "component_respawn"
	if _, err := f.e.Execute(context.Background(), changed); !errors.Is(err, ErrDenied) {
		t.Fatal("changed replay accepted")
	}
	f.execute(t, f.request("close"))
	if f.e.state.Attempts["daemon"].Child != out.Child {
		t.Fatal("close discarded pre-acquisition child")
	}
	for _, reason := range []string{"initial", "scheduler_retry", "component_respawn"} {
		r := f.request("launch")
		r.Reason = reason
		if _, err := f.e.Execute(context.Background(), r); !errors.Is(err, ErrDenied) {
			t.Fatal("ordinary path bypassed held gate")
		}
	}
	f.execute(t, f.request("observe"))
	replacement := f.request("replace")
	f.execute(t, replacement)
	if !f.e.state.Closed || f.process.creates != 2 {
		t.Fatal("replacement reopened gate or failed to create once")
	}
	f.execute(t, replacement)
	f.execute(t, f.request("observe"))
	second := f.request("replace")
	second.Transaction = replacement.Transaction
	if _, err := f.e.Execute(context.Background(), second); !errors.Is(err, ErrDenied) || f.process.creates != 2 {
		t.Fatal("transaction/component replacement authorization reused")
	}
	f.reload(t)
	f.execute(t, replacement)
	if f.process.creates != 2 || !f.e.state.Closed {
		t.Fatal("restart replay reused replacement")
	}
}

func TestExecutorUnknownRecoveryNeverGuessesExit(t *testing.T) {
	for _, failure := range []string{"creation", "resume", "restart", "pid-reuse", "wrong-session", "wrong-image", "wrong-sid", "access-denied"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			f.execute(t, f.request("open"))
			if failure == "creation" {
				f.process.createError = errors.New("ambiguous create")
			}
			if failure == "resume" {
				f.process.resumeError = errors.New("ambiguous resume")
			}
			r := f.request("launch")
			out, err := f.e.Execute(context.Background(), r)
			if failure == "creation" || failure == "resume" {
				if !errors.Is(err, ErrUnknown) || out.State != "unknown" {
					t.Fatal(out, err)
				}
			}
			f.reload(t)
			if f.e.state.Attempts["daemon"].State != "unknown" {
				t.Fatal("restart inferred child stopped")
			}
			f.execute(t, f.request("close"))
			if _, err := f.e.Execute(context.Background(), f.request("replace")); !errors.Is(err, ErrDenied) {
				t.Fatal("unknown child allowed replacement")
			}
			if failure == "pid-reuse" {
				f.process.observed = out.Child
				f.process.observed.Created = out.Child.Created.Add(time.Second)
			}
			switch failure {
			case "wrong-session":
				f.process.observed = out.Child
				f.process.observed.Session++
			case "wrong-image":
				f.process.observed = out.Child
				f.process.observed.Image = strings.Repeat("0", 64)
			case "wrong-sid":
				f.process.observed = out.Child
				f.process.observed.OwnerSID = "other"
			}
			if failure == "access-denied" {
				f.process.observeError = errors.New("access denied")
			}
			observe := f.request("observe")
			result, observeErr := f.e.Execute(context.Background(), observe)
			if failure == "creation" || failure == "pid-reuse" || failure == "wrong-session" ||
				failure == "wrong-image" || failure == "wrong-sid" || failure == "access-denied" {
				if !errors.Is(observeErr, ErrUnknown) || result.State != "unknown" || f.process.creates != 1 {
					t.Fatal(result, observeErr)
				}
				if _, err := f.e.Execute(context.Background(), f.request("open")); !errors.Is(err, ErrDenied) {
					t.Fatal("unknown child allowed reopening")
				}
			} else if observeErr != nil || result.State != "exited" {
				t.Fatal(result, observeErr)
			}
		})
	}
}

func TestExecutorLaunchVersusClose(t *testing.T) {
	for _, winner := range []string{"launch", "close"} {
		t.Run(winner, func(t *testing.T) {
			f := newFixture(t)
			f.execute(t, f.request("open"))
			launch, closeRequest := f.request("launch"), f.request("close")
			if winner == "close" {
				f.execute(t, closeRequest)
				if _, err := f.e.Execute(context.Background(), launch); !errors.Is(err, ErrDenied) || f.process.creates != 0 {
					t.Fatal("delayed ordinary grant crossed close")
				}
				return
			}
			entered, resumeCreate := make(chan struct{}), make(chan struct{})
			f.process.create = func() { close(entered); <-resumeCreate }
			var wg sync.WaitGroup
			wg.Add(2)
			var launchErr, closeErr error
			go func() { defer wg.Done(); _, launchErr = f.e.Execute(context.Background(), launch) }()
			<-entered
			closeFinished := make(chan struct{})
			go func() {
				defer wg.Done()
				_, closeErr = f.e.Execute(context.Background(), closeRequest)
				close(closeFinished)
			}()
			select {
			case <-closeFinished:
				t.Fatal("close acknowledgment outran unresolved creation")
			case <-time.After(10 * time.Millisecond):
			}
			close(resumeCreate)
			wg.Wait()
			if launchErr != nil || closeErr != nil || !f.e.state.Closed || f.e.state.Attempts["daemon"].State != "running" || f.process.creates != 1 {
				t.Fatal(launchErr, closeErr)
			}
		})
	}
}

func TestExecutorDelayedAuthorityAndDuplicateWrappers(t *testing.T) {
	f := newFixture(t)
	f.execute(t, f.request("open"))
	launch, closeRequest := f.request("launch"), f.request("close")
	entered, answer := make(chan struct{}), make(chan struct{})
	f.e.authority = authorityFunc(func(_ context.Context, r Request) error {
		if r.ID == launch.ID {
			close(entered)
			<-answer
		}
		return nil
	})
	done := make(chan error)
	go func() { _, err := f.e.Execute(context.Background(), launch); done <- err }()
	<-entered
	f.execute(t, closeRequest)
	close(answer)
	if err := <-done; !errors.Is(err, ErrDenied) || f.process.creates != 0 {
		t.Fatal("delayed authenticated response bypassed current gate", err)
	}
	if store, err := OpenFileJournal(f.dir); err == nil {
		store.Close()
		t.Fatal("second wrapper acquired same installation journal")
	}
}

func TestExecutorStrictContractAndUnavailableAdapters(t *testing.T) {
	f := newFixture(t)
	open := f.request("open")
	data, err := json.Marshal(open)
	if err != nil {
		t.Fatal(err)
	}
	if parsed, err := DecodeRequest(bytes.NewReader(data)); err != nil || !sameRequest(parsed, open) {
		t.Fatal(parsed, err)
	}
	for _, edit := range []func(string) string{
		func(s string) string {
			return strings.Replace(s, `"operation":"open"`, `"operation":"open","operation":"close"`, 1)
		},
		func(s string) string { return strings.Replace(s, `"operation":"open"`, `"shell":"whoami"`, 1) },
		func(s string) string { return strings.Replace(s, `"operation":"open"`, `"operation":null`, 1) },
		func(s string) string { return s + `{}` },
	} {
		if _, err := DecodeRequest(strings.NewReader(edit(string(data)))); err == nil {
			t.Fatal("malformed contract accepted")
		}
	}
	for _, field := range []string{"owner", "scope", "host", "profile", "session", "incarnation", "deadline", "epoch"} {
		t.Run(field, func(t *testing.T) {
			r := open
			r.ID = uuid.NewString()
			switch field {
			case "owner":
				r.Binding.OwnerID = uuid.NewString()
			case "scope":
				r.Binding.Scope = strings.Repeat("f", 64)
			case "host":
				r.Binding.Host = "other"
			case "profile":
				r.Binding.Profile = "other"
			case "session":
				r.Binding.Session++
			case "incarnation":
				r.Incarnation = uuid.NewString()
			case "deadline":
				r.Expires = f.now
			case "epoch":
				r.Epoch += 2
			}
			if _, err := f.e.Execute(context.Background(), r); !errors.Is(err, ErrDenied) {
				t.Fatal("mismatched authorization accepted", err)
			}
		})
	}
	f.e.authority = UnavailableAuthority{}
	if _, err := f.e.Execute(context.Background(), open); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing authority silently authorized", err)
	}
	if _, err := (UnavailableProcesses{}).CreateSuspended(context.Background(), f.defs[0], uuid.NewString()); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

type failingStore struct {
	Store
	failAt, commits int
	afterWrite      bool
}

func (s *failingStore) Commit(data []byte) error {
	s.commits++
	if s.commits == s.failAt {
		if s.afterWrite {
			if err := s.Store.Commit(data); err != nil {
				return err
			}
		}
		return errors.New("injected sync failure")
	}
	return s.Store.Commit(data)
}

func TestExecutorPersistenceFailureBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after bool
	}{{"before-write", false}, {"after-write", true}} {
		for _, boundary := range []int{1, 2, 3} {
			t.Run(tc.name+string(rune('0'+boundary)), func(t *testing.T) {
				f := newFixture(t)
				f.execute(t, f.request("open"))
				store := &failingStore{Store: f.e.store, failAt: boundary, afterWrite: tc.after}
				f.e.store = store
				_, err := f.e.Execute(context.Background(), f.request("launch"))
				if err == nil || f.e.fault == nil || (boundary == 1 && f.process.creates != 0) || (boundary == 2 && f.process.resumes != 0) {
					t.Fatal("persistence failure crossed launch boundary", err)
				}
				if _, err := f.e.Execute(context.Background(), f.request("launch")); !errors.Is(err, ErrUnavailable) {
					t.Fatal("ambiguous persistence did not latch unavailable", err)
				}
				f.reload(t)
				if (boundary > 1 || tc.after) && f.e.state.Attempts["daemon"].State != "unknown" {
					t.Fatal("restart lost ambiguous intent")
				}
			})
		}
	}
}

func TestExecutorJournalCorruptionAndBinding(t *testing.T) {
	for _, corruption := range []string{"torn", "hash", "unknown", "duplicate", "binding"} {
		t.Run(corruption, func(t *testing.T) {
			f := newFixture(t)
			if err := f.e.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.dir, "executor.jsonl")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch corruption {
			case "torn":
				data = data[:len(data)-1]
			case "hash":
				data = bytes.Replace(data, []byte(`"closed":true`), []byte(`"closed":false`), 1)
			case "unknown":
				data = bytes.Replace(data, []byte(`"sequence":1`), []byte(`"sequence":1,"extra":true`), 1)
			case "duplicate":
				data = bytes.Replace(data, []byte(`"sequence":1`), []byte(`"sequence":1,"sequence":1`), 1)
			case "binding":
				f.binding.OwnerSID = "other"
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			store, err := OpenFileJournal(f.dir)
			if err == nil {
				defer store.Close()
				_, err = New(store, UnavailableAuthority{}, UnavailableProcesses{}, f.binding, f.defs, uuid.NewString(), func() time.Time { return f.now })
			}
			if err == nil {
				t.Fatal("corrupt or wrong-binding persisted state accepted")
			}
		})
	}
}
func TestExecutorDeadlineAfterAuthenticationAndBeforeResume(t *testing.T) {
	for _, boundary := range []string{"authentication", "create"} {
		t.Run(boundary, func(t *testing.T) {
			f := newFixture(t)
			f.execute(t, f.request("open"))
			r := f.request("launch")
			expire := func() { f.now = f.now.Add(time.Minute) }
			if boundary == "authentication" {
				f.e.authority = authorityFunc(func(context.Context, Request) error { expire(); return nil })
			} else {
				f.process.create = expire
			}
			out, err := f.e.Execute(context.Background(), r)
			if err == nil || f.process.resumes != 0 || (boundary == "authentication" && f.process.creates != 0) {
				t.Fatal("expired decision created or resumed child", out, err)
			}
			if boundary == "create" && out.State != "unknown" {
				t.Fatal("expired suspended child was inferred stopped")
			}
		})
	}
}

func TestExecutorConcurrentDuplicateAndUnknownReconciliation(t *testing.T) {
	f := newFixture(t)
	f.execute(t, f.request("open"))
	r := f.request("launch")
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := f.e.Execute(context.Background(), r); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.process.creates != 1 || f.process.resumes != 1 {
		t.Fatal("duplicate wrappers created multiple children")
	}
	f.reload(t)
	f.process.status = "unknown"
	if _, err := f.e.Execute(context.Background(), f.request("observe")); !errors.Is(err, ErrUnknown) {
		t.Fatal("unknown observation was treated as exit")
	}
	f.process.status = "exited"
	f.execute(t, f.request("observe"))
	f.execute(t, f.request("open"))
	next := f.request("launch")
	next.Reason = "component_respawn"
	f.execute(t, next)
	if f.process.creates != 2 {
		t.Fatal("exact observed exit did not permit a fresh authorized launch")
	}
}

func TestExecutorReplacementExactBindingsAndStaleRelease(t *testing.T) {
	for _, field := range []string{"generation", "holder", "hold-id", "transaction", "definition", "core-owner", "core-target", "core-fault", "core-drain", "expired-holder"} {
		t.Run(field, func(t *testing.T) {
			f := newFixture(t)
			f.execute(t, f.request("close"))
			r := f.request("replace")
			switch field {
			case "generation":
				r.Generation--
			case "holder":
				r.Core.Status.Holder, r.Core.Delivery.Command.Holder = "other", "other"
			case "hold-id":
				r.Core.Delivery.Command.ID = uuid.NewString()
			case "transaction":
				r.Transaction = "not-a-transaction"
			case "definition":
				r.Definition.Artifact = strings.Repeat("a", 64)
			case "core-owner":
				r.Core.Delivery.OwnerID = uuid.NewString()
			case "core-target":
				r.Core.Delivery.Command.Host, r.Core.Status.Host = "other", "other"
			case "core-fault":
				r.Core.Status.PersistenceFault = true
			case "core-drain":
				r.Core.Status.CompleteDrain = true
			case "expired-holder":
				r.Core.Status.ExpiresAt, r.Core.Delivery.Command.ExpiresAt = f.now.Add(-time.Second), f.now.Add(-time.Second)
			}
			if _, err := f.e.Execute(context.Background(), r); !errors.Is(err, ErrDenied) || f.process.creates != 0 {
				t.Fatal("invalid replacement binding reached process adapter", err)
			}
		})
	}
	f := newFixture(t)
	f.execute(t, f.request("close"))
	stale := f.request("open")
	f.execute(t, f.request("close"))
	if _, err := f.e.Execute(context.Background(), stale); !errors.Is(err, ErrDenied) || !f.e.state.Closed {
		t.Fatal("stale open decision released newer generation")
	}
	f.execute(t, f.request("open"))
	r := f.request("launch")
	out := f.execute(t, r)
	for _, field := range []string{"digest", "incarnation", "generation", "epoch", "drain", "coverage"} {
		changed := out
		switch field {
		case "digest":
			changed.RequestDigest = strings.Repeat("0", 64)
		case "incarnation":
			changed.Incarnation = uuid.NewString()
		case "generation":
			changed.Generation++
		case "epoch":
			changed.Epoch++
		case "drain":
			changed.CompleteDrain = true
		case "coverage":
			changed.Coverage = "complete"
		}
		if changed.Validate(r) == nil {
			t.Fatal("unbound result accepted:", field)
		}
	}
}

func TestExecutorAmbiguousReplacementConsumesException(t *testing.T) {
	f := newFixture(t)
	f.execute(t, f.request("close"))
	f.process.resumeError = errors.New("lost resume acknowledgment")
	r := f.request("replace")
	if out, err := f.e.Execute(context.Background(), r); !errors.Is(err, ErrUnknown) || out.State != "unknown" {
		t.Fatal(out, err)
	}
	f.reload(t)
	f.execute(t, f.request("observe"))
	reuse := f.request("replace")
	reuse.Transaction = r.Transaction
	if _, err := f.e.Execute(context.Background(), reuse); !errors.Is(err, ErrDenied) || f.process.creates != 1 {
		t.Fatal("ambiguous replacement exception reused after exit/restart", err)
	}
	if out, err := f.e.Execute(context.Background(), r); !errors.Is(err, ErrUnknown) || !out.Historical || f.process.creates != 1 {
		t.Fatal("unknown replacement replay re-executed", out, err)
	}
}

func TestExecutorUnknownCreationExactRecoveryAndTerminalExit(t *testing.T) {
	f := newFixture(t)
	f.execute(t, f.request("open"))
	f.process.createError = errors.New("creation response lost")
	launch := f.request("launch")
	if out, err := f.e.Execute(context.Background(), launch); !errors.Is(err, ErrUnknown) || out.Child != (Child{}) {
		t.Fatal("ambiguous creation manufactured child identity", out, err)
	}
	f.reload(t)
	// Only the injected adapter supplies exact attempt-bound identity; the
	// library cannot derive it from a missing process listing.
	f.process.observed = Child{PID: 42, Created: f.now, Image: f.defs[0].Artifact,
		OwnerSID: f.binding.OwnerSID, Session: f.binding.Session, Containment: uuid.NewString()}
	out := f.execute(t, f.request("observe"))
	if out.State != "exited" || f.process.creates != 1 {
		t.Fatal("exact reconciliation recreated an unknown child", out)
	}
	f.process.status = "running"
	if _, err := f.e.Execute(context.Background(), f.request("observe")); !errors.Is(err, ErrDenied) {
		t.Fatal("terminal exit was allowed to become running", err)
	}
}

func TestExecutorStoredSchemaAndReceiptLimit(t *testing.T) {
	f := newFixture(t)
	data, err := json.Marshal(f.e.state)
	if err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(string) string{
		func(s string) string { return strings.Replace(s, `"closed":true,`, "", 1) },
		func(s string) string { return strings.Replace(s, `"closed":true`, `"closed":null`, 1) },
		func(s string) string {
			return strings.Replace(s, `"hold_expires":"0001-01-01T00:00:00Z"`, `"hold_expires":null`, 1)
		},
		func(s string) string { return strings.Replace(s, `"receipts":{}`, `"receipts":null`, 1) },
	} {
		var decoded state
		if err := decodeStored([]byte(edit(string(data))), &decoded); err == nil {
			t.Fatal("incomplete or null stored schema accepted")
		}
	}
	f.execute(t, f.request("open"))
	launch := f.request("launch")
	f.execute(t, launch)
	// Seed valid retained receipts to exercise the exact request threshold
	// without quadratic full-state journal growth obscuring the budget check.
	for len(f.e.state.Receipts) < 1024 {
		r := launch
		r.ID = uuid.NewString()
		f.e.state.Receipts[r.ID] = receipt{r, f.e.result(r, "running", f.e.state.Attempts["daemon"].Child)}
	}
	if err := f.e.persist(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.Execute(context.Background(), f.request("close")); !errors.Is(err, ErrDenied) || f.e.state.Closed {
		t.Fatal("receipt exhaustion manufactured a held gate", err)
	}
	if out := f.execute(t, launch); !out.Historical || f.process.creates != 1 {
		t.Fatal("receipt budget lost mutation replay", out)
	}
	f.reload(t)
	if out := f.execute(t, launch); !out.Historical || f.process.creates != 1 || !f.e.state.Closed {
		t.Fatal("restart lost receipts or sealed startup", out)
	}
}

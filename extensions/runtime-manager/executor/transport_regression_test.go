package executor

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

func TestTransportPublicReconnect(t *testing.T) {
	a := newAuthorityFixture(t)
	a.r = a.f.request("open")
	a.authorize()
	manager, session := transportPair(t, a)
	// Use only the public constructor/integration, not Executor.authority.
	if err := a.f.e.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenFileJournal(a.f.dir)
	if err != nil {
		t.Fatal(err)
	}
	authority := &SessionAuthority{}
	if err := authority.Bind(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	executor, err := New(journal, authority, a.f.process, a.f.binding, a.f.defs, a.f.instance,
		func() time.Time { return a.f.now })
	if err != nil {
		journal.Close()
		t.Fatal(err)
	}
	defer executor.Close()
	r := sendTransportRequest(t, a, manager, session)
	if _, err := executor.Execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	manager.Close()
	session.Close()
	a.r = a.f.request("launch")
	a.r.Generation, a.r.Epoch = r.Generation, r.Epoch
	a.authorize()
	manager, session = transportPair(t, a)
	r = sendTransportRequest(t, a, manager, session)
	if _, err := executor.Execute(context.Background(), r); err == nil || a.f.process.creates != 0 || a.f.process.resumes != 0 {
		t.Fatal("closed old session authorized a new launch", err)
	}
	if err := authority.Bind(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	out, err := executor.Execute(context.Background(), r)
	if err != nil {
		t.Fatal("public executor cannot use the fresh verified session", err)
	}
	manager.Close()
	err = session.SendResult(context.Background(), r, out, a.message(&out))
	var uncertain *TransportError
	if !errors.As(err, &uncertain) || uncertain.OperationID != r.ID || !errors.Is(err, ErrUnknown) {
		t.Fatal("lost response did not retain the exact unknown operation", err)
	}
	manager, session = transportPair(t, a)
	r = sendTransportRequest(t, a, manager, session)
	if _, err := executor.Execute(context.Background(), r); err == nil || a.f.process.creates != 1 || a.f.process.resumes != 1 {
		t.Fatal("historical reconciliation bypassed fresh session verification", err)
	}
	if err := authority.Bind(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	replay, err := executor.Execute(context.Background(), r)
	if err != nil || !replay.Historical || a.f.process.creates != 1 || a.f.process.resumes != 1 {
		t.Fatal("same executor/journal/incarnation failed same-ID reconciliation", replay, err)
	}
	if replay.RequestID != r.ID || replay.Incarnation != a.f.instance {
		t.Fatal("reconciliation changed operation or incarnation", replay)
	}
	done := make(chan error, 1)
	go func() { done <- session.SendResult(context.Background(), r, replay, a.message(&replay)) }()
	received, err := manager.ReceiveResult(context.Background(), r)
	if sendErr := <-done; err != nil || sendErr != nil || received != replay {
		t.Fatal("fresh manager receiver refused signed historical result", received, err, sendErr)
	}
}

func TestTransportAbsentIntermediates(t *testing.T) {
	for _, endpoint := range []string{"request", "result"} {
		t.Run(endpoint, func(t *testing.T) {
			a := newAuthorityFixture(t)
			manager, executor := transportPair(t, a)
			out := transportOutcome(t, a)
			message := a.message(nil)
			if endpoint == "result" {
				message = a.message(&out)
			}
			before := testSigningBytes(t, message)
			message.Intermediates = nil
			if !bytes.Equal(before, testSigningBytes(t, message)) {
				t.Fatal("intermediate encoding changed signed fields")
			}
			done := make(chan error, 1)
			go func() {
				if endpoint == "request" {
					done <- manager.SendRequest(context.Background(), a.r, message)
				} else {
					done <- executor.SendResult(context.Background(), a.r, out, message)
				}
			}()
			var err error
			if endpoint == "request" {
				_, err = executor.ReceiveRequest(context.Background())
			} else {
				_, err = manager.ReceiveResult(context.Background(), a.r)
			}
			sendErr := <-done
			if err != nil || sendErr != nil {
				t.Fatal("valid root-issued certificate failed round trip", err, sendErr)
			}
			if message.Intermediates != nil || !bytes.Equal(before, testSigningBytes(t, message)) {
				t.Fatal("sending mutated the caller's authority or signed fields")
			}
		})
	}
}

func TestTransportFinalRequestAuthorization(t *testing.T) {
	for _, limit := range []string{"hold", "observation", "hold-boundary", "observation-boundary", "hold-timer", "observation-timer"} {
		t.Run(limit, func(t *testing.T) {
			a := newAuthorityFixture(t)
			expires := a.f.now.Add(100 * time.Millisecond)
			if limit == "hold" || limit == "hold-boundary" || limit == "hold-timer" {
				a.r.Core.Status.ExpiresAt = expires
				a.r.Core.Delivery.Command.ExpiresAt = expires
			} else {
				a.r.Core.Status.ObservedAt = expires.Add(-time.Minute)
				a.r.Core.Delivery.Command.IssuedAt = a.r.Core.Status.ObservedAt
			}
			a.authorize()
			client, server := net.Pipe()
			recording := &deadlineConn{Conn: client}
			manager, executor := transportPairConnections(t, a, recording, server)
			decisions, clocks := 0, 0
			// Only the receiver uses these providers. The third clock read after
			// its second decision is the final frame-signature phase.
			executor.authority.Decisions = decisionFunc(func(context.Context, string) (CommittedDecision, error) {
				decisions++
				return a.decision, nil
			})
			executor.authority.Now = func() time.Time {
				if decisions == 2 {
					clocks++
					if clocks >= 3 {
						if limit == "hold-timer" || limit == "observation-timer" {
							deadline := recording.deadline()
							remaining := time.Until(deadline)
							if remaining <= 0 || remaining > 100*time.Millisecond {
								t.Error("final signature phase lost the shorter request deadline", remaining)
							}
							// Keep the injected authorization clock valid. Only the
							// retained real context timer can refuse this completion.
							<-time.After(max(remaining, 0) + 10*time.Millisecond)
							return a.f.now
						}
						if limit == "hold-boundary" || limit == "observation-boundary" {
							return expires
						}
						return expires.Add(time.Nanosecond)
					}
				}

				return a.f.now
			}
			done := make(chan error, 1)
			go func() { done <- manager.SendRequest(context.Background(), a.r, a.message(nil)) }()
			_, err := executor.ReceiveRequest(context.Background())
			<-done
			if decisions != 2 || clocks < 3 {
				t.Fatal("fixture did not reach final signature phase", decisions, clocks, err)
			}
			if err == nil || a.f.process.creates != 0 || a.f.process.resumes != 0 {
				t.Fatal("expired final-phase request returned as authorized", err)
			}
			expected := ErrDenied
			if limit == "hold-timer" || limit == "observation-timer" {
				expected = context.DeadlineExceeded
			}
			if !errors.Is(err, expected) {
				t.Fatal("expiry refusal lost its explicit cause", err)
			}
		})
	}
}

type deadlineConn struct {
	net.Conn
	mu   sync.Mutex
	last time.Time
}

func (c *deadlineConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.last = deadline
	c.mu.Unlock()
	return c.Conn.SetDeadline(deadline)
}

func (c *deadlineConn) deadline() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

func TestSessionAuthoritySynchronizedBinding(t *testing.T) {
	a := newAuthorityFixture(t)
	manager, old := transportPair(t, a)
	_, fresh := transportPair(t, a)
	authority := &SessionAuthority{}
	if err := authority.Verify(context.Background(), a.r); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unbound session authority was available", err)
	}
	if err := authority.Bind(context.Background(), nil); !errors.Is(err, ErrUnavailable) {
		t.Fatal("nil session accepted", err)
	}
	if err := authority.Bind(context.Background(), manager); !errors.Is(err, ErrDenied) {
		t.Fatal("manager-role session accepted as executor authority", err)
	}
	if err := authority.Bind(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	old.authority.Decisions = decisionFunc(func(ctx context.Context, _ string) (CommittedDecision, error) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return a.decision, nil
		case <-ctx.Done():
			return CommittedDecision{}, ctx.Err()
		}
	})
	done := make(chan error, 1)
	go func() { done <- authority.Verify(context.Background(), a.r) }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	err := authority.Bind(ctx, fresh)
	cancel()
	close(release)
	verifyErr := <-done
	if !errors.Is(err, context.DeadlineExceeded) || verifyErr != nil {
		t.Fatal("binding did not wait for active verification/cancel safely", err, verifyErr)
	}
	old.Close()
	if err := authority.Verify(context.Background(), a.r); err == nil {
		t.Fatal("cancelled binding replaced the old session")
	}
	if err := authority.Bind(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	if err := authority.Verify(context.Background(), a.r); err != nil {
		t.Fatal("fresh binding did not use its real connection", err)
	}
}

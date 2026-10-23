package executor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	lifecyclev1 "github.com/multica-ai/multica/server/pkg/lifecycle/v1"
)

type credentialsFunc func(context.Context) (TLSCredentials, error)

func (f credentialsFunc) TLSCredentials(ctx context.Context) (TLSCredentials, error) { return f(ctx) }

func transportCredentials(t *testing.T, a *authorityFixture, role string) TLSCredentials {
	t.Helper()
	root, err := x509.ParseCertificate(a.root.der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	cert := a.manager
	if role == "executor" {
		cert = a.participant
	}
	return TLSCredentials{Approved: true, Roots: roots, ManagerDNS: a.enrollment.ManagerDNSName,
		Certificate: tls.Certificate{Certificate: [][]byte{cert.der}, PrivateKey: cert.key}}
}

func transportPair(t *testing.T, a *authorityFixture) (*Transport, *Transport) {
	t.Helper()
	client, server := net.Pipe()
	return transportPairConnections(t, a, client, server)
}

func transportPairConnections(t *testing.T, a *authorityFixture, client, server net.Conn) (*Transport, *Transport) {
	t.Helper()
	managerConfig, executorConfig := transportCredentials(t, a, "manager"), transportCredentials(t, a, "executor")
	type opened struct {
		transport *Transport
		err       error
	}
	done := make(chan opened, 1)
	// A deliberately hostile injected TLS struct source must never be consulted.
	authority := a.adapter
	authority.Channels = channelFunc(func(context.Context, string) (tls.ConnectionState, error) {
		t.Error("transport used caller-supplied peer evidence")
		return tls.ConnectionState{}, ErrDenied
	})
	go func() {
		tr, err := OpenTransport(context.Background(), server, "manager",
			credentialsFunc(func(context.Context) (TLSCredentials, error) { return managerConfig, nil }), authority, time.Second)
		done <- opened{tr, err}
	}()
	executor, err := OpenTransport(context.Background(), client, "executor",
		credentialsFunc(func(context.Context) (TLSCredentials, error) { return executorConfig, nil }), authority, time.Second)
	manager := <-done
	if err != nil || manager.err != nil {
		t.Fatal("in-memory TLS open failed", err, manager.err)
	}
	t.Cleanup(func() { executor.Close(); manager.transport.Close() })
	return manager.transport, executor
}

func sendTransportRequest(t *testing.T, a *authorityFixture, manager, executor *Transport) Request {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- manager.SendRequest(context.Background(), a.r, a.message(nil)) }()
	r, err := executor.ReceiveRequest(context.Background())
	sendErr := <-done
	if err != nil || sendErr != nil {
		t.Fatal("request framing/verification failed", err, sendErr)
	}
	same, err := sameRequest(a.r, r)
	if err != nil || !same {
		t.Fatal("transport changed request", err)
	}
	return r
}

func rawFrame(data []byte) []byte {
	wire := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(wire, uint32(len(data)))
	copy(wire[4:], data)
	return wire
}

func transportOutcome(t *testing.T, a *authorityFixture) Outcome {
	t.Helper()
	out := Outcome{RequestID: a.r.ID, RequestDigest: testRequestDigest(t, a.r), Incarnation: a.r.Incarnation,
		State: "closed", Generation: a.r.Generation, Epoch: a.r.Epoch, Coverage: "unknown"}
	if err := out.Validate(a.r); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTransportMalformedFrames(t *testing.T) {
	for _, endpoint := range []string{"request", "result"} {
		for _, failure := range []string{"truncated-header", "truncated-body", "zero", "oversized", "malformed", "unknown-field",
			"duplicate", "missing", "null", "null-intermediates", "trailing-json", "wrong-contract", "bad-signature", "disconnect"} {
			t.Run(endpoint+"-"+failure, func(t *testing.T) {
				a := newAuthorityFixture(t)
				manager, executor := transportPair(t, a)
				sender, receiver := manager, executor
				var value any = RequestFrame{TransportContract, a.r, a.message(nil)}
				out := transportOutcome(t, a)
				if endpoint == "result" {
					sender, receiver = executor, manager
					value = ResultFrame{TransportContract, out, a.message(&out)}
				}
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				wire := rawFrame(data)
				switch failure {
				case "truncated-header":
					wire = wire[:2]
				case "truncated-body":
					wire = wire[:len(wire)-5]
				case "zero":
					wire = make([]byte, 4)
				case "oversized":
					wire = make([]byte, 4)
					binary.BigEndian.PutUint32(wire, lifecyclev1.MaintenanceBodyLimit+1)
				case "malformed":
					wire = rawFrame([]byte(`{`))
				case "unknown-field":
					wire = rawFrame([]byte(strings.Replace(string(data), `"contract":`, `"actor":"owner","contract":`, 1)))
				case "duplicate":
					wire = rawFrame([]byte(strings.Replace(string(data), `"contract":`, `"contract":"duplicate","contract":`, 1)))
				case "missing":
					wire = rawFrame([]byte(`{}`))
				case "null":
					wire = rawFrame([]byte(`null`))
				case "null-intermediates":
					wire = rawFrame([]byte(strings.Replace(string(data), `"intermediates":[]`, `"intermediates":null`, 1)))
				case "trailing-json":
					wire = rawFrame(append(data, []byte(`{}`)...))
				case "wrong-contract":
					wire = rawFrame([]byte(strings.Replace(string(data), TransportContract, "unsupported", 1)))
				case "bad-signature":
					if endpoint == "request" {
						f := value.(RequestFrame)
						f.Authority.Signature[0] ^= 1
						data, err = json.Marshal(f)
					} else {
						f := value.(ResultFrame)
						f.Authority.Signature[0] ^= 1
						data, err = json.Marshal(f)
					}
					if err != nil {
						t.Fatal(err)
					}
					wire = rawFrame(data)
				case "disconnect":
					wire = nil
				}
				done := make(chan error, 1)
				go func() {
					if err := sender.raw.SetDeadline(time.Now().Add(time.Second)); err != nil {
						done <- err
						return
					}
					_, err := sender.conn.Write(wire)
					sender.Close()
					done <- err
				}()
				if endpoint == "request" {
					_, err = receiver.ReceiveRequest(context.Background())
				} else {
					_, err = receiver.ReceiveResult(context.Background(), a.r)
				}
				<-done
				if err == nil || a.f.process.creates != 0 || a.f.process.resumes != 0 {
					t.Fatal("malformed frame accepted or process invoked", err)
				}
			})
		}
	}
}

func TestTransportDeadlinesAndCancellation(t *testing.T) {
	for _, stage := range []string{"handshake", "request-read", "request-write", "result-read", "result-write"} {
		for _, failure := range []string{"timeout", "cancel"} {
			t.Run(stage+"-"+failure, func(t *testing.T) {
				a := newAuthorityFixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if failure == "timeout" {
					ctx, cancel = context.WithTimeout(context.Background(), 25*time.Millisecond)
					defer cancel()
				}
				var err error
				if stage == "handshake" {
					client, server := net.Pipe()
					defer server.Close()
					config := transportCredentials(t, a, "executor")
					blocked := newBlockedConn(client)
					if failure == "cancel" {
						blocked.arm("write")
					}
					done := make(chan error, 1)
					go func() {
						_, openErr := OpenTransport(ctx, blocked, "executor",
							credentialsFunc(func(context.Context) (TLSCredentials, error) { return config, nil }), a.adapter, time.Second)
						done <- openErr
					}()
					if failure == "cancel" {
						waitBlocked(t, blocked)
						cancel()
					}
					err = <-done
				} else {
					client, server := net.Pipe()
					clientConn, serverConn := newBlockedConn(client), newBlockedConn(server)
					manager, executor := transportPairConnections(t, a, clientConn, serverConn)
					blocked := clientConn
					if stage == "request-write" || stage == "result-read" {
						blocked = serverConn
					}
					if failure == "cancel" {
						if strings.HasSuffix(stage, "-read") {
							blocked.arm("read")
						} else {
							blocked.arm("write")
						}
					}
					out := transportOutcome(t, a)
					done := make(chan error, 1)
					go func() {
						var operationErr error
						switch stage {
						case "request-read":
							_, operationErr = executor.ReceiveRequest(ctx)
						case "request-write":
							operationErr = manager.SendRequest(ctx, a.r, a.message(nil))
						case "result-read":
							_, operationErr = manager.ReceiveResult(ctx, a.r)
						case "result-write":
							operationErr = executor.SendResult(ctx, a.r, out, a.message(&out))
						}
						done <- operationErr
					}()
					if failure == "cancel" {
						waitBlocked(t, blocked)
						cancel()
					}
					err = <-done
				}
				if err == nil {
					t.Fatal("unresponsive transport did not stop")
				}
				if failure == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation was not explicit", err)
				}
				if failure == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("deadline was not explicit", err)
				}
			})
		}
	}
}

// Once armed, I/O cannot progress until Close: the signal originates inside
// the blocked connection call, not before entering a transport operation.
type blockedConn struct {
	net.Conn
	mu        sync.Mutex
	mode      string
	entered   chan struct{}
	closed    chan struct{}
	enterOnce sync.Once
	closeOnce sync.Once
}

func newBlockedConn(conn net.Conn) *blockedConn {
	return &blockedConn{Conn: conn, entered: make(chan struct{}), closed: make(chan struct{})}
}

func (c *blockedConn) arm(mode string) {
	c.mu.Lock()
	c.mode = mode
	c.mu.Unlock()
}

func (c *blockedConn) block(mode string) bool {
	c.mu.Lock()
	armed := c.mode == mode
	c.mu.Unlock()
	if armed {
		c.enterOnce.Do(func() { close(c.entered) })
		<-c.closed
	}
	return armed
}

func (c *blockedConn) Read(p []byte) (int, error) {
	if c.block("read") {
		return 0, net.ErrClosed
	}
	return c.Conn.Read(p)
}

func (c *blockedConn) Write(p []byte) (int, error) {
	if c.block("write") {
		return 0, net.ErrClosed
	}
	return c.Conn.Write(p)
}

func (c *blockedConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func waitBlocked(t *testing.T, c *blockedConn) {
	t.Helper()
	select {
	case <-c.entered:
	case <-time.After(time.Second):
		t.Fatal("operation never entered blocked connection I/O")
	}
}

func TestTransportUnavailableAndWrongPeer(t *testing.T) {
	for _, failure := range []string{"missing-provider", "unapproved", "unapproved-enrollment", "missing-roots", "missing-certificate", "wrong-ca", "wrong-dns", "wrong-pin"} {
		t.Run(failure, func(t *testing.T) {
			a := newAuthorityFixture(t)
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			config := transportCredentials(t, a, "executor")
			if failure == "unapproved" {
				config.Approved = false
			}
			if failure == "unapproved-enrollment" {
				a.enrollment.Approved = false
			}
			if failure == "missing-roots" {
				config.Roots = nil
			}
			if failure == "missing-certificate" {
				config.Certificate = tls.Certificate{}
			}
			if failure == "wrong-ca" {
				root := fixtureCertificate(t, a.f.now, "", 0, nil)
				parsed, err := x509.ParseCertificate(root.der)
				if err != nil {
					t.Fatal(err)
				}
				config.Roots = x509.NewCertPool()
				config.Roots.AddCert(parsed)
			}
			if failure == "wrong-dns" {
				config.ManagerDNS = "wrong.fixture.invalid"
			}
			provider := TLSCredentialSource(credentialsFunc(func(context.Context) (TLSCredentials, error) { return config, nil }))
			if failure == "missing-provider" {
				provider = nil
			}
			serverConfig := transportCredentials(t, a, "manager")
			done := make(chan error, 1)
			go func() {
				manager, err := OpenTransport(context.Background(), server, "manager",
					credentialsFunc(func(context.Context) (TLSCredentials, error) { return serverConfig, nil }), a.adapter, time.Second)
				if err == nil {
					defer manager.Close()
					err = manager.SendRequest(context.Background(), a.r, a.message(nil))
				}
				done <- err
			}()
			executor, err := OpenTransport(context.Background(), client, "executor", provider, a.adapter, time.Second)
			if err == nil {
				defer executor.Close()
				if failure == "wrong-pin" {
					enrollment := a.enrollment
					enrollment.ManagerFingerprint = strings.Repeat("0", 64)
					executor.authority.Enrollment = enrollmentFunc(func(context.Context) (Enrollment, error) { return enrollment, nil })
				}
				_, err = executor.ReceiveRequest(context.Background())
			}
			<-done
			if err == nil {
				t.Fatal("unapproved credentials or wrong peer accepted", failure)
			}
		})
	}
}

func TestTransportDelayedResultAndAmbiguousSend(t *testing.T) {
	a := newAuthorityFixture(t)
	manager, executor := transportPair(t, a)
	out := transportOutcome(t, a)
	data, err := json.Marshal(ResultFrame{TransportContract, out, a.message(&out)})
	if err != nil {
		t.Fatal(err)
	}
	a.f.now = a.r.Expires
	done := make(chan error, 1)
	go func() { _, err := executor.conn.Write(rawFrame(data)); done <- err }()
	if _, err := manager.ReceiveResult(context.Background(), a.r); !errors.Is(err, ErrUnknown) {
		t.Fatal("delayed result accepted or lost reconciliation status", err)
	}
	manager.Close()
	<-done
	a.f.now = a.r.Expires.Add(-30 * time.Second)
	manager, executor = transportPair(t, a)
	executor.Close()
	err = manager.SendRequest(context.Background(), a.r, a.message(nil))
	var uncertain *TransportError
	if !errors.As(err, &uncertain) || uncertain.OperationID != a.r.ID || !errors.Is(err, ErrUnknown) {
		t.Fatal("ambiguous send lost stable operation ID", err)
	}
	if a.f.process.creates != 0 || a.f.process.resumes != 0 {
		t.Fatal("transport failure retried a process")
	}
}

func TestTransportResultExpiresDuringRead(t *testing.T) {
	a := newAuthorityFixture(t)
	manager, executor := transportPair(t, a)
	out := transportOutcome(t, a)
	data, err := json.Marshal(ResultFrame{TransportContract, out, a.message(&out)})
	if err != nil {
		t.Fatal(err)
	}
	wire := rawFrame(data)
	done := make(chan error, 1)
	go func() {
		// ReadFull cannot finish until the final byte; only the receiver will
		// inspect the injected clock after the complete body is available.
		if _, err := executor.conn.Write(wire[:len(wire)-1]); err != nil {
			done <- err
			return
		}
		a.f.now = a.r.Expires
		_, err := executor.conn.Write(wire[len(wire)-1:])
		done <- err
	}()
	_, err = manager.ReceiveResult(context.Background(), a.r)
	<-done
	if !errors.Is(err, ErrUnknown) || !errors.Is(err, ErrDenied) {
		t.Fatal("late complete result became live authority", err)
	}
}

func TestTransportExactFrameLimitAndRoleGuards(t *testing.T) {
	a := newAuthorityFixture(t)
	manager, executor := transportPair(t, a)
	data, err := json.Marshal(RequestFrame{TransportContract, a.r, a.message(nil)})
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte(strings.Repeat(" ", lifecyclev1.MaintenanceBodyLimit-len(data)))...)
	done := make(chan error, 1)
	go func() { _, err := manager.conn.Write(rawFrame(data)); done <- err }()
	if _, err := executor.ReceiveRequest(context.Background()); err != nil {
		t.Fatal("exact maximum valid frame refused", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ReceiveRequest(context.Background()); !errors.Is(err, ErrDenied) {
		t.Fatal("manager accepted request receiver role", err)
	}
	if err := executor.SendRequest(context.Background(), a.r, a.message(nil)); !errors.Is(err, ErrDenied) {
		t.Fatal("executor sent manager request", err)
	}
	if _, err := executor.ReceiveResult(context.Background(), a.r); !errors.Is(err, ErrDenied) {
		t.Fatal("executor accepted result receiver role", err)
	}
	if err := manager.SendResult(context.Background(), a.r, transportOutcome(t, a), a.message(nil)); !errors.Is(err, ErrDenied) {
		t.Fatal("manager sent participant result", err)
	}
	if err := manager.Verify(context.Background(), a.r); !errors.Is(err, ErrDenied) {
		t.Fatal("manager acted as executor authority", err)
	}
	oversized := a.message(nil)
	oversized.Signature = make([]byte, lifecyclev1.MaintenanceBodyLimit)
	if err := manager.SendRequest(context.Background(), a.r, oversized); !errors.Is(err, ErrDenied) || !errors.Is(err, ErrUnknown) {
		t.Fatal("oversized outgoing frame not explicitly refused", err)
	}
}

func TestTransportPreservesSignedBindings(t *testing.T) {
	for _, field := range []string{"request-scope", "request-incarnation", "request-audience", "result-historical", "result-request-id", "result-generation"} {
		t.Run(field, func(t *testing.T) {
			a := newAuthorityFixture(t)
			manager, executor := transportPair(t, a)
			message := a.message(nil)
			r := a.r
			out := transportOutcome(t, a)
			resultMessage := a.message(&out)
			switch field {
			case "request-scope":
				r.Binding.Scope = strings.Repeat("0", 64)
			case "request-incarnation":
				r.Incarnation = "22222222-2222-4222-8222-222222222222"
			case "request-audience":
				message.Audience = a.enrollment.ManagerFingerprint
			case "result-historical":
				out.Historical = true
			case "result-request-id":
				out.RequestID = "22222222-2222-4222-8222-222222222222"
			case "result-generation":
				out.Generation++
			}
			var value any = RequestFrame{TransportContract, r, message}
			sender := manager
			if strings.HasPrefix(field, "result-") {
				value, sender = ResultFrame{TransportContract, out, resultMessage}, executor
			}
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := sender.conn.Write(rawFrame(data)); done <- err }()
			if strings.HasPrefix(field, "request-") {
				_, err = executor.ReceiveRequest(context.Background())
			} else {
				_, err = manager.ReceiveResult(context.Background(), a.r)
			}
			<-done
			if err == nil || a.f.process.creates != 0 || a.f.process.resumes != 0 {
				t.Fatal("framing weakened signed binding or invoked process", field, err)
			}
		})
	}
}

var _ Authority = (*Transport)(nil)
var _ Authority = (*SessionAuthority)(nil)
var _ io.Closer = (*Transport)(nil)

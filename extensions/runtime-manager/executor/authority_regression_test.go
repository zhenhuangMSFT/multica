package executor

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"testing"
	"time"
)

func TestAuthorityRejectsUnencodableOutcome(t *testing.T) {
	a := newAuthorityFixture(t)
	a.f.execute(t, a.f.request("open"))
	a.r = a.f.request("launch")
	a.authorize()
	out, err := a.f.e.result(a.r, "unknown", Child{})
	if err != nil {
		t.Fatal(err)
	}
	out.Child.Created = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	// Reproduce the old failed-serialization digest without relying on the
	// helper under test to create a malformed signed fixture.
	emptyHash := sha256.Sum256(nil)
	message := a.message(nil)
	message.Kind = "participant-result"
	message.OutcomeDigest = hex.EncodeToString(emptyHash[:])
	message.Audience = a.enrollment.ManagerFingerprint
	message.Certificate = a.participant.der
	message.Signature = ed25519.Sign(a.participant.key, testSigningBytes(t, message))
	for _, field := range []string{"original", "historical", "child-pid", "child-image"} {
		t.Run(field, func(t *testing.T) {
			changed := out
			switch field {
			case "historical":
				changed.Historical = true
			case "child-pid":
				changed.Child.PID = 99
			case "child-image":
				changed.Child.Image = "altered"
			}
			if err := a.adapter.VerifyResult(context.Background(), a.r, changed, message); err == nil {
				t.Fatal("unencodable outcome accepted under original empty-byte signature:", field)
			}
		})
	}
}

// A real TLS1.3 handshake over net.Pipe provides each receiver's actual peer
// evidence without a listener, network connection, provisioned key or process.
func endpointTLSFixture(t *testing.T, a *authorityFixture) (tls.ConnectionState, tls.ConnectionState) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	if err := clientConn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := serverConn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	root, err := x509.ParseCertificate(a.root.der)
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(root)
	client := tls.Client(clientConn, &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		RootCAs: roots, ServerName: a.enrollment.ManagerDNSName, Time: func() time.Time { return a.f.now },
		Certificates: []tls.Certificate{{Certificate: [][]byte{a.participant.der}, PrivateKey: a.participant.key}}})
	server := tls.Server(serverConn, &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, Time: func() time.Time { return a.f.now },
		Certificates: []tls.Certificate{{Certificate: [][]byte{a.manager.der}, PrivateKey: a.manager.key}}})
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.HandshakeContext(context.Background()) }()
	clientErr := client.HandshakeContext(context.Background())
	serverErr := <-serverDone
	if clientErr != nil || serverErr != nil {
		t.Fatal("in-memory mutual TLS handshake failed", clientErr, serverErr)
	}
	return client.ConnectionState(), server.ConnectionState()
}

func TestAuthorityEndpointFaithfulChannels(t *testing.T) {
	a := newAuthorityFixture(t)
	managerPeer, executorPeer := endpointTLSFixture(t, a)
	requestCalls, resultCalls := 0, 0
	requestReceiver, resultReceiver := a.adapter, a.adapter
	requestReceiver.Channels = channelFunc(func(_ context.Context, role string) (tls.ConnectionState, error) {
		requestCalls++
		if role != "manager" {
			t.Errorf("executor request receiver asked for unavailable opposite-endpoint %q peer", role)
			return tls.ConnectionState{}, ErrUnavailable
		}
		return managerPeer, nil
	})
	resultReceiver.Channels = channelFunc(func(_ context.Context, role string) (tls.ConnectionState, error) {
		resultCalls++
		if role != "executor" {
			t.Errorf("manager result receiver asked for unavailable opposite-endpoint %q peer", role)
			return tls.ConnectionState{}, ErrUnavailable
		}
		return executorPeer, nil
	})
	if err := requestReceiver.Verify(context.Background(), a.r); err != nil {
		t.Fatal("executor rejected genuine manager peer", err)
	}
	out := a.f.execute(t, a.r)
	if err := resultReceiver.VerifyResult(context.Background(), a.r, out, a.message(&out)); err != nil {
		t.Fatal("manager rejected genuine executor peer", err)
	}
	if requestCalls != 1 || resultCalls != 1 {
		t.Fatal("verification required extra endpoint perspectives", requestCalls, resultCalls)
	}
	for _, endpoint := range []string{"request", "result"} {
		for _, refusal := range []string{"opposite-peer", "wrong-message", "revoked-peer", "revoked-decision", "revoked-human", "expired", "cancelled"} {
			t.Run(endpoint+"-"+refusal, func(t *testing.T) {
				now, enrollment, decision, human := a.f.now, a.enrollment, a.decision, a.human
				defer func() { a.f.now, a.enrollment, a.decision, a.human = now, enrollment, decision, human }()
				message := a.message(&out)
				receiver := requestReceiver
				if endpoint == "result" {
					receiver = resultReceiver
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				switch refusal {
				case "opposite-peer":
					receiver.Channels = channelFunc(func(_ context.Context, role string) (tls.ConnectionState, error) {
						if endpoint == "request" && role == "manager" {
							return executorPeer, nil
						}
						if endpoint == "result" && role == "executor" {
							return managerPeer, nil
						}
						t.Errorf("unavailable opposite endpoint requested: %s", role)
						return tls.ConnectionState{}, ErrUnavailable
					})
				case "wrong-message":
					if endpoint == "request" {
						a.decision.Message.Audience = a.enrollment.ManagerFingerprint
						a.decision.Message.Signature = ed25519.Sign(a.manager.key, testSigningBytes(t, a.decision.Message))
					} else {
						message.Audience = a.enrollment.ExecutorFingerprint
						message.Signature = ed25519.Sign(a.participant.key, testSigningBytes(t, message))
					}
				case "revoked-peer":
					pin := a.enrollment.ManagerFingerprint
					if endpoint == "result" {
						pin = a.enrollment.ExecutorFingerprint
					}
					a.enrollment.RevokedCertificates = []string{pin}
				case "revoked-decision":
					a.decision.Revoked = true
				case "revoked-human":
					a.human.Revoked = true
				case "expired":
					a.f.now = a.r.Expires
				case "cancelled":
					cancel()
				}
				var err error
				if endpoint == "request" {
					err = receiver.Verify(ctx, a.r)
				} else {
					err = receiver.VerifyResult(ctx, a.r, out, message)
				}
				if err == nil {
					t.Fatal("endpoint verification lost refusal:", endpoint, refusal)
				}
				if a.f.process.creates != 0 || a.f.process.resumes != 0 {
					t.Fatal("endpoint authentication invoked process")
				}
			})
		}
	}
}

func TestAuthoritySerializationErrorsPropagate(t *testing.T) {
	a := newAuthorityFixture(t)
	unencodable := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	out := a.f.execute(t, a.r)
	malformed := out
	malformed.State, malformed.Child.Created = "unknown", unencodable
	if digest, err := outcomeDigest(malformed); !errors.Is(err, ErrDenied) || digest != "" {
		t.Fatal("failed outcome serialization returned a usable digest", digest, err)
	}
	r := a.r
	proof := *r.Core
	proof.Status.ObservedAt = unencodable
	r.Core = &proof
	if digest, err := requestDigest(r); !errors.Is(err, ErrDenied) || digest != "" {
		t.Fatal("failed request serialization returned a usable digest", digest, err)
	}
	if same, err := sameRequest(r, r); !errors.Is(err, ErrDenied) || same {
		t.Fatal("unencodable request compared equal", same, err)
	}
	if same, err := sameRequest(a.r, r); !errors.Is(err, ErrDenied) || same {
		t.Fatal("unencodable second request compared equal", same, err)
	}
	if err := out.Validate(r); !errors.Is(err, ErrDenied) {
		t.Fatal("outcome validation suppressed request encoding failure", err)
	}
	message := a.message(&out)
	for _, field := range []string{"issued", "expires"} {
		t.Run(field, func(t *testing.T) {
			changed := message
			if field == "issued" {
				changed.Issued = unencodable
			} else {
				changed.Expires = unencodable
			}
			if data, err := changed.SigningBytes(); !errors.Is(err, ErrDenied) || data != nil {
				t.Fatal("failed signing serialization returned usable bytes", data, err)
			}
			if err := verifyAuthorityMessage(a.enrollment, changed, a.r, "participant-result",
				message.OutcomeDigest, a.f.now, true); !errors.Is(err, ErrDenied) {
				t.Fatal("message verification suppressed serialization failure", err)
			}
		})
	}
}

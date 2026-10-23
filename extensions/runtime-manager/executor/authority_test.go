package executor

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type enrollmentFunc func(context.Context) (Enrollment, error)

func (f enrollmentFunc) Enrollment(ctx context.Context) (Enrollment, error) { return f(ctx) }

type channelFunc func(context.Context, string) (tls.ConnectionState, error)

func (f channelFunc) Channel(ctx context.Context, role string) (tls.ConnectionState, error) {
	return f(ctx, role)
}

type decisionFunc func(context.Context, string) (CommittedDecision, error)

func (f decisionFunc) Decision(ctx context.Context, id string) (CommittedDecision, error) {
	return f(ctx, id)
}

type humanFunc func(context.Context, string) (HumanApproval, error)

func (f humanFunc) HumanApproval(ctx context.Context, id string) (HumanApproval, error) {
	return f(ctx, id)
}

type certificateFixture struct {
	der []byte
	key ed25519.PrivateKey
}

func fixtureCertificate(t *testing.T, now time.Time, identity string, usage x509.ExtKeyUsage, issuer *certificateFixture) certificateFixture {
	t.Helper()
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "isolated-fixture"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature}
	parent, signer := template, key
	if issuer == nil {
		template.IsCA = true
		template.KeyUsage |= x509.KeyUsageCertSign
	} else {
		parent, err = x509.ParseCertificate(issuer.der)
		if err != nil {
			t.Fatal(err)
		}
		signer = issuer.key
		u, err := url.Parse(identity)
		if err != nil {
			t.Fatal(err)
		}
		template.URIs, template.ExtKeyUsage = []*url.URL{u}, []x509.ExtKeyUsage{usage}
		if usage == x509.ExtKeyUsageServerAuth {
			template.DNSNames = []string{"manager.fixture.invalid"}
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, public, signer)
	if err != nil {
		t.Fatal(err)
	}
	return certificateFixture{der, key}
}

type authorityFixture struct {
	f                          *fixture
	r                          Request
	root, manager, participant certificateFixture
	enrollment                 Enrollment
	decision                   CommittedDecision
	human                      HumanApproval
	adapter                    CertificateAuthority
}

func newAuthorityFixture(t *testing.T) *authorityFixture {
	t.Helper()
	f := newFixture(t)
	a := &authorityFixture{f: f, r: f.request("close")}
	a.root = fixtureCertificate(t, f.now, "", 0, nil)
	managerURI, executorURI := "urn:multica:fixture:manager:"+uuid.NewString(), "urn:multica:fixture:executor:"+f.binding.Installation
	a.manager = fixtureCertificate(t, f.now, managerURI, x509.ExtKeyUsageServerAuth, &a.root)
	a.participant = fixtureCertificate(t, f.now, executorURI, x509.ExtKeyUsageClientAuth, &a.root)
	a.enrollment = Enrollment{Approved: true, Active: true, Binding: f.binding, Incarnation: f.instance,
		ManagerURI: managerURI, ManagerDNSName: "manager.fixture.invalid", ExecutorURI: executorURI,
		ManagerFingerprint: certificateFingerprint(a.manager.der), ExecutorFingerprint: certificateFingerprint(a.participant.der),
		Roots: [][]byte{a.root.der}, ValidUntil: f.now.Add(time.Minute)}
	a.adapter = CertificateAuthority{
		Enrollment: enrollmentFunc(func(context.Context) (Enrollment, error) { return a.enrollment, nil }),
		Decisions: decisionFunc(func(_ context.Context, id string) (CommittedDecision, error) {
			if id != a.decision.Message.RequestID {
				return CommittedDecision{}, ErrDenied
			}
			return a.decision, nil
		}),
		Humans: humanFunc(func(_ context.Context, id string) (HumanApproval, error) {
			if id != a.human.RequestID {
				return HumanApproval{}, ErrDenied
			}
			return a.human, nil
		}), Now: func() time.Time { return f.now },
		Channels: channelFunc(func(_ context.Context, role string) (tls.ConnectionState, error) {
			peer := a.manager
			if role == "executor" {
				peer = a.participant
			}
			leaf, err := x509.ParseCertificate(peer.der)
			if err != nil {
				return tls.ConnectionState{}, err
			}
			root, err := x509.ParseCertificate(a.root.der)
			if err != nil {
				return tls.ConnectionState{}, err
			}
			return tls.ConnectionState{Version: tls.VersionTLS13, HandshakeComplete: true,
				ServerName: a.enrollment.ManagerDNSName, PeerCertificates: []*x509.Certificate{leaf},
				VerifiedChains: [][]*x509.Certificate{{leaf, root}}}, nil
		}),
	}
	a.authorize()
	return a
}

func (a *authorityFixture) message(result *Outcome) SignedAuthority {
	cert, kind, audience := a.manager, decisionKind(a.r), a.enrollment.ExecutorFingerprint
	digest := ""
	if result != nil {
		cert, kind, audience = a.participant, "participant-result", a.enrollment.ManagerFingerprint
		digest = outcomeDigest(*result)
	}
	m := SignedAuthority{Contract: AuthorityContract, Kind: kind, RequestID: a.r.ID, RequestDigest: requestDigest(a.r),
		OutcomeDigest: digest, Audience: audience, Issued: a.f.now, Expires: a.r.Expires,
		Certificate: cert.der, Intermediates: [][]byte{}}
	m.Signature = ed25519.Sign(cert.key, m.SigningBytes())
	return m
}

func (a *authorityFixture) authorize() {
	a.decision = CommittedDecision{Committed: true, Message: a.message(nil)}
	a.human = HumanApproval{AuthenticatedHuman: true, OwnerID: a.r.Binding.OwnerID,
		RequestID: a.r.ID, RequestDigest: requestDigest(a.r), Expires: a.r.Expires}
}

func TestAuthorityExactRequestAndParticipantResult(t *testing.T) {
	a := newAuthorityFixture(t)
	a.f.e.authority = a.adapter
	out := a.f.execute(t, a.r)
	if err := a.adapter.VerifyResult(context.Background(), a.r, out, a.message(&out)); err != nil {
		t.Fatal(err)
	}
	replay := a.f.execute(t, a.r)
	if !replay.Historical || a.f.process.creates != 0 {
		t.Fatal("replay reran process or lost historical marker")
	}
	if err := a.adapter.VerifyResult(context.Background(), a.r, replay, a.message(&replay)); err != nil {
		t.Fatal(err)
	}
	changed := out
	changed.Historical = true
	if err := a.adapter.VerifyResult(context.Background(), a.r, changed, a.message(&out)); !errors.Is(err, ErrDenied) {
		t.Fatal("result signature did not bind historical flag", err)
	}
}

func TestAuthorityTrustAndCertificateFailures(t *testing.T) {
	for _, field := range []string{"unapproved", "unenrolled", "missing-roots", "missing-source", "missing-decisions", "missing-channel", "missing-clock", "expired-enrollment", "short-enrollment",
		"wrong-issuer", "wrong-manager", "wrong-executor", "wrong-scope", "wrong-incarnation",
		"manager-revoked", "executor-revoked", "root-revoked", "expired-cert", "future-cert", "short-cert",
		"wrong-uri", "wrong-usage", "task-cert", "plugin-cert", "spoofed-common-name", "bad-signature", "revocation-unknown"} {
		t.Run(field, func(t *testing.T) {
			a := newAuthorityFixture(t)
			want := ErrDenied
			switch field {
			case "unapproved":
				a.enrollment.Approved, want = false, ErrUnavailable
			case "unenrolled":
				a.enrollment.Active, want = false, ErrUnavailable
			case "missing-roots":
				a.enrollment.Roots, want = nil, ErrUnavailable
			case "missing-source":
				a.adapter.Enrollment, want = nil, ErrUnavailable
			case "missing-decisions":
				a.adapter.Decisions, want = nil, ErrUnavailable
			case "missing-channel":
				a.adapter.Channels, want = nil, ErrUnavailable
			case "missing-clock":
				a.adapter.Now, want = nil, ErrUnavailable
			case "expired-enrollment":
				a.enrollment.ValidUntil = a.f.now.Add(-time.Second)
			case "short-enrollment":
				a.enrollment.ValidUntil = a.f.now.Add(time.Second)
			case "wrong-issuer":
				other := fixtureCertificate(t, a.f.now, "", 0, nil)
				a.enrollment.Roots = [][]byte{other.der}
			case "wrong-manager":
				a.enrollment.ManagerFingerprint = strings.Repeat("0", 64)
			case "wrong-executor":
				a.enrollment.ExecutorFingerprint = strings.Repeat("0", 64)
			case "wrong-scope":
				a.enrollment.Binding.Scope = strings.Repeat("0", 64)
			case "wrong-incarnation":
				a.enrollment.Incarnation = uuid.NewString()
			case "manager-revoked":
				a.enrollment.RevokedCertificates = []string{a.enrollment.ManagerFingerprint}
			case "executor-revoked":
				a.enrollment.RevokedCertificates = []string{a.enrollment.ExecutorFingerprint}
			case "root-revoked":
				a.enrollment.RevokedCertificates = []string{certificateFingerprint(a.root.der)}
			case "expired-cert", "future-cert", "short-cert":
				certTime := a.f.now.Add(-2 * time.Hour)
				if field == "future-cert" {
					certTime = a.f.now.Add(2 * time.Hour)
				} else if field == "short-cert" {
					certTime = a.f.now.Add(-time.Hour + 5*time.Second)
				}
				a.manager = fixtureCertificate(t, certTime, a.enrollment.ManagerURI, x509.ExtKeyUsageServerAuth, &a.root)
				a.enrollment.ManagerFingerprint = certificateFingerprint(a.manager.der)
				a.authorize()
			case "wrong-uri", "task-cert", "plugin-cert", "spoofed-common-name", "wrong-usage":
				identity, usage := a.enrollment.ManagerURI, x509.ExtKeyUsageServerAuth
				switch field {
				case "wrong-uri":
					identity = "urn:other:manager"
				case "task-cert":
					identity = "urn:multica:task:" + uuid.NewString()
				case "plugin-cert":
					identity = "urn:multica:plugin:" + uuid.NewString()
				case "spoofed-common-name":
					identity = "urn:isolated-fixture"
				case "wrong-usage":
					usage = x509.ExtKeyUsageClientAuth
				}
				a.manager = fixtureCertificate(t, a.f.now, identity, usage, &a.root)
				a.enrollment.ManagerFingerprint = certificateFingerprint(a.manager.der)
				a.authorize()
			case "bad-signature":
				a.decision.Message.Signature[0] ^= 1
			case "revocation-unknown":
				a.adapter.Enrollment = enrollmentFunc(func(context.Context) (Enrollment, error) {
					return Enrollment{}, errors.New("revocation snapshot unavailable")
				})
				want = ErrUnavailable
			}
			if err := a.adapter.Verify(context.Background(), a.r); !errors.Is(err, want) {
				t.Fatal("invalid trust accepted:", field, err)
			}
			if a.f.process.creates != 0 || a.f.process.resumes != 0 {
				t.Fatal("authority test invoked process")
			}
		})
	}
}

func TestAuthorityDecisionHumanAndReplayBindings(t *testing.T) {
	for _, field := range []string{"not-committed", "decision-revoked", "wrong-id", "changed-request", "wrong-kind", "wrong-audience",
		"expired-message", "future-message", "stale-message", "long-message", "result-as-grant",
		"missing-human", "participant-not-human", "other-owner", "human-revoked", "human-expired", "human-short", "human-digest"} {
		t.Run(field, func(t *testing.T) {
			a := newAuthorityFixture(t)
			want := ErrDenied
			switch field {
			case "not-committed":
				a.decision.Committed = false
			case "decision-revoked":
				a.decision.Revoked = true
			case "wrong-id":
				a.decision.Message.RequestID = uuid.NewString()
			case "changed-request":
				a.r.Epoch++
			case "wrong-kind":
				a.decision.Message.Kind = "ordinary-policy"
			case "wrong-audience":
				a.decision.Message.Audience = a.enrollment.ManagerFingerprint
			case "expired-message":
				a.decision.Message.Expires = a.f.now.Add(-time.Second)
			case "future-message":
				a.decision.Message.Issued = a.f.now.Add(time.Second)
			case "stale-message":
				a.decision.Message.Issued = a.f.now.Add(-time.Minute)
			case "long-message":
				a.decision.Message.Expires = a.r.Expires.Add(time.Second)
			case "result-as-grant":
				a.decision.Message = a.message(&Outcome{})
			case "missing-human":
				a.adapter.Humans, want = nil, ErrUnavailable
			case "participant-not-human":
				a.human.AuthenticatedHuman = false
			case "other-owner":
				a.human.OwnerID = uuid.NewString()
			case "human-revoked":
				a.human.Revoked = true
			case "human-expired":
				a.human.Expires = a.f.now
			case "human-short":
				a.human.Expires = a.f.now.Add(time.Second)
			case "human-digest":
				a.human.RequestDigest = strings.Repeat("0", 64)
			}
			// Re-sign altered claims so negative checks test policy, not just a
			// broken signature. A signed participant still cannot grant human rights.
			a.decision.Message.Signature = ed25519.Sign(a.manager.key, a.decision.Message.SigningBytes())
			if err := a.adapter.Verify(context.Background(), a.r); !errors.Is(err, want) {
				t.Fatal("invalid decision/human binding accepted:", field, err)
			}
		})
	}
	a := newAuthorityFixture(t)
	if err := a.adapter.Verify(context.Background(), a.r); err != nil {
		t.Fatal(err)
	}
	a.enrollment.RevokedCertificates = []string{a.enrollment.ManagerFingerprint}
	if err := a.adapter.Verify(context.Background(), a.r); !errors.Is(err, ErrDenied) {
		t.Fatal("cached decision survived revocation", err)
	}
}

func TestAuthorityParticipantCannotGrantReplacement(t *testing.T) {
	a := newAuthorityFixture(t)
	a.f.execute(t, a.r)
	a.r = a.f.request("replace")
	a.authorize()
	if err := a.adapter.Verify(context.Background(), a.r); err != nil {
		t.Fatal(err)
	}
	a.human.AuthenticatedHuman = false
	if err := a.adapter.Verify(context.Background(), a.r); !errors.Is(err, ErrDenied) {
		t.Fatal("manager delivery identity substituted for replacement human", err)
	}
	a.human.AuthenticatedHuman = true
	a.decision.Message.Certificate = a.participant.der
	a.decision.Message.Signature = ed25519.Sign(a.participant.key, a.decision.Message.SigningBytes())
	if err := a.adapter.Verify(context.Background(), a.r); !errors.Is(err, ErrDenied) {
		t.Fatal("participant certificate issued replacement grant", err)
	}
}

func TestAuthorityOperationAndExactByteBinding(t *testing.T) {
	for _, operation := range []string{"open", "launch", "close", "replace", "observe"} {
		t.Run(operation, func(t *testing.T) {
			a := newAuthorityFixture(t)
			switch operation {
			case "launch", "observe":
				a.f.execute(t, a.f.request("open"))
				if operation == "observe" {
					a.f.execute(t, a.f.request("launch"))
				}
			case "replace":
				a.f.execute(t, a.r)
			}
			a.r = a.f.request(operation)
			a.authorize()
			if operation == "launch" || operation == "open" || operation == "observe" {
				a.adapter.Humans = nil
			}
			a.f.e.authority = a.adapter
			out := a.f.execute(t, a.r)
			if err := a.adapter.VerifyResult(context.Background(), a.r, out, a.message(&out)); err != nil {
				t.Fatal("authenticated operation failed result binding", err)
			}
		})
	}
	for _, field := range []string{"request-id", "owner", "host", "scope", "session", "incarnation", "generation",
		"epoch", "deadline", "definition", "transaction", "core-result"} {
		t.Run("changed-"+field, func(t *testing.T) {
			a := newAuthorityFixture(t)
			a.f.execute(t, a.r)
			a.r = a.f.request("replace")
			a.authorize()
			switch field {
			case "request-id":
				a.r.ID = uuid.NewString()
			case "owner":
				a.r.Binding.OwnerID = uuid.NewString()
			case "host":
				a.r.Binding.Host = "other"
			case "scope":
				a.r.Binding.Scope = strings.Repeat("0", 64)
			case "session":
				a.r.Binding.Session++
			case "incarnation":
				a.r.Incarnation = uuid.NewString()
			case "generation":
				a.r.Generation++
			case "epoch":
				a.r.Epoch++
			case "deadline":
				a.r.Expires = a.r.Expires.Add(time.Second)
			case "definition":
				a.r.Definition.Config = strings.Repeat("0", 64)
			case "transaction":
				a.r.Transaction = uuid.NewString()
			case "core-result":
				a.r.Core.Status.ActiveTasks++
			}
			a.human.RequestDigest = requestDigest(a.r)
			if err := a.adapter.Verify(context.Background(), a.r); err == nil {
				t.Fatal("changed exact request accepted", field)
			}
		})
	}
}

func TestAuthorityChannelIsolation(t *testing.T) {
	for _, role := range []string{"manager", "executor"} {
		for _, failure := range []string{"missing", "tls12", "no-handshake", "no-verified-chain", "other-peer", "other-origin", "cancelled"} {
			if role == "executor" && failure == "other-origin" {
				continue // Executor identity is its enrolled URI/pin, not server DNS.
			}
			t.Run(role+"-"+failure, func(t *testing.T) {
				a := newAuthorityFixture(t)
				out := a.f.execute(t, a.r)
				original := a.adapter.Channels
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				a.adapter.Channels = channelFunc(func(ctx context.Context, requested string) (tls.ConnectionState, error) {
					state, err := original.Channel(ctx, requested)
					if requested != role {
						return state, err
					}
					switch failure {
					case "missing":
						return tls.ConnectionState{}, ErrUnavailable
					case "tls12":
						state.Version = tls.VersionTLS12
					case "no-handshake":
						state.HandshakeComplete = false
					case "no-verified-chain":
						state.VerifiedChains = nil
					case "other-peer":
						state.PeerCertificates[0], err = x509.ParseCertificate(a.root.der)
					case "other-origin":
						state.ServerName = "other.fixture.invalid"
					case "cancelled":
						cancel()
					}
					return state, err
				})
				var err error
				if role == "manager" {
					err = a.adapter.Verify(ctx, a.r)
				} else {
					err = a.adapter.VerifyResult(ctx, a.r, out, a.message(&out))
				}
				if err == nil {
					t.Fatal("unqualified peer channel accepted", role, failure)
				}
			})
		}
	}
}

func TestAuthorityResultBindings(t *testing.T) {
	for _, field := range []string{"manager-as-executor", "wrong-executor-uri", "revoked-executor", "expired-executor", "wrong-executor-issuer",
		"wrong-executor-usage", "changed-state", "changed-generation",
		"changed-incarnation", "changed-digest", "old-request", "wrong-audience", "bad-signature"} {
		t.Run(field, func(t *testing.T) {
			a := newAuthorityFixture(t)
			out := a.f.execute(t, a.r)
			message := a.message(&out)
			switch field {
			case "manager-as-executor":
				message.Certificate = a.manager.der
				message.Signature = ed25519.Sign(a.manager.key, message.SigningBytes())
			case "wrong-executor-uri":
				a.participant = fixtureCertificate(t, a.f.now, "urn:other:executor", x509.ExtKeyUsageClientAuth, &a.root)
				a.enrollment.ExecutorFingerprint = certificateFingerprint(a.participant.der)
				a.authorize()
				message = a.message(&out)
			case "revoked-executor":
				a.enrollment.RevokedCertificates = []string{a.enrollment.ExecutorFingerprint}
			case "expired-executor", "wrong-executor-issuer", "wrong-executor-usage":
				certTime, usage, issuer := a.f.now, x509.ExtKeyUsageClientAuth, a.root
				switch field {
				case "expired-executor":
					certTime = certTime.Add(-2 * time.Hour)
				case "wrong-executor-issuer":
					issuer = fixtureCertificate(t, a.f.now, "", 0, nil)
				case "wrong-executor-usage":
					usage = x509.ExtKeyUsageServerAuth
				}
				a.participant = fixtureCertificate(t, certTime, a.enrollment.ExecutorURI, usage, &issuer)
				a.enrollment.ExecutorFingerprint = certificateFingerprint(a.participant.der)
				a.authorize()
				message = a.message(&out)
			case "changed-state":
				out.State = "open"
			case "changed-generation":
				out.Generation++
			case "changed-incarnation":
				out.Incarnation = uuid.NewString()
			case "changed-digest":
				out.RequestDigest = strings.Repeat("0", 64)
			case "old-request":
				message.RequestID = uuid.NewString()
				message.Signature = ed25519.Sign(a.participant.key, message.SigningBytes())
			case "wrong-audience":
				message.Audience = a.enrollment.ExecutorFingerprint
				message.Signature = ed25519.Sign(a.participant.key, message.SigningBytes())
			case "bad-signature":
				message.Signature[0] ^= 1
			}
			if err := a.adapter.VerifyResult(context.Background(), a.r, out, message); !errors.Is(err, ErrDenied) {
				t.Fatal("unbound participant delivery accepted:", field, err)
			}
		})
	}
}

func TestAuthorityReplayRevocationAndDeadlines(t *testing.T) {
	for _, change := range []string{"deadline", "incarnation", "decision-revoked", "human-revoked", "enrollment-revoked"} {
		t.Run(change, func(t *testing.T) {
			a := newAuthorityFixture(t)
			a.f.e.authority = a.adapter
			a.f.execute(t, a.r)
			switch change {
			case "deadline":
				a.f.now = a.r.Expires
			case "incarnation":
				a.f.reload(t)
				a.enrollment.Incarnation = a.f.instance
				a.f.e.authority = a.adapter
			case "decision-revoked":
				a.decision.Revoked = true
			case "human-revoked":
				a.human.Revoked = true
			case "enrollment-revoked":
				a.enrollment.Active = false
			}
			if _, err := a.f.e.Execute(context.Background(), a.r); err == nil ||
				a.f.process.creates != 0 || a.f.process.resumes != 0 || !a.f.e.state.Closed {
				t.Fatal("replay used stale authentication or changed process/gate", change, err)
			}
		})
	}
}

func TestAuthorityDelayedCancellationAndStrictMessages(t *testing.T) {
	for _, stage := range []string{"enrollment", "decision", "channel", "human"} {
		for _, failure := range []string{"expiry", "cancel", "timeout"} {
			t.Run(stage+"-"+failure, func(t *testing.T) {
				a := newAuthorityFixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if failure == "timeout" {
					a.r.Expires = a.f.now.Add(15 * time.Millisecond)
					a.authorize()
				}
				hook := func(ctx context.Context) {
					if _, exists := ctx.Deadline(); !exists {
						t.Fatal("source received unbounded context")
					}
					switch failure {
					case "expiry":
						a.f.now = a.f.now.Add(time.Minute)
					case "cancel":
						cancel()
					case "timeout":
						<-ctx.Done()
					}
				}
				switch stage {
				case "enrollment":
					a.adapter.Enrollment = enrollmentFunc(func(ctx context.Context) (Enrollment, error) { hook(ctx); return a.enrollment, nil })
				case "decision":
					a.adapter.Decisions = decisionFunc(func(ctx context.Context, _ string) (CommittedDecision, error) { hook(ctx); return a.decision, nil })
				case "human":
					a.adapter.Humans = humanFunc(func(ctx context.Context, _ string) (HumanApproval, error) { hook(ctx); return a.human, nil })
				case "channel":
					original := a.adapter.Channels
					a.adapter.Channels = channelFunc(func(ctx context.Context, role string) (tls.ConnectionState, error) {
						hook(ctx)
						return original.Channel(ctx, role)
					})
				}
				if err := a.adapter.Verify(ctx, a.r); err == nil {
					t.Fatal("delayed/cancelled source authorized request")
				}
			})
		}
	}
	a := newAuthorityFixture(t)
	data, err := json.Marshal(a.decision.Message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSignedAuthority(bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(string) string{
		func(s string) string { return strings.Replace(s, `"kind":`, `"actor":"owner","kind":`, 1) },
		func(s string) string { return strings.Replace(s, `"kind":`, `"task_token":"fixture","kind":`, 1) },
		func(s string) string { return strings.Replace(s, `"kind":`, `"callback_token":"fixture","kind":`, 1) },
		func(s string) string { return strings.Replace(s, `"kind":`, `"kind":"human-maintenance","kind":`, 1) },
		func(s string) string { return s + `{}` },
		func(s string) string { return strings.Repeat(" ", 16<<10) + s },
	} {
		if _, err := DecodeSignedAuthority(strings.NewReader(edit(string(data)))); err == nil {
			t.Fatal("spoofed metadata or malformed authority message accepted")
		}
	}
	if err := (CertificateAuthority{}).Verify(context.Background(), a.r); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unconfigured authority available", err)
	}
}

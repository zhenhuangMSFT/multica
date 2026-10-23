package executor

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	lifecyclev1 "github.com/multica-ai/multica/server/pkg/lifecycle/v1"
)

const AuthorityContract = Contract + "/authority"

// Enrollment comes from approved protected configuration, never message claims.
// Sources return owned snapshots and must honor context cancellation.
type Enrollment struct {
	Approved            bool
	Active              bool
	Binding             Binding
	Incarnation         string
	ManagerURI          string
	ManagerDNSName      string
	ExecutorURI         string
	ManagerFingerprint  string
	ExecutorFingerprint string
	Roots               [][]byte
	RevokedCertificates []string
	ValidUntil          time.Time
}

type EnrollmentSource interface {
	Enrollment(context.Context) (Enrollment, error)
}

// ChannelSource supplies a real completed TLS connection state from the future
// transport, not request metadata. "manager" is outbound; "executor" is result
// delivery. This source-only adapter installs no transport or TLS listener.
type ChannelSource interface {
	Channel(context.Context, string) (tls.ConnectionState, error)
}

// DecisionSource reads an authoritative, committed, nonrevoked manager decision.
// Returning a caller-supplied signed message without checking that record is not
// an implementation of this interface's trust contract.
type DecisionSource interface {
	Decision(context.Context, string) (CommittedDecision, error)
}

type CommittedDecision struct {
	Committed bool
	Revoked   bool
	Message   SignedAuthority
}

// HumanApprovalSource is separate from participant certificates/delivery.
// Its implementation must authenticate the exact human owner through the
// approved owner-authentication boundary, not infer a human from actor metadata.
type HumanApprovalSource interface {
	HumanApproval(context.Context, string) (HumanApproval, error)
}

type HumanApproval struct {
	AuthenticatedHuman bool
	OwnerID            string
	RequestID          string
	RequestDigest      string
	Revoked            bool
	Expires            time.Time
}

// SignedAuthority signs a domain-separated digest of the existing v1 contract.
// No bearer token, actor identity or process definition is accepted here.
type SignedAuthority struct {
	Contract      string    `json:"contract"`
	Kind          string    `json:"kind"`
	RequestID     string    `json:"request_id"`
	RequestDigest string    `json:"request_digest"`
	OutcomeDigest string    `json:"outcome_digest"`
	Audience      string    `json:"audience"`
	Issued        time.Time `json:"issued"`
	Expires       time.Time `json:"expires"`
	Certificate   []byte    `json:"certificate"`
	Intermediates [][]byte  `json:"intermediates"`
	Signature     []byte    `json:"signature"`
}

func DecodeSignedAuthority(reader io.Reader) (SignedAuthority, error) {
	var message SignedAuthority
	err := lifecyclev1.DecodeMaintenanceJSON(reader, &message)
	return message, err
}

// SigningBytes is also the interoperable signing input for injected fixtures.
// Certificate/signature transport encodings do not alter the signed statement.
func (m SignedAuthority) SigningBytes() []byte {
	data, _ := json.Marshal(struct {
		Contract      string    `json:"contract"`
		Kind          string    `json:"kind"`
		RequestID     string    `json:"request_id"`
		RequestDigest string    `json:"request_digest"`
		OutcomeDigest string    `json:"outcome_digest"`
		Audience      string    `json:"audience"`
		Issued        time.Time `json:"issued"`
		Expires       time.Time `json:"expires"`
	}{m.Contract, m.Kind, m.RequestID, m.RequestDigest, m.OutcomeDigest, m.Audience, m.Issued, m.Expires})
	return data
}

func certificateFingerprint(der []byte) string {
	hash := sha256.Sum256(der)
	return hex.EncodeToString(hash[:])
}

func outcomeDigest(out Outcome) string {
	data, _ := json.Marshal(out)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// CertificateAuthority has no network, credential loader, key or default roots.
// A zero/unconfigured instance remains unavailable.
type CertificateAuthority struct {
	Enrollment EnrollmentSource
	Decisions  DecisionSource
	Humans     HumanApprovalSource
	Channels   ChannelSource
	Now        func() time.Time
}

func decisionKind(r Request) string {
	switch {
	case r.Operation == "observe":
		return "participant-observation"
	case r.Operation == "launch" || (r.Operation == "open" && r.Generation == 0 && r.Core == nil):
		return "ordinary-policy"
	default:
		return "human-maintenance"
	}
}

func (a CertificateAuthority) Verify(ctx context.Context, r Request) error {
	_, err := a.verify(ctx, r)
	return err
}

func (a CertificateAuthority) verify(ctx context.Context, r Request) (Enrollment, error) {
	if a.Enrollment == nil || a.Decisions == nil || a.Channels == nil || a.Now == nil {
		return Enrollment{}, ErrUnavailable
	}
	bounded, cancel, err := r.authorizedContext(ctx, a.Now())
	if err != nil {
		return Enrollment{}, err
	}
	defer cancel()
	enrollment, err := a.Enrollment.Enrollment(bounded)
	if err != nil {
		return Enrollment{}, errors.Join(ErrUnavailable, err)
	}
	if !enrollment.Approved || !enrollment.Active || len(enrollment.Roots) == 0 {
		return Enrollment{}, ErrUnavailable
	}
	if err := bounded.Err(); err != nil {
		return Enrollment{}, err
	}
	decision, err := a.Decisions.Decision(bounded, r.ID)
	if err != nil {
		return Enrollment{}, errors.Join(ErrUnavailable, err)
	}
	if err := bounded.Err(); err != nil {
		return Enrollment{}, err
	}
	channel, err := a.Channels.Channel(bounded, "manager")
	if err != nil {
		return Enrollment{}, errors.Join(ErrUnavailable, err)
	}
	var human HumanApproval
	if decisionKind(r) == "human-maintenance" {
		if a.Humans == nil {
			return Enrollment{}, ErrUnavailable
		}
		if err := bounded.Err(); err != nil {
			return Enrollment{}, err
		}
		human, err = a.Humans.HumanApproval(bounded, r.ID)
		if err != nil {
			return Enrollment{}, errors.Join(ErrUnavailable, err)
		}
	}
	now := a.Now()
	if err := bounded.Err(); err != nil {
		return Enrollment{}, err
	}
	if r.validate(now) != nil || enrollment.Binding != r.Binding || enrollment.Incarnation != r.Incarnation ||
		!enrollment.ValidUntil.After(now) || enrollment.ValidUntil.Before(r.Expires) ||
		enrollment.ValidUntil.Location() != time.UTC || enrollment.ManagerURI == "" || enrollment.ManagerDNSName == "" || enrollment.ExecutorURI == "" ||
		enrollment.ManagerURI == enrollment.ExecutorURI ||
		!digestPattern.MatchString(enrollment.ManagerFingerprint) || !digestPattern.MatchString(enrollment.ExecutorFingerprint) ||
		enrollment.ManagerFingerprint == enrollment.ExecutorFingerprint || !decision.Committed || decision.Revoked {
		return Enrollment{}, ErrDenied
	}
	if len(enrollment.RevokedCertificates) > 1024 {
		return Enrollment{}, ErrDenied
	}
	for _, revoked := range enrollment.RevokedCertificates {
		if !digestPattern.MatchString(revoked) || revoked == enrollment.ManagerFingerprint || revoked == enrollment.ExecutorFingerprint {
			return Enrollment{}, ErrDenied
		}
	}
	if decisionKind(r) == "human-maintenance" && (!human.AuthenticatedHuman || human.Revoked ||
		human.OwnerID != r.Binding.OwnerID || human.RequestID != r.ID || human.RequestDigest != requestDigest(r) ||
		human.Expires.Location() != time.UTC || !human.Expires.After(now) || human.Expires.Before(r.Expires)) {
		return Enrollment{}, ErrDenied
	}
	if err := verifyAuthorityMessage(enrollment, decision.Message, r, decisionKind(r), "", now, false); err != nil {
		return Enrollment{}, err
	}
	if !enrolledChannel(channel, decision.Message.Certificate) || channel.ServerName != enrollment.ManagerDNSName {
		return Enrollment{}, ErrDenied
	}
	if err := bounded.Err(); err != nil {
		return Enrollment{}, err
	}
	if r.validate(a.Now()) != nil {
		return Enrollment{}, ErrDenied
	}
	return enrollment, nil
}

// VerifyResult authenticates participant delivery, not maintenance permission.
// It rechecks the committed request decision (and human approval when needed).
// A historical result stays historical; successful verification is no readiness
// or OS-execution attestation.
func (a CertificateAuthority) VerifyResult(ctx context.Context, r Request, out Outcome, message SignedAuthority) error {
	enrollment, err := a.verify(ctx, r)
	if err != nil {
		return err
	}
	if out.Validate(r) != nil {
		return ErrDenied
	}
	bounded, cancel, err := r.authorizedContext(ctx, a.Now())
	if err != nil {
		return err
	}
	defer cancel()
	channel, err := a.Channels.Channel(bounded, "executor")
	if err != nil {
		return errors.Join(ErrUnavailable, err)
	}
	if err := bounded.Err(); err != nil {
		return err
	}
	if !enrolledChannel(channel, message.Certificate) {
		return ErrDenied
	}
	if err := verifyAuthorityMessage(enrollment, message, r, "participant-result", outcomeDigest(out), a.Now(), true); err != nil {
		return err
	}
	if err := bounded.Err(); err != nil {
		return err
	}
	return r.validate(a.Now())
}

func enrolledChannel(channel tls.ConnectionState, certificate []byte) bool {
	return channel.HandshakeComplete && channel.Version == tls.VersionTLS13 &&
		len(channel.PeerCertificates) > 0 && channel.PeerCertificates[0] != nil &&
		len(channel.VerifiedChains) > 0 && len(channel.VerifiedChains[0]) > 0 && channel.VerifiedChains[0][0] != nil &&
		certificateFingerprint(channel.PeerCertificates[0].Raw) == certificateFingerprint(certificate) &&
		certificateFingerprint(channel.VerifiedChains[0][0].Raw) == certificateFingerprint(certificate)
}

func verifyAuthorityMessage(e Enrollment, m SignedAuthority, r Request, kind, resultDigest string, now time.Time, result bool) error {
	wire, err := json.Marshal(m)
	if err != nil || len(wire) > lifecyclev1.MaintenanceBodyLimit {
		return ErrDenied
	}
	fingerprint, identity, audience, usage := e.ManagerFingerprint, e.ManagerURI, e.ExecutorFingerprint, x509.ExtKeyUsageServerAuth
	if result {
		fingerprint, identity, audience, usage = e.ExecutorFingerprint, e.ExecutorURI, e.ManagerFingerprint, x509.ExtKeyUsageClientAuth
	}
	if m.Contract != AuthorityContract || m.Kind != kind || m.RequestID != r.ID || m.RequestDigest != requestDigest(r) ||
		m.OutcomeDigest != resultDigest || m.Audience != audience || m.Issued.Location() != time.UTC ||
		m.Issued.IsZero() || m.Issued.After(now) || now.Sub(m.Issued) >= time.Minute ||
		!m.Issued.Before(m.Expires) || !m.Expires.Equal(r.Expires) || m.Expires.Location() != time.UTC ||
		m.Expires.After(m.Issued.Add(time.Minute)) ||
		!m.Expires.After(now) || len(m.Signature) != ed25519.SignatureSize ||
		certificateFingerprint(m.Certificate) != fingerprint {
		return ErrDenied
	}
	cert, err := verifyEnrolledCertificate(e, m, identity, usage, now, r.Expires)
	if err != nil {
		return err
	}
	public, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok || !ed25519.Verify(public, m.SigningBytes(), m.Signature) {
		return ErrDenied
	}
	return nil
}

func verifyEnrolledCertificate(e Enrollment, m SignedAuthority, identity string, usage x509.ExtKeyUsage, now, expires time.Time) (*x509.Certificate, error) {
	if len(m.Certificate) == 0 || len(m.Certificate) > 8<<10 || len(m.Intermediates) > 4 || len(e.Roots) > 8 {
		return nil, ErrDenied
	}
	cert, err := x509.ParseCertificate(m.Certificate)
	if err != nil || cert.IsCA || cert.KeyUsage&x509.KeyUsageDigitalSignature == 0 || len(cert.URIs) != 1 ||
		cert.URIs[0].String() != identity || cert.NotAfter.Before(expires) ||
		len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != usage {
		return nil, ErrDenied
	}
	roots, intermediates := x509.NewCertPool(), x509.NewCertPool()
	for i, ders := range [][][]byte{e.Roots, m.Intermediates} {
		for _, der := range ders {
			if len(der) == 0 || len(der) > 8<<10 {
				return nil, ErrDenied
			}
			ca, err := x509.ParseCertificate(der)
			if err != nil || !ca.IsCA {
				return nil, ErrDenied
			}
			if i == 0 {
				roots.AddCert(ca)
			} else {
				intermediates.AddCert(ca)
			}
		}
	}
	dnsName := ""
	if usage == x509.ExtKeyUsageServerAuth {
		dnsName = e.ManagerDNSName
	}
	chains, err := cert.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, CurrentTime: now,
		DNSName: dnsName, KeyUsages: []x509.ExtKeyUsage{usage}})
	if err != nil {
		return nil, errors.Join(ErrDenied, err)
	}
	// Refuse any revoked or too-short-lived member of the selected chains.
	// No AIA, CRL, OCSP or network fallback is performed.
	for _, chain := range chains {
		for _, member := range chain {
			if member.NotAfter.Before(expires) {
				return nil, ErrDenied
			}
			for _, revoked := range e.RevokedCertificates {
				if revoked == certificateFingerprint(member.Raw) {
					return nil, ErrDenied
				}
			}
		}
	}
	return cert, nil
}

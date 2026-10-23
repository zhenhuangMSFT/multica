package executor

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	lifecyclev1 "github.com/multica-ai/multica/server/pkg/lifecycle/v1"
)

const TransportContract = Contract + "/transport"

// TLSCredentials is injected protected configuration, not a wire message.
// No verification callbacks, system roots or insecure TLS options are accepted.
type TLSCredentials struct {
	Approved    bool
	Certificate tls.Certificate
	Roots       *x509.CertPool
	ManagerDNS  string
}

type TLSCredentialSource interface {
	TLSCredentials(context.Context) (TLSCredentials, error)
}

type RequestFrame struct {
	Contract  string          `json:"contract"`
	Request   Request         `json:"request"`
	Authority SignedAuthority `json:"authority"`
}

type ResultFrame struct {
	Contract  string          `json:"contract"`
	Outcome   Outcome         `json:"outcome"`
	Authority SignedAuthority `json:"authority"`
}

// TransportError preserves the operation to reconcile. It is never a retry
// instruction; a failed send/result read does not prove the child did not run.
type TransportError struct {
	OperationID string
	Cause       error
}

func (e *TransportError) Error() string {
	return "transport operation " + e.OperationID + ": " + e.Cause.Error()
}
func (e *TransportError) Unwrap() error { return e.Cause }

type Transport struct {
	raw       net.Conn
	conn      *tls.Conn
	role      string
	authority CertificateAuthority
	timeout   time.Duration
	gate      chan struct{}
	closeOnce sync.Once
}

// OpenTransport takes ownership of raw, including on failure. It never dials,
// listens, provisions credentials or starts an executor. Role executor uses the
// outbound TLS client; role manager uses the mutually authenticated TLS server.
func OpenTransport(ctx context.Context, raw net.Conn, role string, credentials TLSCredentialSource, authority CertificateAuthority, timeout time.Duration) (*Transport, error) {
	if raw == nil {
		return nil, ErrUnavailable
	}
	fail := func(err error) (*Transport, error) { return nil, errors.Join(err, raw.Close()) }
	if credentials == nil || authority.Enrollment == nil || authority.Decisions == nil || authority.Now == nil ||
		(role != "executor" && role != "manager") || timeout <= 0 || timeout > 30*time.Second {
		return fail(ErrUnavailable)
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	config, err := credentials.TLSCredentials(bounded)
	if err != nil {
		return fail(errors.Join(ErrUnavailable, err))
	}
	if !config.Approved || config.Roots == nil || len(config.Roots.Subjects()) == 0 ||
		len(config.Certificate.Certificate) == 0 || config.Certificate.PrivateKey == nil || config.ManagerDNS == "" {
		return fail(ErrUnavailable)
	}
	if err := bounded.Err(); err != nil {
		return fail(err)
	}
	enrollment, err := authority.Enrollment.Enrollment(bounded)
	if err != nil {
		return fail(errors.Join(ErrUnavailable, err))
	}
	if !enrollment.Approved || !enrollment.Active || len(enrollment.Roots) == 0 ||
		!enrollment.ValidUntil.After(authority.Now()) {
		return fail(ErrUnavailable)
	}
	if err := bounded.Err(); err != nil {
		return fail(err)
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		Certificates: []tls.Certificate{config.Certificate}, Time: authority.Now}
	var conn *tls.Conn
	if role == "executor" {
		tlsConfig.RootCAs, tlsConfig.ServerName = config.Roots.Clone(), config.ManagerDNS
		conn = tls.Client(raw, tlsConfig)
	} else {
		tlsConfig.ClientCAs, tlsConfig.ClientAuth = config.Roots.Clone(), tls.RequireAndVerifyClientCert
		conn = tls.Server(raw, tlsConfig)
	}
	t := &Transport{raw: raw, conn: conn, role: role, authority: authority, timeout: timeout, gate: make(chan struct{}, 1)}
	t.authority.Channels = connectionChannel{transport: t}
	err = t.withDeadline(bounded, func() error { return conn.HandshakeContext(bounded) })
	if err != nil {
		return fail(err)
	}
	return t, nil
}

// The transport replaces any injected ChannelSource with its own unexported
// connection-bound source. Callers cannot supply TLS state in this path.
type connectionChannel struct{ transport *Transport }

func (c connectionChannel) Channel(ctx context.Context, peer string) (tls.ConnectionState, error) {
	if err := ctx.Err(); err != nil {
		return tls.ConnectionState{}, err
	}
	if (c.transport.role == "executor" && peer != "manager") ||
		(c.transport.role == "manager" && peer != "executor") {
		return tls.ConnectionState{}, ErrDenied
	}
	state := c.transport.conn.ConnectionState()
	if !state.HandshakeComplete || state.Version != tls.VersionTLS13 || len(state.VerifiedChains) == 0 {
		return tls.ConnectionState{}, ErrDenied
	}
	return state, nil
}

func (t *Transport) Close() error {
	var err error
	t.closeOnce.Do(func() { err = t.raw.Close() })
	return err
}

func (t *Transport) withDeadline(ctx context.Context, work func() error) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := t.raw.SetDeadline(deadline); err != nil {
		return err
	}
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		t.Close()
		close(stopped)
	})
	err := work()
	if !stop() {
		<-stopped
	}
	if contextErr := ctx.Err(); contextErr != nil {
		err = errors.Join(err, contextErr)
	} else if !time.Now().Before(deadline) {
		err = errors.Join(err, context.DeadlineExceeded)
	}
	if err != nil {
		return errors.Join(err, t.Close())
	}
	if err := t.raw.SetDeadline(time.Time{}); err != nil {
		return errors.Join(err, t.Close())
	}
	return nil
}

func (t *Transport) operation(ctx context.Context, request *Request, work func(context.Context) error) error {
	bounded, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	if request != nil {
		var err error
		bounded, cancel, err = request.authorizedContext(bounded, t.authority.Now())
		if err != nil {
			return err
		}
		defer cancel()
	}
	select {
	case t.gate <- struct{}{}:
		defer func() { <-t.gate }()
	case <-bounded.Done():
		return bounded.Err()
	}
	return t.withDeadline(bounded, func() error { return work(bounded) })
}

func (t *Transport) writeFrame(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return errors.Join(ErrDenied, err)
	}
	if len(data) == 0 || len(data) > lifecyclev1.MaintenanceBodyLimit {
		return ErrDenied
	}
	frame := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(frame, uint32(len(data)))
	copy(frame[4:], data)
	for len(frame) > 0 {
		n, err := t.conn.Write(frame)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}

func (t *Transport) readFrame(target any) error {
	var header [4]byte
	if _, err := io.ReadFull(t.conn, header[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > lifecyclev1.MaintenanceBodyLimit {
		return ErrDenied
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(t.conn, data); err != nil {
		return err
	}
	return lifecyclev1.DecodeMaintenanceJSON(bytes.NewReader(data), target)
}

func transportUnknown(id string, err error) error {
	if err == nil {
		return nil
	}
	return &TransportError{OperationID: id, Cause: errors.Join(ErrUnknown, err)}
}

// sendPeer verifies the same enrolled counterpart before transmitting signed
// data. The receiving endpoint independently verifies its own peer and message.
func (t *Transport) sendPeer(ctx context.Context, r Request) error {
	records, err := t.authority.verifyAuthorization(ctx, r)
	if err != nil {
		return err
	}
	state := t.conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return ErrDenied
	}
	e := records.enrollment
	identity, pin, usage := e.ExecutorURI, e.ExecutorFingerprint, x509.ExtKeyUsageClientAuth
	if t.role == "executor" {
		identity, pin, usage = e.ManagerURI, e.ManagerFingerprint, x509.ExtKeyUsageServerAuth
		if state.ServerName != e.ManagerDNSName {
			return ErrDenied
		}
	}
	message := SignedAuthority{Certificate: state.PeerCertificates[0].Raw}
	for _, intermediate := range state.PeerCertificates[1:] {
		message.Intermediates = append(message.Intermediates, intermediate.Raw)
	}
	if !enrolledChannel(state, message.Certificate) || certificateFingerprint(message.Certificate) != pin {
		return ErrDenied
	}
	_, err = verifyEnrolledCertificate(e, message, identity, usage, t.authority.Now(), r.Expires)
	return err
}

func (t *Transport) SendRequest(ctx context.Context, r Request, message SignedAuthority) error {
	if t.role != "manager" {
		return ErrDenied
	}
	err := t.operation(ctx, &r, func(ctx context.Context) error {
		if err := t.sendPeer(ctx, r); err != nil {
			return err
		}
		return t.writeFrame(RequestFrame{TransportContract, r, message})
	})
	return transportUnknown(r.ID, err)
}

func (t *Transport) ReceiveRequest(ctx context.Context) (Request, error) {
	if t.role != "executor" {
		return Request{}, ErrDenied
	}
	var frame RequestFrame
	err := t.operation(ctx, nil, func(ctx context.Context) error {
		if err := t.readFrame(&frame); err != nil {
			return err
		}
		if frame.Contract != TransportContract {
			return ErrDenied
		}
		if err := t.authority.Verify(ctx, frame.Request); err != nil {
			return err
		}
		records, err := t.authority.verifyAuthorization(ctx, frame.Request)
		if err != nil {
			return err
		}
		return verifyAuthorityMessage(records.enrollment, frame.Authority, frame.Request,
			decisionKind(frame.Request), "", t.authority.Now(), false)
	})
	if err != nil {
		return Request{}, err
	}
	return frame.Request, nil
}

// Verify lets the existing Executor recheck authority through the same real
// connection before its serializer. It adds no execution or retry path.
func (t *Transport) Verify(ctx context.Context, r Request) error {
	if t.role != "executor" {
		return ErrDenied
	}
	return t.operation(ctx, &r, func(ctx context.Context) error { return t.authority.Verify(ctx, r) })
}

func (t *Transport) SendResult(ctx context.Context, r Request, out Outcome, message SignedAuthority) error {
	if t.role != "executor" {
		return ErrDenied
	}
	err := t.operation(ctx, &r, func(ctx context.Context) error {
		if err := out.Validate(r); err != nil {
			return err
		}
		if err := t.sendPeer(ctx, r); err != nil {
			return err
		}
		return t.writeFrame(ResultFrame{TransportContract, out, message})
	})
	return transportUnknown(r.ID, err)
}

func (t *Transport) ReceiveResult(ctx context.Context, r Request) (Outcome, error) {
	if t.role != "manager" {
		return Outcome{}, ErrDenied
	}
	var frame ResultFrame
	err := t.operation(ctx, &r, func(ctx context.Context) error {
		if err := t.readFrame(&frame); err != nil {
			return err
		}
		if frame.Contract != TransportContract {
			return ErrDenied
		}
		return t.authority.VerifyResult(ctx, r, frame.Outcome, frame.Authority)
	})
	if err != nil {
		return Outcome{}, transportUnknown(r.ID, err)
	}
	return frame.Outcome, nil
}

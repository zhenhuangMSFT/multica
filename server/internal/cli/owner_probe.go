package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const ownerProbeBudget = 10 * time.Second
const ownerProbeResponseLimit = 64 * 1024

// OwnerProbeResult describes authenticated API access, not tunnel, provider or
// admission readiness. No server response bodies or credentials are emitted.
type OwnerProbeResult struct {
	SchemaVersion      int    `json:"schemaVersion"`
	ObservationID      string `json:"observationId"`
	ObservedAtUTC      string `json:"observedAtUtc"`
	Endpoint           string `json:"endpoint"`
	OwnerID            string `json:"ownerId"`
	WorkspaceID        string `json:"workspaceId"`
	State              string `json:"state"`
	ReasonCode         string `json:"reasonCode"`
	APIAuthenticated   bool   `json:"apiAuthenticated"`
	DurationMS         int64  `json:"durationMs"`
	AuthorizesMutation bool   `json:"authorizesMutation"`
}

func validProbeID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func validateOwnerProbeEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" ||
		(u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return fmt.Errorf("owner API probe requires a loopback HTTP root endpoint without credentials, query or fragment")
	}
	// Reject noncanonical host spellings and invalid/out-of-range ports before
	// resolving credentials or making a request.
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("owner API probe requires a valid numeric port")
		}
		for _, ch := range u.Port() {
			if ch < '0' || ch > '9' {
				return fmt.Errorf("owner API probe requires a numeric port")
			}
		}
	}
	if u.Host != u.Hostname() && u.Host != u.Hostname()+":"+u.Port() {
		return fmt.Errorf("owner API probe endpoint is not canonical")
	}
	return nil
}

// ProbeOwnerAPI uses one ten-second budget for both authenticated reads. It
// deliberately ignores the general client's redirects, proxy and timeout policy.
func (c *APIClient) ProbeOwnerAPI(ctx context.Context, expectedEndpoint, ownerID, workspaceID string) (result OwnerProbeResult, err error) {
	start := time.Now()
	result = OwnerProbeResult{
		SchemaVersion: 1, ObservationID: uuid.NewString(), State: "failed", ReasonCode: "protocol_error",
	}
	defer func() {
		result.ObservedAtUTC = time.Now().UTC().Format(time.RFC3339Nano)
		result.DurationMS = time.Since(start).Milliseconds()
	}()
	if err := validateOwnerProbeEndpoint(expectedEndpoint); err != nil {
		return result, err
	}
	if c.BaseURL != strings.TrimRight(expectedEndpoint, "/") || !validProbeID(ownerID) || !validProbeID(workspaceID) ||
		c.WorkspaceID != workspaceID {
		result.ReasonCode = "identity_mismatch"
		return result, fmt.Errorf("owner API probe endpoint/workspace/identity binding is invalid")
	}
	result.Endpoint, result.OwnerID, result.WorkspaceID = expectedEndpoint, ownerID, workspaceID
	if strings.TrimSpace(c.Token) == "" || strings.HasPrefix(c.Token, TaskTokenPrefix) || c.AgentID != "" || c.TaskID != "" {
		result.State, result.ReasonCode = "reauth_required", "authentication_expired"
		return result, fmt.Errorf("owner API probe requires the selected owner's authenticated profile, not a task identity")
	}
	ctx, cancel := context.WithTimeout(ctx, ownerProbeBudget)
	defer cancel()
	endpointURL, _ := url.Parse(expectedEndpoint) // Already validated above.
	port := endpointURL.Port()
	if port == "" {
		port = "80"
	}
	dialer := &net.Dialer{Timeout: ownerProbeBudget}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			// Pin localhost to IPv4 loopback instead of trusting DNS/hosts files
			// with owner credentials. This collector's approved tunnel is IPv4.
			return dialer.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
		},
		DisableKeepAlives:      true,
		ResponseHeaderTimeout:  ownerProbeBudget,
		MaxResponseHeaderBytes: 16 * 1024,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport, Timeout: ownerProbeBudget,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	for _, read := range []struct{ path, expectedID string }{
		{"/api/me", ownerID}, {"/api/workspaces/" + workspaceID, workspaceID},
	} {
		if err := c.probeIdentity(ctx, client, read.path, read.expectedID); err != nil {
			result.State, result.ReasonCode = ownerProbeFailure(err)
			return result, err
		}
	}
	result.State, result.ReasonCode, result.APIAuthenticated = "authenticated", "authenticated", true
	return result, nil
}

var errProbeIdentity = errors.New("owner API probe response identity does not match the expected identity")
var errProbeProtocol = errors.New("owner API probe received an invalid, redirected or oversized JSON response")

func (c *APIClient) probeIdentity(ctx context.Context, client *http.Client, path, expectedID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return errProbeProtocol
	}
	c.setHeaders(req)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		// Never include a credential-bearing response or raw transport detail in
		// collector output, including the CLI's --debug error chain.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &NetworkError{Kind: KindNetworkOffline, Op: "owner API probe", Err: errors.New("loopback API connection failed")}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode < 400 {
			return errProbeProtocol
		}
		return &HTTPError{Method: http.MethodGet, Path: path, StatusCode: resp.StatusCode}
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errProbeProtocol
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, ownerProbeResponseLimit+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errProbeProtocol
	}
	if len(body) > ownerProbeResponseLimit {
		return errProbeProtocol
	}
	id, err := probeObjectID(body)
	if err != nil {
		return errProbeProtocol
	}
	if id != expectedID {
		return errProbeIdentity
	}
	return nil
}

func probeObjectID(body []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return "", errProbeProtocol
	}
	seen := map[string]bool{}
	var id string
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return "", errProbeProtocol
		}
		seen[name] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return "", errProbeProtocol
		}
		if name == "id" {
			if err := json.Unmarshal(value, &id); err != nil || !validProbeID(id) {
				return "", errProbeProtocol
			}
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || id == "" {
		return "", errProbeProtocol
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", errProbeProtocol
	}
	return id, nil
}

func ownerProbeFailure(err error) (string, string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return "failed", "timeout"
	}
	if errors.Is(err, errProbeIdentity) {
		return "failed", "identity_mismatch"
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.StatusCode == 401:
			return "reauth_required", "authentication_expired"
		case httpErr.StatusCode == 403:
			return "failed", "access_denied"
		case httpErr.StatusCode >= 500:
			return "waiting_dependency", "dependency_unavailable"
		}
	}
	var networkErr *NetworkError
	if errors.As(err, &networkErr) {
		return "waiting_dependency", "dependency_unavailable"
	}
	return "failed", "protocol_error"
}

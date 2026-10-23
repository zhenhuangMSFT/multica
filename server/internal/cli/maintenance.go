package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/multica-ai/multica/server/internal/util"
	lifecyclev1 "github.com/multica-ai/multica/server/pkg/lifecycle/v1"
)

// Maintenance talks only to the authenticated backend, never the local daemon.
// Submit and result reads are single requests; ambiguous failures are not retried.
func (c *APIClient) Maintenance(ctx context.Context, runtimeID, requestID string, command *lifecyclev1.MaintenanceCommand) (*lifecyclev1.MaintenanceRequest, error) {
	if _, err := util.ParseUUID(runtimeID); err != nil {
		return nil, errors.New("invalid runtime ID")
	}
	method, path := http.MethodGet, "/api/runtimes/"+runtimeID+"/maintenance/"+requestID
	var payload []byte
	if command != nil {
		if err := command.Validate(time.Now()); err != nil {
			return nil, err
		}
		method, path = http.MethodPost, "/api/runtimes/"+runtimeID+"/maintenance"
		payload, _ = json.Marshal(command)
		requestID = command.ID
	} else if _, err := util.ParseUUID(requestID); err != nil {
		return nil, errors.New("invalid maintenance request ID")
	}
	expectedStatus := http.StatusAccepted
	if method == http.MethodGet {
		expectedStatus = http.StatusOK
	}
	var result lifecyclev1.MaintenanceRequest
	if err := c.maintenanceCall(ctx, method, path, payload, expectedStatus, &result); err != nil {
		return nil, err
	}
	if result.Validate() != nil || result.Delivery.Command.ID != requestID || (command != nil && result.Delivery.Command != *command) {
		return nil, errors.New("invalid or mismatched maintenance response")
	}
	return &result, nil
}

func (c *APIClient) MaintenanceCapabilities(ctx context.Context, runtimeID string) (*lifecyclev1.MaintenanceCapabilities, error) {
	if _, err := util.ParseUUID(runtimeID); err != nil {
		return nil, errors.New("invalid runtime ID")
	}
	var result lifecyclev1.MaintenanceCapabilities
	if err := c.maintenanceCall(ctx, http.MethodGet, "/api/runtimes/"+runtimeID+"/maintenance/capabilities", nil, http.StatusOK, &result); err != nil {
		return nil, err
	}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *APIClient) maintenanceCall(ctx context.Context, method, path string, payload []byte, expectedStatus int, target any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return errors.New("invalid maintenance backend URL")
	}
	request.Header.Set("Authorization", "Bearer "+c.Token)
	request.Header.Set("X-Workspace-ID", c.WorkspaceID)
	request.Header.Set("Content-Type", "application/json")
	client := *c.HTTPClient
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if transport, ok := transport.(*http.Transport); ok {
		bounded := transport.Clone()
		bounded.MaxResponseHeaderBytes = lifecyclev1.MaintenanceBodyLimit
		defer bounded.CloseIdleConnections()
		client.Transport = bounded
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("maintenance backend unavailable; command outcome may be unknown; read the same request ID before further action")
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		return fmt.Errorf("maintenance backend returned HTTP %d", response.StatusCode)
	}
	return lifecyclev1.DecodeMaintenanceJSON(response.Body, target)
}

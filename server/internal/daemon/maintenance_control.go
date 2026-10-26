package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	lifecyclev1 "github.com/multica-ai/multica/server/pkg/lifecycle/v1"
)

type maintenanceReceipt struct {
	Delivery lifecyclev1.MaintenanceDelivery `json:"delivery"`
	Result   *lifecyclev1.MaintenanceResult  `json:"result"`
}

type maintenanceReceipts struct {
	dir     string
	entries map[string]maintenanceReceipt
}

const maintenanceReceiptLimit = 1024

func (d *Daemon) loadMaintenanceReceipts(dir string) error {
	store := &maintenanceReceipts{dir: dir, entries: make(map[string]maintenanceReceipt)}
	check := &maintenanceFileStore{}
	if err := check.checkPath(dir); err != nil {
		return err
	}
	files, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(files) > 2*maintenanceReceiptLimit {
		return errors.New("maintenance command receipt limit exceeded; offline archival required")
	}
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".intent") {
			if !strings.HasSuffix(file.Name(), ".result") {
				return errors.New("unknown maintenance receipt file")
			}
			continue
		}
		path := filepath.Join(dir, file.Name())
		var receipt maintenanceReceipt
		if err := readMaintenanceReceipt(path, &receipt); err != nil {
			return err
		}
		id := receipt.Delivery.Command.ID
		if file.Name() != id+".intent" || !validMaintenanceReceipt(receipt, d.observeMaintenance().Record, d.cfg.DaemonID) {
			return errors.New("maintenance receipt binding mismatch")
		}
		var result maintenanceReceipt
		if err := readMaintenanceReceipt(filepath.Join(dir, id+".result"), &result); err == nil {
			if result.Delivery != receipt.Delivery || result.Result == nil || result.Result.Delivery != receipt.Delivery || result.Result.Validate() != nil {
				return errors.New("maintenance result receipt binding mismatch")
			}
			receipt = result
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		store.entries[id] = receipt
	}
	// A result without its durable intent is corruption, not an empty inbox.
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".result") {
			if _, ok := store.entries[strings.TrimSuffix(file.Name(), ".result")]; !ok {
				return errors.New("maintenance result has no intent")
			}
		}
	}
	d.maintenanceReceipts = store
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	for _, entry := range store.entries {
		command := entry.Delivery.Command
		if entry.Result == nil && command.Operation != "status" && command.Generation >= d.maintenance.record.Generation {
			d.maintenance.fenced, d.maintenance.reloaded = true, true
			if err := d.maintenance.store.fence(); err != nil {
				d.maintenance.fault = err
				return err
			}
		}
	}
	return nil
}

func validMaintenanceReceipt(receipt maintenanceReceipt, record maintenanceRecord, daemon string) bool {
	command := receipt.Delivery.Command
	request := lifecyclev1.MaintenanceRequest{Delivery: receipt.Delivery, State: "pending"}
	return receipt.Result == nil && command.Host == record.Host && command.Profile == record.Profile &&
		command.DaemonID == daemon && request.Validate() == nil
}

func readMaintenanceReceipt(path string, receipt *maintenanceReceipt) error {
	if err := (&maintenanceFileStore{}).checkPath(path); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > lifecyclev1.MaintenanceBodyLimit {
		return errors.New("invalid maintenance receipt file")
	}
	return lifecyclev1.DecodeMaintenanceJSON(file, receipt)
}

func (s *maintenanceReceipts) write(suffix string, receipt maintenanceReceipt) error {
	if err := (&maintenanceFileStore{}).checkPath(s.dir); err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(s.dir, receipt.Delivery.Command.ID+suffix)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	data, err := json.Marshal(receipt)
	if err == nil {
		_, err = file.Write(append(data, '\n'))
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (d *Daemon) maintenanceStatus() lifecyclev1.MaintenanceStatus {
	o := d.observeMaintenance()
	return lifecyclev1.MaintenanceStatus{
		ObservedAt: time.Now().UTC(),
		Host:       o.Record.Host, Profile: o.Record.Profile, Holder: o.Record.Holder, Generation: o.Record.Generation,
		ExpiresAt: o.Record.ExpiresAt, Blocked: o.Blocked, ClaimsInFlight: o.ClaimsInFlight,
		ActiveTasks: o.ActiveTasks, RunningTasks: o.RunningTasks, ResourceWait: o.ResourceWait,
		LocalQuiescent: o.LocalQuiescent, CompleteDrain: false, PersistenceFault: o.PersistenceFault != nil, RecoveryRequired: o.RecoveryRequired,
	}
}

func (d *Daemon) maintenancePersistenceFailed(err error) {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	d.maintenance.fenced, d.maintenance.fault = true, err
	if d.maintenance.store != nil {
		if fenceErr := d.maintenance.store.fence(); fenceErr != nil {
			d.maintenance.fault = errors.Join(err, fenceErr)
		}
	}
}

// The caller serializes command execution. Only a fresh authenticated backend
// delivery reaches here; neither a local listener nor receipt replay grants
// permission to mutate admission.
func (d *Daemon) executeMaintenance(delivery lifecyclev1.MaintenanceDelivery, now time.Time) lifecyclev1.MaintenanceResult {
	command := delivery.Command
	result := lifecyclev1.MaintenanceResult{Delivery: delivery, Code: "rejected"}
	status := d.maintenanceStatus()
	request := lifecyclev1.MaintenanceRequest{Delivery: delivery, State: "pending"}
	if command.Validate(now) != nil || command.DaemonID != d.cfg.DaemonID ||
		command.Host != status.Host || command.Profile != status.Profile || request.Validate() != nil {
		result.Code = "target_mismatch"
		result.Status.Host, result.Status.Profile = command.Host, command.Profile
		result.Status.ObservedAt = now.UTC()
		return result
	}
	receipts := d.maintenanceReceipts
	if receipts == nil {
		result.Code, result.Status = "persistence_fault", status
		return result
	}
	if existing, ok := receipts.entries[command.ID]; ok {
		if existing.Delivery != delivery {
			result.Code, result.Status = "rejected", status
		} else if existing.Result != nil {
			return *existing.Result
		} else {
			result.Code, result.Status = "outcome_unknown", status
		}
		return result
	}
	// Observations do not mutate admission and need no durable replay receipt.
	// In particular, diagnostics must not exhaust the mutation safety budget.
	if command.Operation == "status" {
		result.Code, result.Status = "ok", status
		if status.PersistenceFault {
			result.Code = "persistence_fault"
		}
		return result
	}
	intent := maintenanceReceipt{Delivery: delivery}
	if len(receipts.entries) >= maintenanceReceiptLimit {
		d.maintenancePersistenceFailed(errors.New("maintenance receipt limit reached"))
		result.Code, result.Status = "persistence_fault", d.maintenanceStatus()
		return result
	}
	if err := receipts.write(".intent", intent); err != nil {
		d.maintenancePersistenceFailed(err)
		result.Code, result.Status = "persistence_fault", d.maintenanceStatus()
		return result
	}
	receipts.entries[command.ID] = intent
	var err error
	switch command.Operation {
	case "acquire":
		_, err = d.acquireMaintenance(command.Holder, command.Generation, command.ExpiresAt, now)
	case "recover":
		_, err = d.recoverMaintenance(command.Holder, command.Generation, command.ExpiresAt, now)
	case "release":
		err = d.releaseMaintenance(command.Holder, command.Generation, now)
	}
	result.Code = "ok"
	if err != nil {
		result.Code = "rejected"
	}
	result.Status = d.maintenanceStatus()
	if result.Status.PersistenceFault {
		result.Code = "persistence_fault"
	}
	intent.Result = &result
	if err := receipts.write(".result", intent); err != nil {
		d.maintenancePersistenceFailed(err)
		result.Code, result.Status = "persistence_fault", d.maintenanceStatus()
		return result
	}
	receipts.entries[command.ID] = intent
	return result
}

func (d *Daemon) pollMaintenance(ctx context.Context, runtimeID string) {
	if d.maintenanceReceipts == nil || d.client == nil {
		return
	}
	if !d.maintenanceControlMu.TryLock() {
		return
	}
	defer d.maintenanceControlMu.Unlock()
	if time.Since(d.lastMaintenancePoll) < 15*time.Second {
		return
	}
	d.lastMaintenancePoll = time.Now()
	var delivery lifecyclev1.MaintenanceDelivery
	found, err := d.client.maintenanceRequest(ctx, "/api/daemon/runtimes/"+runtimeID+"/maintenance/claim", nil, &delivery)
	if err != nil {
		d.logger.Debug("maintenance delivery unavailable; admission unchanged")
		return
	}
	if !found {
		return
	}
	if request := (lifecyclev1.MaintenanceRequest{Delivery: delivery, State: "pending"}); request.Validate() != nil {
		d.logger.Warn("invalid maintenance delivery refused")
		return
	}
	result := d.executeMaintenance(delivery, time.Now())
	_, err = d.client.maintenanceRequest(ctx,
		"/api/daemon/runtimes/"+runtimeID+"/maintenance/"+delivery.Command.ID+"/result", result, nil)
	if err != nil {
		d.logger.Warn("maintenance result not acknowledged; query status before any recovery", "code", result.Code)
	}
}

func (c *Client) maintenanceRequest(ctx context.Context, path string, payload any, target any) (bool, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return false, errors.New("invalid maintenance endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	client := *c.client
	if transport, ok := c.client.Transport.(*http.Transport); ok {
		bounded := transport.Clone()
		bounded.MaxResponseHeaderBytes = lifecyclev1.MaintenanceBodyLimit
		defer bounded.CloseIdleConnections()
		client.Transport = bounded
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return false, errors.New("maintenance transport unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent && target != nil {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("maintenance backend returned HTTP %d", resp.StatusCode)
	}
	if target != nil {
		if err := lifecyclev1.DecodeMaintenanceJSON(resp.Body, target); err != nil {
			return false, err
		}
	} else {
		var ack lifecyclev1.MaintenanceAcknowledgment
		if err := lifecyclev1.DecodeMaintenanceJSON(resp.Body, &ack); err != nil {
			return false, err
		}
		result, ok := payload.(lifecyclev1.MaintenanceResult)
		if !ok || !ack.Accepted || ack.ID != result.Delivery.Command.ID {
			return false, errors.New("maintenance result acknowledgment mismatch")
		}
	}
	return true, nil
}

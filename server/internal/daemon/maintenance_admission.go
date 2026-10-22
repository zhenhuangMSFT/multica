package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// maintenanceRecord is local admission ownership, never a complete drain lease.
type maintenanceRecord struct {
	Version    int       `json:"version"`
	Host       string    `json:"host"`
	Profile    string    `json:"profile"`
	Holder     string    `json:"holder"`
	Generation uint64    `json:"generation"`
	Held       bool      `json:"held"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

type maintenancePersistence interface {
	load() (maintenanceRecord, bool, error)
	fence() error
	append(maintenanceRecord) error
	unfence() error
}

type maintenanceAdmission struct {
	record   maintenanceRecord
	store    maintenancePersistence
	fenced   bool
	fault    error
	reloaded bool
}

func (m *maintenanceAdmission) blocked() bool {
	return m.fenced || m.record.Held || m.fault != nil
}

func (d *Daemon) loadMaintenanceAdmission() error {
	host, err := os.Hostname()
	if err != nil {
		return err
	}
	dir, err := cli.ProfileDir(d.cfg.Profile)
	if err != nil {
		return err
	}
	if err := d.initializeMaintenance(&maintenanceFileStore{
		path: filepath.Join(dir, "maintenance-admission.jsonl"), host: host, profile: d.cfg.Profile,
	}); err != nil {
		return err
	}
	return d.loadMaintenanceReceipts(filepath.Join(dir, "maintenance-commands"))
}

func (d *Daemon) initializeMaintenance(store maintenancePersistence) error {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	d.maintenance = maintenanceAdmission{store: store, fenced: true}
	record, fenced, err := store.load()
	if err != nil {
		d.maintenance.fault = err
		return err
	}
	d.maintenance.record, d.maintenance.fenced, d.maintenance.reloaded = record, fenced, fenced || record.Held
	return nil
}

// acquireMaintenance pauses only local batch claims. Claims already in flight
// finish their normal handoff and active/resource-wait tasks are not cancelled.
func (d *Daemon) acquireMaintenance(holder string, expected uint64, expires, now time.Time) (maintenanceRecord, error) {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	m := &d.maintenance
	if m.store == nil || m.blocked() || d.pauseClaims || d.updating.Load() {
		return m.record, errors.New("maintenance acquisition unavailable or admission already held")
	}
	return d.persistMaintenanceHolder(holder, expected, expires, now)
}

// recoverMaintenance is an explicit CAS takeover, including after a restart or
// failed write. Expiry alone never clears admission; the new generation fences
// releases by the old holder. Persistence faults require a healthy reload, so
// an ambiguous persistence error cannot be retried in place.
func (d *Daemon) recoverMaintenance(holder string, expected uint64, expires, now time.Time) (maintenanceRecord, error) {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	m := &d.maintenance
	if m.store == nil || m.fault != nil || !m.blocked() || d.pauseClaims || d.updating.Load() {
		return m.record, errors.New("maintenance recovery requires healthy held admission")
	}
	if !m.reloaded && m.record.Held && now.Before(m.record.ExpiresAt) {
		return m.record, errors.New("maintenance holder has not expired; recovery requires restart observation")
	}
	return d.persistMaintenanceHolder(holder, expected, expires, now)
}

// Caller owns claimMu across the durable transition and claim rejection.
func (d *Daemon) persistMaintenanceHolder(holder string, expected uint64, expires, now time.Time) (maintenanceRecord, error) {
	m := &d.maintenance
	if holder == "" || strings.TrimSpace(holder) != holder || len(holder) > 128 ||
		strings.ContainsAny(holder, "\r\n\x00") || !expires.After(now) || expires.After(now.Add(24*time.Hour)) ||
		m.record.Generation != expected || expected == math.MaxUint64 {
		return m.record, errors.New("invalid maintenance holder, generation or expiry")
	}
	m.fenced = true
	if err := m.store.fence(); err != nil {
		m.fault = fmt.Errorf("persist maintenance fence: %w", err)
		return m.record, m.fault
	}
	next := m.record
	next.Holder, next.Generation, next.Held, next.ExpiresAt = holder, expected+1, true, expires.UTC()
	if err := m.store.append(next); err != nil {
		m.fault = fmt.Errorf("persist maintenance acquisition: %w", err)
		return m.record, m.fault
	}
	m.record = next
	m.reloaded = false
	return next, nil
}

func (d *Daemon) releaseMaintenance(holder string, generation uint64, now time.Time) error {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	m := &d.maintenance
	if m.store == nil || m.fault != nil || !m.record.Held || m.record.Holder != holder || m.record.Generation != generation ||
		!now.Before(m.record.ExpiresAt) || m.reloaded {
		return errors.New("maintenance release rejected: stale holder/generation or persistence fault")
	}
	next := m.record
	next.Held, next.Holder, next.ExpiresAt = false, "", time.Time{}
	if err := m.store.append(next); err != nil {
		m.fault = fmt.Errorf("persist maintenance release: %w", err)
		return m.fault
	}
	if err := m.store.unfence(); err != nil {
		m.fault = fmt.Errorf("remove maintenance fence: %w", err)
		return m.fault
	}
	m.record, m.fenced = next, false
	return nil
}

type maintenanceObservation struct {
	Record           maintenanceRecord
	Blocked          bool
	ClaimsInFlight   int
	ActiveTasks      int64
	RunningTasks     int64
	ResourceWait     int64
	LocalQuiescent   bool
	CompleteDrain    bool
	RecoveryRequired bool
	PersistenceFault error
}

func (d *Daemon) observeMaintenance() maintenanceObservation {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	m := &d.maintenance
	active, running, waiting := d.activeTasks.Load(), d.runningTasks.Load(), d.resourceWaitTasks.Load()
	return maintenanceObservation{
		Record: m.record, Blocked: m.blocked(), ClaimsInFlight: d.claimsInFlight,
		ActiveTasks: active, RunningTasks: running, ResourceWait: waiting,
		LocalQuiescent: m.blocked() && m.fault == nil && d.claimsInFlight == 0 && active == 0 && running == 0 && waiting == 0,
		CompleteDrain:  false, PersistenceFault: m.fault,
		RecoveryRequired: m.reloaded || (m.record.Held && !time.Now().Before(m.record.ExpiresAt)),
	}
}

// The daemon's existing exclusive profile process lock owns this store. No
// external writer may append to it. The fence survives ambiguous/torn journal
// writes, and is removed only after a synced explicit release record.
type maintenanceFileStore struct {
	path          string
	host, profile string
}

const maintenanceJournalLimit = 1 << 20

func (s *maintenanceFileStore) checkPath(path string) error {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("maintenance symlink path refused")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}

func (s *maintenanceFileStore) load() (maintenanceRecord, bool, error) {
	record := maintenanceRecord{Version: 1, Host: s.host, Profile: s.profile}
	if err := s.checkPath(s.path); err != nil {
		return record, true, err
	}
	if err := s.checkPath(s.path + ".held"); err != nil {
		return record, true, err
	}
	_, fenceErr := os.Lstat(s.path + ".held")
	fenced := fenceErr == nil
	if fenceErr != nil && !errors.Is(fenceErr, os.ErrNotExist) {
		return record, true, fenceErr
	}
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		if fenced {
			return record, true, errors.New("maintenance journal missing while fenced")
		}
		return record, false, nil
	}
	if err != nil {
		return record, true, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return record, true, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maintenanceJournalLimit {
		return record, true, errors.New("invalid or oversized maintenance journal")
	}
	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF && len(line) == 0 {
			break
		}
		if err != nil {
			return record, true, errors.New("incomplete maintenance journal; explicit repair required")
		}
		next, err := decodeMaintenanceRecord(line)
		if err != nil || next.Version != 1 || next.Host != s.host || next.Profile != s.profile ||
			(record.Generation == math.MaxUint64 && next.Held) ||
			(next.Held && (next.Holder == "" || next.ExpiresAt.IsZero() || next.Generation != record.Generation+1)) ||
			(!next.Held && (!record.Held || next.Generation != record.Generation || next.Holder != "" || !next.ExpiresAt.IsZero())) {
			return record, true, errors.New("invalid maintenance journal transition/binding")
		}
		record = next
	}
	return record, fenced || record.Held, nil
}

func decodeMaintenanceRecord(line []byte) (maintenanceRecord, error) {
	var record maintenanceRecord
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return record, errors.New("trailing maintenance JSON")
	}
	// Reject duplicate fields rather than accepting a last-value ownership key.
	keys := json.NewDecoder(bytes.NewReader(line))
	if _, err := keys.Token(); err != nil {
		return record, err
	}
	seen := map[string]bool{}
	allowed := map[string]bool{"version": true, "host": true, "profile": true, "holder": true, "generation": true, "held": true, "expiresAt": true}
	for keys.More() {
		key, err := keys.Token()
		if err != nil {
			return record, err
		}
		name, ok := key.(string)
		if !ok || seen[name] || !allowed[name] {
			return record, errors.New("duplicate maintenance JSON key")
		}
		seen[name] = true
		var value json.RawMessage
		if err := keys.Decode(&value); err != nil {
			return record, err
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return record, errors.New("null maintenance JSON field")
		}
	}
	if len(seen) != 7 {
		return record, errors.New("missing maintenance JSON fields")
	}
	return record, nil
}

func (s *maintenanceFileStore) fence() error {
	if err := s.checkPath(s.path + ".held"); err != nil {
		return err
	}
	file, err := os.OpenFile(s.path+".held", os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(syncErr, closeErr)
}

func (s *maintenanceFileStore) append(record maintenanceRecord) (err error) {
	if err := s.checkPath(s.path); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size()+int64(len(data))+1 > maintenanceJournalLimit {
		return errors.New("maintenance journal full; offline reviewed archival required")
	}
	data = append(data, '\n')
	n, err := file.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return file.Sync()
}

func (s *maintenanceFileStore) unfence() error {
	return os.Remove(s.path + ".held")
}

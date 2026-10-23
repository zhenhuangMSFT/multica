package executor

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"
)

// FileJournal is a source/test adapter, not a qualified protected Windows store.
// The exclusive cooperating lock is never stolen on age/PID guesses.
type FileJournal struct {
	mu       sync.Mutex
	file     *os.File
	lock     *os.File
	lockPath string
	latest   []byte
	digest   string
	sequence uint64
	fault    error
}

type journalEntry struct {
	Sequence uint64          `json:"sequence"`
	Previous string          `json:"previous"`
	State    json.RawMessage `json:"state"`
	Digest   string          `json:"digest"`
}

func OpenFileJournal(directory string) (*FileJournal, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	for p := absolute; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, errors.New("executor store requires existing non-symlink directories")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	lockPath := filepath.Join(absolute, "executor.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*FileJournal, error) {
		closeErr := lock.Close()
		removeErr := os.Remove(lockPath)
		return nil, errors.Join(err, closeErr, removeErr)
	}
	path := filepath.Join(absolute, "executor.jsonl")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Size() > 16<<20 {
			return fail(errors.New("invalid or oversized executor journal"))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return fail(err)
	}
	store := &FileJournal{file: file, lock: lock, lockPath: lockPath}
	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF && len(line) == 0 {
			break
		}
		if err != nil || len(line) > 4<<20 {
			file.Close()
			return fail(errors.New("incomplete or oversized executor journal entry"))
		}
		var entry journalEntry
		decodeErr := decodeStored(line, &entry)
		if decodeErr != nil || entry.Sequence != store.sequence+1 || entry.Previous != store.digest ||
			entry.Digest != entryDigest(entry.Sequence, entry.Previous, entry.State) {
			file.Close()
			return fail(errors.New("executor journal sequence or digest mismatch"))
		}
		store.latest, store.sequence, store.digest = bytes.Clone(entry.State), entry.Sequence, entry.Digest
	}
	return store, nil
}

func decodeStored(data []byte, target any) error {
	// Stored maps/arrays require duplicate detection too; the public lifecycle
	// decoder intentionally only accepts its fixed object-only control schema.
	tokens := json.NewDecoder(bytes.NewReader(data))
	var scan func() error
	scan = func() error {
		token, err := tokens.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := token.(json.Delim); ok {
			keys := map[string]bool{}
			for tokens.More() {
				if delimiter == '{' {
					key, err := tokens.Token()
					name, ok := key.(string)
					if err != nil || !ok || keys[name] {
						return errors.New("duplicate or invalid stored key")
					}
					keys[name] = true
				}
				if err := scan(); err != nil {
					return err
				}
			}
			_, err = tokens.Token()
			return err
		}
		return nil
	}
	if err := scan(); err != nil {
		return err
	}
	if _, err := tokens.Token(); err != io.EOF {
		return errors.New("trailing stored data")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return requireStoredFields(data, reflect.TypeOf(target).Elem())
}

func requireStoredFields(data json.RawMessage, typ reflect.Type) error {
	if typ.Kind() == reflect.Pointer {
		if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
			return nil
		}
		return requireStoredFields(data, typ.Elem())
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("null executor state field")
	}
	if typ == reflect.TypeOf(json.RawMessage{}) || typ == reflect.TypeOf(time.Time{}) {
		return nil
	}
	switch typ.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil || len(fields) != typ.NumField() {
			return errors.New("missing or unknown executor state field")
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			raw, exists := fields[field.Tag.Get("json")]
			if !exists {
				return errors.New("missing executor state field")
			}
			if err := requireStoredFields(raw, field.Type); err != nil {
				return err
			}
		}
	case reflect.Map:
		var values map[string]json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		for _, raw := range values {
			if err := requireStoredFields(raw, typ.Elem()); err != nil {
				return err
			}
		}
	case reflect.Array:
		var values []json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil || len(values) != typ.Len() {
			return errors.New("invalid executor state array")
		}
		for _, raw := range values {
			if err := requireStoredFields(raw, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func entryDigest(sequence uint64, previous string, state []byte) string {
	sum := sha256.Sum256(append([]byte(fmt.Sprintf("%d:%s:", sequence, previous)), state...))
	return hex.EncodeToString(sum[:])
}

func (s *FileJournal) Load() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil || s.fault != nil {
		return nil, errors.Join(ErrUnavailable, s.fault)
	}
	return bytes.Clone(s.latest), nil
}

func (s *FileJournal) Commit(state []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil || s.fault != nil {
		return errors.Join(ErrUnavailable, s.fault)
	}
	entry := journalEntry{Sequence: s.sequence + 1, Previous: s.digest, State: state}
	entry.Digest = entryDigest(entry.Sequence, entry.Previous, state)
	data, err := json.Marshal(entry)
	if err == nil {
		info, statErr := s.file.Stat()
		err = statErr
		if err == nil && (len(data) > 4<<20 || info.Size()+int64(len(data))+1 > 16<<20) {
			err = errors.New("executor journal retention limit; reviewed archival required")
		}
	}
	if err == nil {
		_, err = s.file.Write(append(data, '\n'))
	}
	if err == nil {
		err = s.file.Sync()
	}
	if err != nil {
		s.fault = err
		return err
	}
	s.latest, s.sequence, s.digest = bytes.Clone(state), entry.Sequence, entry.Digest
	return nil
}

func (s *FileJournal) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := errors.Join(s.file.Close(), s.lock.Close(), os.Remove(s.lockPath))
	s.file = nil
	return err
}

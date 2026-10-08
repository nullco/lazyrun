//go:build linux

package supervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"lazyrun/internal/model"
	"lazyrun/internal/securefs"
)

const metadataVersion = 1
const maxMetadataBytes = 8 * 1024 * 1024

type record struct {
	Version int       `json:"version"`
	Run     model.Run `json:"run"`
}
type store struct {
	dir       *securefs.Dir
	projectID string
	mu        sync.Mutex
}

func metadataName(alias string) string {
	sum := sha256.Sum256([]byte(alias))
	return hex.EncodeToString(sum[:]) + ".json"
}

func (s *store) Save(run model.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run.ProjectID != s.projectID || run.ID == "" || run.Definition.Alias == "" {
		return errors.New("invalid run metadata")
	}
	data, err := json.Marshal(record{Version: metadataVersion, Run: run})
	if err != nil {
		return err
	}
	if len(data) > maxMetadataBytes {
		return errors.New("run metadata exceeds storage limit")
	}
	return s.dir.AtomicWrite(metadataName(run.Definition.Alias), data)
}

func (s *store) Load() ([]model.Run, error) {
	names, err := s.dir.Names()
	if err != nil {
		return nil, err
	}
	var runs []model.Run
	for _, name := range names {
		if strings.HasPrefix(name, ".tmp-") {
			continue
		} // Never unlink arbitrary leftover files.
		if name == "diagnostic.log" || name == "state.lock" {
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			return nil, fmt.Errorf("unexpected state file %s; refusing unsafe reconciliation", name)
		}
		b, err := s.dir.Read(name, maxMetadataBytes)
		if err != nil {
			return nil, err
		}
		var rec record
		if err := json.Unmarshal(b, &rec); err != nil {
			return nil, fmt.Errorf("invalid metadata %s: %w", name, err)
		}
		if rec.Version != metadataVersion || rec.Run.ProjectID != s.projectID || rec.Run.ID == "" || rec.Run.Definition.Alias == "" || name != metadataName(rec.Run.Definition.Alias) {
			return nil, fmt.Errorf("incompatible or invalid metadata %s; manual inspection required", name)
		}
		switch rec.Run.Lifecycle {
		case model.Starting, model.Running, model.Stopping, model.Exited, model.Unknown:
		default:
			return nil, fmt.Errorf("invalid lifecycle in %s", name)
		}
		if rec.Run.Definition.Kind != model.Service && rec.Run.Definition.Kind != model.Task {
			return nil, fmt.Errorf("invalid command kind in %s", name)
		}
		runs = append(runs, rec.Run)
	}
	return runs, nil
}

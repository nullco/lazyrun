//go:build linux

package runtime

import "lazyrun/internal/model"

func (m *Manager) ReadOutput(alias, runID string, after uint64, limit int) (model.LogRead, error) {
	s, err := m.lookup(alias)
	if err != nil {
		return model.LogRead{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if runID == "" || s.run.ID != runID {
		return model.LogRead{}, ErrRunChanged
	}
	if limit == 0 {
		limit = MaxLogRead
	}
	if limit < 1 || limit > MaxLogRead {
		return model.LogRead{}, ErrCursor
	}
	if s.outputUnavailable {
		return model.LogRead{RunID: runID, Unavailable: true}, nil
	}
	return s.output.read(runID, after, limit)
}

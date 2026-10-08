//go:build linux

package runtime

import (
	"lazyrun/internal/logstore"
	"lazyrun/internal/model"
)

func (m *Manager) ReadOutput(alias, runID string, after uint64, limit int) (model.LogRead, error) {
	return m.readOutput(alias, runID, after, -1, limit)
}

func (m *Manager) TailOutput(alias, runID string, lines, limit int) (model.LogRead, error) {
	if lines < 0 {
		return model.LogRead{}, ErrCursor
	}
	return m.readOutput(alias, runID, 0, lines, limit)
}

func (m *Manager) readOutput(alias, runID string, after uint64, tail, limit int) (model.LogRead, error) {
	s, err := m.lookup(alias)
	if err != nil {
		return model.LogRead{}, err
	}
	s.mu.Lock()
	if runID == "" || s.run.ID != runID {
		s.mu.Unlock()
		return model.LogRead{}, ErrRunChanged
	}
	disk, memory, unavailable := s.diskOutput, s.output, s.outputUnavailable
	s.mu.Unlock() // disk reads cannot hold a lifecycle lock or a socket's lifetime
	if limit == 0 {
		limit = MaxLogRead
	}
	if limit < 1 || limit > MaxLogRead {
		return model.LogRead{}, ErrCursor
	}
	if unavailable {
		return model.LogRead{RunID: runID, Unavailable: true}, nil
	}
	if disk != nil {
		if tail >= 0 {
			return disk.Tail(tail, limit)
		}
		return disk.Read(after, limit)
	}
	if tail >= 0 {
		memory.mu.Lock()
		total := memory.total
		memory.mu.Unlock()
		after = 0
		if total > uint64(limit) {
			after = total - uint64(limit)
		}
	}
	r, err := memory.read(runID, after, limit)
	if tail >= 0 {
		logstore.TrimTail(&r, tail)
	}
	return r, err
}

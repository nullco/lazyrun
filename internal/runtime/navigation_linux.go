//go:build linux

package runtime

import (
	"github.com/nullco/lazyrun/internal/logsearch"
	"github.com/nullco/lazyrun/internal/model"
)

func (m *Manager) WindowOutput(alias, runID string, anchor uint64, before, limit int) (model.LogRead, error) {
	if limit == 0 {
		limit = MaxLogRead
	}
	if before < 0 || before > limit || limit < 1 || limit > MaxLogRead {
		return model.LogRead{}, ErrCursor
	}
	s, err := m.lookup(alias)
	if err != nil {
		return model.LogRead{}, err
	}
	s.mu.Lock()
	if runID == "" || s.run.ID != runID {
		s.mu.Unlock()
		return model.LogRead{}, ErrRunChanged
	}
	disk := s.diskOutput
	s.mu.Unlock()
	if disk == nil {
		return model.LogRead{RunID: runID, Unavailable: true}, nil
	}
	return disk.Window(anchor, before, limit)
}

func (m *Manager) SearchOutput(alias, runID string, request model.LogSearchRequest) (model.LogSearchResult, error) {
	result, err := logsearch.Scan(request, func(after uint64, limit int) (logsearch.Read, error) {
		r, err := m.ReadOutput(alias, runID, after, limit)
		result := logsearch.Read{Next: r.Next, First: r.First, End: r.End, Unavailable: r.Unavailable, Error: r.Error}
		for _, record := range r.Records {
			result.Records = append(result.Records, logsearch.Record{Cursor: record.Cursor, Data: record.Data})
		}
		return result, err
	})
	result.RunID = runID
	return result, err
}

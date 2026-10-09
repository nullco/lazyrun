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
	disk, memory, unavailable := s.diskOutput, s.output, s.outputUnavailable
	s.mu.Unlock()
	if unavailable {
		return model.LogRead{RunID: runID, Unavailable: true}, nil
	}
	if disk != nil {
		return disk.Window(anchor, before, limit)
	}
	memory.mu.Lock()
	total := memory.total
	first := total - uint64(len(memory.data))
	memory.mu.Unlock()
	if anchor > total {
		return model.LogRead{}, ErrCursor
	}
	start := anchor
	if uint64(before) > start {
		start = 0
	} else {
		start -= uint64(before)
	}
	return memory.read(runID, max(first, start), limit)
}

func (m *Manager) SearchOutput(alias, runID string, request model.LogSearchRequest) (model.LogSearchResult, error) {
	result, err := logsearch.Scan(request, func(after uint64, limit int) (logsearch.Read, error) {
		r, err := m.ReadOutput(alias, runID, after, limit)
		result := logsearch.Read{Next: r.Next, First: r.First, End: r.End, Unavailable: r.Unavailable, Error: r.Error}
		for _, record := range r.Records {
			result.Records = append(result.Records, logsearch.Record{Cursor: record.Cursor, Data: record.Data})
		}
		if len(r.Records) == 0 && len(r.Data) > 0 {
			result.Records = []logsearch.Record{{Cursor: r.Next - uint64(len(r.Data)), Data: r.Data}}
		}
		return result, err
	})
	result.RunID = runID
	return result, err
}

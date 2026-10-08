package runtime

import (
	"errors"
	"lazyrun/internal/model"
)

const MaxLogRead = 64 * 1024

var ErrCursor = errors.New("invalid log cursor or read limit")

// These byte cursors are the M3 memory-tail transport adapter. Capture-time
// records and disk retention are M4 work; the run ID remains mandatory.
func (b *memoryOutput) read(runID string, after uint64, limit int) (model.LogRead, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if after > b.total || limit < 1 || limit > MaxLogRead {
		return model.LogRead{}, ErrCursor
	}
	first := b.total - uint64(len(b.data))
	gap := after < first
	if gap {
		after = first
	}
	end := after + uint64(limit)
	if end > b.total {
		end = b.total
	}
	return model.LogRead{RunID: runID, Data: append([]byte(nil), b.data[after-first:end-first]...), Next: end, Truncated: gap}, nil
}

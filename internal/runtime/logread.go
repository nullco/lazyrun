package runtime

import (
	"lazyrun/internal/logstore"
	"lazyrun/internal/model"
)

const MaxLogRead = logstore.MaxRead

var ErrCursor = logstore.ErrCursor

// The memory adapter is used only in lifecycle fixtures; the CLI uses disk logs.
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

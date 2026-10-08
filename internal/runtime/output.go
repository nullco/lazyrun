package runtime

import "sync"

// memoryOutput is bounded proof-of-concept retention, not the M4 disk logstore.
// It retains raw bytes (including partial lines); terminal rendering must sanitize
// them later. No slow client participates in the capture write path.
type memoryOutput struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func (b *memoryOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.data == nil {
		b.data = make([]byte, 0, b.limit)
	}
	n := len(p)
	if n >= b.limit {
		b.truncated = b.truncated || len(b.data) > 0 || n > b.limit
		b.data = append(b.data[:0], p[n-b.limit:]...)
	} else {
		if extra := len(b.data) + n - b.limit; extra > 0 {
			copy(b.data, b.data[extra:])
			b.data = b.data[:len(b.data)-extra]
			b.truncated = true
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}

func (b *memoryOutput) snapshot() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data...), b.truncated
}

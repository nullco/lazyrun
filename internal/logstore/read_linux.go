//go:build linux

package logstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"sort"
	"time"

	"golang.org/x/sys/unix"
	"lazyrun/internal/model"
)

// Restore rebuilds a bounded index once at supervisor startup. Reconnecting
// clients only read requested payloads, never scan whole log files.
func (s *Store) Restore(alias, runID string, maxBytes int64, end uint64, problem string) *Output {
	o := output(s, runID, maxBytes)
	o.end = end
	o.closing = true
	o.problem = problem
	close(o.done)
	if maxBytes <= 0 {
		o.unavailable = true
		return o
	} // pre-disk-log metadata
	d, err := s.directory(alias, false)
	if err != nil {
		o.unavailable = true
		if !errors.Is(err, unix.ENOENT) {
			o.fail(err)
		}
		return o
	}
	o.dir = d
	found := false
	for i := 0; i < ringFiles; i++ {
		f, err := d.File(fileName(i), unix.O_RDONLY)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			o.fail(err)
			continue
		}
		ok, err := o.load(i, f)
		f.Close()
		found = found || ok
		if err != nil {
			o.fail(err)
		}
	}
	o.unavailable = !found
	// Headers/records must not overlap between slots, even after partial rotation.
	var last uint64
	for _, idx := range o.order() {
		for _, e := range o.segments[idx].entries {
			if e.cursor < last {
				o.fail(errors.New("overlapping log records"))
				o.unavailable = true
				return o
			}
			last = e.cursor + uint64(e.size)
		}
	}
	return o
}

func (o *Output) load(idx int, f *os.File) (bool, error) {
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if info.Size() == 0 {
		return false, nil
	}
	if info.Size() < headerBytes {
		return false, errors.New("invalid log segment size")
	}
	header := make([]byte, headerBytes)
	if _, err := f.ReadAt(header, 0); err != nil {
		return false, err
	}
	if string(header[:8]) != magic || binary.BigEndian.Uint32(header[56:]) != crc32.ChecksumIEEE(header[:56]) {
		return false, errors.New("invalid log segment header")
	}
	hash := sha256.Sum256([]byte(o.runID))
	if !bytes.Equal(header[8:40], hash[:]) {
		return false, nil
	} // never serve a different run
	if info.Size() > o.segmentBytes || binary.BigEndian.Uint64(header[40:48]) != uint64(o.maxBytes) || idx >= o.count {
		return false, errors.New("log segment budget mismatch")
	}
	seg := &o.segments[idx]
	seg.start = binary.BigEndian.Uint64(header[48:56])
	if seg.start > o.end {
		o.end = seg.start
	}
	seg.used = headerBytes
	last := seg.start
	for seg.used < info.Size() {
		if len(seg.entries) >= maxRecords || info.Size()-seg.used < recordBytes {
			return true, errors.New("incomplete or excessive log records")
		}
		b := make([]byte, recordBytes)
		if _, err := f.ReadAt(b, seg.used); err != nil {
			return true, err
		}
		cursor := binary.BigEndian.Uint64(b[:8])
		size := int(binary.BigEndian.Uint32(b[16:20]))
		if size < 1 || size > ChunkBytes || int64(size) > info.Size()-seg.used-recordBytes || cursor < last || cursor+uint64(size) < cursor {
			return true, errors.New("invalid or incomplete log record")
		}
		data := make([]byte, size)
		if _, err := f.ReadAt(data, seg.used+recordBytes); err != nil {
			return true, err
		}
		checksum := crc32.Update(crc32.ChecksumIEEE(b[:20]), crc32.IEEETable, data)
		if binary.BigEndian.Uint32(b[20:24]) != checksum {
			return true, errors.New("log record checksum mismatch")
		}
		at := time.Unix(0, int64(binary.BigEndian.Uint64(b[8:16]))).UTC()
		seg.entries = append(seg.entries, entry{cursor: cursor, at: at, offset: seg.used + recordBytes, size: size})
		seg.used += recordBytes + int64(size)
		last = cursor + uint64(size)
		if last > o.end {
			o.end = last
		}
	}
	return true, nil
}

func (o *Output) order() []int {
	order := make([]int, 0, ringFiles)
	for i, s := range o.segments {
		if len(s.entries) > 0 {
			order = append(order, i)
		}
	}
	sort.Slice(order, func(i, j int) bool {
		return o.segments[order[i]].entries[0].cursor < o.segments[order[j]].entries[0].cursor
	})
	return order
}

func (o *Output) End() uint64 { o.disk.Lock(); defer o.disk.Unlock(); return o.end }

func (o *Output) Read(after uint64, limit int) (model.LogRead, error) {
	o.disk.Lock()
	defer o.disk.Unlock()
	return o.read(after, limit)
}

func (o *Output) read(after uint64, limit int) (model.LogRead, error) {
	if limit < 1 || limit > MaxRead {
		return model.LogRead{}, ErrCursor
	}
	result := model.LogRead{RunID: o.runID, Next: after, Error: o.Error(), Unavailable: o.unavailable || o.released}
	if result.Unavailable {
		return result, nil // no retained stream exists against which to validate after
	}
	if after > o.end {
		return model.LogRead{}, ErrCursor
	}
	for _, idx := range o.order() {
		s := &o.segments[idx]
		var f *os.File
		for _, e := range s.entries {
			end := e.cursor + uint64(e.size)
			if end <= result.Next {
				continue
			}
			if e.cursor > result.Next {
				result.Truncated = true
				result.Next = e.cursor
			}
			if f == nil {
				var err error
				f, err = o.dir.File(fileName(idx), unix.O_RDONLY)
				if err != nil {
					o.fail(err)
					result.Error = o.Error()
					result.Truncated = true
					result.Next = o.end
					return result, nil
				}
			}
			skip := int(result.Next - e.cursor)
			size := min(e.size-skip, limit-len(result.Data))
			data := make([]byte, size)
			if _, err := f.ReadAt(data, e.offset+int64(skip)); err != nil {
				f.Close()
				o.fail(fmt.Errorf("read log record: %w", err))
				result.Error = o.Error()
				result.Truncated = true
				result.Next = o.end
				return result, nil
			}
			result.Records = append(result.Records, model.LogRecord{Cursor: result.Next, Time: e.at, Data: data})
			result.Data = append(result.Data, data...)
			result.Next += uint64(size)
			if len(result.Data) == limit {
				f.Close()
				return result, nil
			}
		}
		if f != nil {
			f.Close()
		}
	}
	if result.Next < o.end {
		result.Truncated = true
		result.Next = o.end
	}
	return result, nil
}

// Tail bounds bytes before counting lines. A partial huge line cannot grow the
// response. Zero lines means all available bytes within the response budget.
func (o *Output) Tail(lines, limit int) (model.LogRead, error) {
	if lines < 0 || limit < 1 || limit > MaxRead {
		return model.LogRead{}, ErrCursor
	}
	o.disk.Lock()
	defer o.disk.Unlock()
	// Count retained bytes, not cursor distance: a large dropped suffix must
	// not hide the last verified output that is still available on disk.
	after := uint64(0)
	remaining := limit
	order := o.order()
	for i := len(order) - 1; i >= 0 && remaining > 0; i-- {
		entries := o.segments[order[i]].entries
		for j := len(entries) - 1; j >= 0 && remaining > 0; j-- {
			e := entries[j]
			take := min(remaining, e.size)
			after = e.cursor + uint64(e.size-take)
			remaining -= take
		}
	}
	r, err := o.read(after, limit)
	if err != nil {
		return r, err
	}
	if !r.Unavailable && r.Next < o.end {
		r.Truncated = true
		r.Next = o.end
	}
	TrimTail(&r, lines)
	return r, nil
}

// TrimTail is shared with the in-memory runtime fixture adapter.
func TrimTail(r *model.LogRead, lines int) {
	if lines <= 0 || len(r.Data) == 0 {
		return
	}
	start := len(r.Data) - 1
	if r.Data[start] == '\n' {
		start--
	}
	count := 0
	skip := 0
	for i := start; i >= 0; i-- {
		if r.Data[i] == '\n' {
			count++
			if count == lines {
				skip = i + 1
				break
			}
		}
	}
	if skip == 0 {
		return
	}
	r.Data = append([]byte(nil), r.Data[skip:]...)
	records := r.Records[:0]
	for _, e := range r.Records {
		if skip >= len(e.Data) {
			skip -= len(e.Data)
			continue
		}
		e.Cursor += uint64(skip)
		e.Data = append([]byte(nil), e.Data[skip:]...)
		skip = 0
		records = append(records, e)
	}
	r.Records = records
}

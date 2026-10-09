//go:build linux

// Package logstore retains only the latest run, in a bounded private file ring.
// Capture never waits for disks or readers: a bounded queue evicts older pending
// chunks on overload. Gaps and errors are explicit, not disguised as full logs.
package logstore

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sync"
	"time"

	"github.com/nullco/lazyrun/internal/securefs"
	"golang.org/x/sys/unix"
)

const (
	MaxRead     = 64 * 1024
	ChunkBytes  = 32 * 1024
	QueueChunks = 8
	ringFiles   = 4
	headerBytes = 60
	recordBytes = 24
	maxRecords  = 1024 // bounds the in-memory index even for tiny writes
	magic       = "LRLOG001"
)

var ErrCursor = errors.New("invalid log cursor or read limit")

type Store struct {
	dir     *securefs.Dir                              // borrowed project state capability
	writeAt func(*os.File, []byte, int64) (int, error) // fault injection in package tests
}

func New(dir *securefs.Dir) *Store { return &Store{dir: dir} }

type chunk struct {
	cursor uint64
	at     time.Time
	data   []byte
}
type entry struct {
	cursor uint64
	at     time.Time
	offset int64
	size   int
}
type segment struct {
	file    *os.File
	entries []entry
	used    int64
	start   uint64
}

type Output struct {
	store        *Store
	runID        string
	maxBytes     int64
	count        int
	segmentBytes int64
	// Queue/error lock is separate from disk/index lock: writes cannot wait on IO.
	mu          sync.Mutex
	queue       []chunk
	total       uint64
	closing     bool
	problem     string
	wake        chan struct{}
	done        chan struct{}
	disk        sync.Mutex
	dir         *securefs.Dir
	segments    [ringFiles]segment
	active      int
	end         uint64 // published cursor, never skips pending output
	unavailable bool
	released    bool
}

func output(s *Store, runID string, maxBytes int64) *Output {
	count := ringFiles
	if maxBytes < int64(count) {
		count = int(maxBytes)
	}
	if count < 1 {
		count = 1
	}
	return &Output{store: s, runID: runID, maxBytes: maxBytes, count: count,
		segmentBytes: maxBytes/int64(count) + headerBytes + recordBytes,
		queue:        make([]chunk, 0, QueueChunks), wake: make(chan struct{}, 1), done: make(chan struct{}), active: -1}
}

func (s *Store) directory(alias string, create bool) (*securefs.Dir, error) {
	logs, err := s.dir.Child("logs", create)
	if err != nil {
		return nil, err
	}
	defer logs.Close()
	return logs.Child(fmt.Sprintf("%x", sha256.Sum256([]byte(alias))), create)
}

// Create returns immediately. Setup/write failures do not prevent execution;
// they are visible through Error and Read while incoming output is still drained.
func (s *Store) Create(alias, runID string, maxBytes int64) *Output {
	o := output(s, runID, maxBytes)
	go o.worker(alias)
	return o
}

func (o *Output) Error() string { o.mu.Lock(); defer o.mu.Unlock(); return o.problem }
func (o *Output) fail(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.problem == "" {
		o.problem = "log retention failed; output may be missing: " + err.Error()
	}
}

// Write always consumes all bytes, including discarded bytes. Cursors count
// captured bytes, not retained bytes, so overload cannot silently close a gap.
func (o *Output) Write(p []byte) (int, error) {
	n := len(p)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closing {
		return 0, errors.New("log output is closed")
	}
	for len(p) > 0 {
		size := min(len(p), ChunkBytes)
		data := p[:size]
		cursor := o.total
		o.total += uint64(size)
		// Tiny configured budgets should not cause thousands of pointless writes.
		if int64(len(data)) > o.maxBytes && o.maxBytes > 0 {
			skip := len(data) - int(o.maxBytes)
			cursor += uint64(skip)
			data = data[skip:]
		}
		if len(o.queue) == QueueChunks {
			copy(o.queue, o.queue[1:])
			o.queue = o.queue[:QueueChunks-1]
			if o.problem == "" {
				o.problem = "log retention failed; output may be missing: disk queue overflow"
			}
		}
		o.queue = append(o.queue, chunk{cursor: cursor, at: time.Now().UTC(), data: append([]byte(nil), data...)})
		p = p[size:]
	}
	select {
	case o.wake <- struct{}{}:
	default:
	}
	return n, nil
}

// Close flushes accepted chunks and fsyncs the files, but keeps the read index.
// Only capture calls Close; clients never participate in this completion path.
func (o *Output) Close() error {
	o.mu.Lock()
	o.closing = true
	o.mu.Unlock()
	select {
	case o.wake <- struct{}{}:
	default:
	}
	<-o.done
	if err := o.Error(); err != "" {
		return errors.New(err)
	}
	return nil
}

// Release is called when a completed latest run is superseded.
func (o *Output) Release() {
	o.Close()
	o.disk.Lock()
	defer o.disk.Unlock()
	o.released = true
	if o.dir != nil {
		o.dir.Close()
		o.dir = nil
	}
}

func fileName(i int) string { return fmt.Sprintf("%d.log", i) }
func (o *Output) write(f *os.File, b []byte, offset int64) error {
	var n int
	var err error
	if o.store.writeAt != nil {
		n, err = o.store.writeAt(f, b, offset)
	} else {
		n, err = f.WriteAt(b, offset)
	}
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	return err
}

func (o *Output) setup(alias string) error {
	if o.maxBytes < 1 {
		return errors.New("invalid log byte budget")
	}
	d, err := o.store.directory(alias, true)
	if err != nil {
		return err
	}
	o.dir = d
	// Validate all slots before clearing any old run; never touch links/public files.
	for i := 0; i < ringFiles; i++ {
		if _, err := d.Stat(fileName(i), unix.S_IFREG); err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
	}
	for i := 0; i < ringFiles; i++ {
		f, err := d.File(fileName(i), unix.O_RDWR|unix.O_CREAT)
		if err != nil {
			return err
		}
		o.segments[i].file = f
		if err := f.Truncate(0); err != nil {
			return err
		}
	}
	return o.rotate(0) // even a silent successful run has a durable log identity
}

func (o *Output) rotate(cursor uint64) error {
	o.active = (o.active + 1) % o.count
	s := &o.segments[o.active]
	if err := s.file.Truncate(0); err != nil {
		return err
	}
	s.entries = nil
	s.used = 0
	s.start = cursor
	b := make([]byte, headerBytes)
	copy(b, magic)
	hash := sha256.Sum256([]byte(o.runID))
	copy(b[8:40], hash[:])
	binary.BigEndian.PutUint64(b[40:48], uint64(o.maxBytes))
	binary.BigEndian.PutUint64(b[48:56], cursor)
	binary.BigEndian.PutUint32(b[56:60], crc32.ChecksumIEEE(b[:56]))
	if err := o.write(s.file, b, 0); err != nil {
		return err
	}
	s.used = headerBytes
	return nil
}

func (o *Output) append(c chunk) error {
	for len(c.data) > 0 {
		if o.active < 0 || len(o.segments[o.active].entries) >= maxRecords || o.segmentBytes-o.segments[o.active].used <= recordBytes {
			if err := o.rotate(c.cursor); err != nil {
				return err
			}
		}
		s := &o.segments[o.active]
		size := min(len(c.data), int(min(int64(ChunkBytes), o.segmentBytes-s.used-recordBytes)))
		b := make([]byte, recordBytes+size)
		binary.BigEndian.PutUint64(b[:8], c.cursor)
		binary.BigEndian.PutUint64(b[8:16], uint64(c.at.UnixNano()))
		binary.BigEndian.PutUint32(b[16:20], uint32(size))
		copy(b[recordBytes:], c.data[:size])
		checksum := crc32.Update(crc32.ChecksumIEEE(b[:20]), crc32.IEEETable, b[recordBytes:])
		binary.BigEndian.PutUint32(b[20:24], checksum)
		if err := o.write(s.file, b, s.used); err != nil {
			return err
		}
		s.entries = append(s.entries, entry{cursor: c.cursor, at: c.at, offset: s.used + recordBytes, size: size})
		s.used += int64(len(b))
		c.cursor += uint64(size)
		c.data = c.data[size:]
		o.end = c.cursor
	}
	return nil
}

func (o *Output) worker(alias string) {
	defer close(o.done)
	o.disk.Lock()
	err := o.setup(alias)
	o.disk.Unlock()
	if err != nil {
		o.fail(err)
	}
	for {
		o.mu.Lock()
		if len(o.queue) == 0 {
			closing := o.closing
			total := o.total
			o.mu.Unlock()
			if closing {
				o.disk.Lock()
				o.end = total // includes explicitly dropped trailing bytes
				for i := range o.segments {
					f := o.segments[i].file
					if f != nil {
						if e := f.Sync(); e != nil {
							o.fail(e)
						}
						if e := f.Close(); e != nil {
							o.fail(e)
						}
						o.segments[i].file = nil
					}
				}
				if o.dir != nil {
					if e := o.dir.Sync(); e != nil {
						o.fail(e)
					}
				}
				o.disk.Unlock()
				if err != nil {
					o.fail(err)
				}
				return
			}
			<-o.wake
			continue
		}
		c := o.queue[0]
		copy(o.queue, o.queue[1:])
		o.queue[len(o.queue)-1] = chunk{}
		o.queue = o.queue[:len(o.queue)-1]
		o.mu.Unlock()
		o.disk.Lock()
		if err == nil {
			err = o.append(c)
		}
		if err != nil {
			o.end = c.cursor + uint64(len(c.data))
		}
		o.disk.Unlock()
		if err != nil {
			o.fail(err)
		}
	}
}

//go:build linux

package logstore

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"lazyrun/internal/securefs"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := securefs.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dir.Close() })
	return New(dir)
}
func create(t *testing.T, s *Store, run string, budget int64) *Output {
	t.Helper()
	o := s.Create("alias", run, budget)
	t.Cleanup(o.Release)
	return o
}
func put(t *testing.T, o *Output, data []byte) {
	t.Helper()
	if n, err := o.Write(data); err != nil || n != len(data) {
		t.Fatal("capture did not drain", n, err)
	}
}
func finish(t *testing.T, o *Output) {
	t.Helper()
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
}
func waitEnd(t *testing.T, o *Output, end uint64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if o.End() >= end {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("disk worker did not publish output")
}

func TestRoundTripRecordsPartialReadsAndInvalidBytes(t *testing.T) {
	s := testStore(t)
	o := create(t, s, "run-one", 1<<20)
	data := []byte("partial\x00\xff\xfe\x1b[31m line\nfinal-no-newline")
	put(t, o, data[:7])
	put(t, o, data[7:])
	finish(t, o)
	var got []byte
	var cursor uint64
	for {
		r, err := o.Read(cursor, 5)
		if err != nil || r.Truncated || r.Error != "" || r.Unavailable || len(r.Data) > 5 {
			t.Fatal(r, err)
		}
		var records []byte
		for _, record := range r.Records {
			if record.Cursor != cursor+uint64(len(records)) || record.Time.IsZero() {
				t.Fatal(record)
			}
			records = append(records, record.Data...)
		}
		if !bytes.Equal(records, r.Data) {
			t.Fatal("record/data disagreement")
		}
		got = append(got, r.Data...)
		if r.Next == cursor {
			break
		}
		cursor = r.Next
	}
	if !bytes.Equal(got, data) {
		t.Fatal(got)
	}
	restored := s.Restore("alias", "run-one", 1<<20, o.End(), "")
	t.Cleanup(restored.Release)
	r, err := restored.Read(0, MaxRead)
	if err != nil || r.Unavailable || r.Error != "" || !bytes.Equal(r.Data, data) || r.Next != uint64(len(data)) {
		t.Fatal(r, err)
	}
	wrong := s.Restore("alias", "different-run", 1<<20, 0, "")
	t.Cleanup(wrong.Release)
	r, err = wrong.Read(0, MaxRead)
	if err != nil || !r.Unavailable || len(r.Data) != 0 {
		t.Fatal("served another run", r, err)
	}
	for _, args := range []struct {
		after uint64
		limit int
	}{{uint64(len(data) + 1), 1}, {0, 0}, {0, MaxRead + 1}, {0, -1}} {
		if _, err := o.Read(args.after, args.limit); !errors.Is(err, ErrCursor) {
			t.Fatal("invalid cursor accepted", err)
		}
	}
}

func TestRotationIsBoundedEvenForTinyBudgetsAndHugeLines(t *testing.T) {
	for _, budget := range []int64{1, 2, 3, 4, 31, 84, 1024, 12000} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			s := testStore(t)
			o := create(t, s, "rotate", budget)
			// A single huge line and final bytes; no line-count-only retention.
			data := append(bytes.Repeat([]byte{'x'}, 3*ChunkBytes), []byte("END")...)
			put(t, o, data)
			finish(t, o)
			r, err := o.Read(0, MaxRead)
			if err != nil || !r.Truncated || len(r.Data) > int(budget) || r.Next != uint64(len(data)) {
				t.Fatal(r, err)
			}
			suffix := []byte("END")
			if budget < 3 {
				suffix = suffix[3-int(budget):]
			}
			if !bytes.HasSuffix(r.Data, suffix) {
				t.Fatal("lost final bytes", r)
			}
			var diskBytes int64
			for i := 0; i < ringFiles; i++ {
				st, err := os.Stat(o.dir.ProcPath(fileName(i)))
				if err != nil {
					t.Fatal(err)
				}
				diskBytes += st.Size()
			}
			if diskBytes > budget+ringFiles*(headerBytes+recordBytes) {
				t.Fatal("disk budget exceeded", diskBytes)
			}
			restored := s.Restore("alias", "rotate", budget, o.End(), "")
			t.Cleanup(restored.Release)
			again, err := restored.Read(0, MaxRead)
			if err != nil || !bytes.Equal(r.Data, again.Data) || again.Next != r.Next || !again.Truncated {
				t.Fatal(again, err)
			}
		})
	}
}

func TestQueueOverflowDoesNotBlockCaptureAndKeepsNewestBytes(t *testing.T) {
	s := testStore(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s.writeAt = func(f *os.File, b []byte, offset int64) (int, error) {
		once.Do(func() { close(entered); <-release })
		return f.WriteAt(b, offset)
	}
	o := create(t, s, "overload", MaxRead)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not enter disk write")
	}
	// Always unblock even if an assertion fails, before Release waits for the worker.
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	data := append(bytes.Repeat([]byte("z"), 20*ChunkBytes), []byte("END")...)
	drained := make(chan struct{})
	go func() { _, _ = o.Write(data); close(drained) }()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("capture blocked on disk")
	}
	if !strings.Contains(o.Error(), "queue overflow") {
		t.Fatal("silent overload", o.Error())
	}
	if len(o.queue) > QueueChunks {
		t.Fatal("unbounded queue")
	}
	close(release)
	if err := o.Close(); err == nil {
		t.Fatal("overload was not visible")
	}
	r, err := o.Read(0, MaxRead)
	if err != nil || r.Error == "" || !r.Truncated || r.Next != uint64(len(data)) || !bytes.HasSuffix(r.Data, []byte("END")) {
		t.Fatal(r, err)
	}
}

func TestDiskFailuresStillDrainAndExposeTrailingLoss(t *testing.T) {
	s := testStore(t)
	s.writeAt = func(f *os.File, b []byte, offset int64) (int, error) {
		if offset >= headerBytes && len(b) > recordBytes && binaryCursor(b) > 0 {
			return 0, unix.ENOSPC
		}
		return f.WriteAt(b, offset)
	}
	o := create(t, s, "full-disk", 1<<20)
	put(t, o, []byte("kept"))
	waitEnd(t, o, 4)
	put(t, o, []byte("discarded"))
	put(t, o, bytes.Repeat([]byte{'x'}, 3*ChunkBytes))
	if err := o.Close(); err == nil || !strings.Contains(err.Error(), "no space") {
		t.Fatal(err)
	}
	r, err := o.Read(0, MaxRead)
	if err != nil || string(r.Data) != "kept" || !r.Truncated || r.Error == "" || r.Next != 4+9+3*ChunkBytes {
		t.Fatal(r, err)
	}
	restored := s.Restore("alias", "full-disk", 1<<20, o.End(), o.Error())
	t.Cleanup(restored.Release)
	r, err = restored.Read(0, MaxRead)
	if err != nil || string(r.Data) != "kept" || !r.Truncated || r.Error == "" || r.Next != o.End() {
		t.Fatal(r, err)
	}
	for _, output := range []*Output{o, restored} {
		for _, limit := range []int{4, MaxRead} {
			r, err := output.Tail(0, limit)
			if err != nil || string(r.Data) != "kept" || !r.Truncated || r.Error == "" || r.Next != o.End() {
				t.Fatal("large trailing loss hid the retained tail", r, err)
			}
		}
	}
}

func binaryCursor(b []byte) uint64 {
	var n uint64
	for _, v := range b[:8] {
		n = n<<8 | uint64(v)
	}
	return n
}

func TestShortWriteIsReported(t *testing.T) {
	s := testStore(t)
	s.writeAt = func(f *os.File, b []byte, offset int64) (int, error) {
		n, err := f.WriteAt(b[:len(b)-1], offset)
		return n, err
	}
	o := create(t, s, "short", 1000)
	put(t, o, []byte("still-drained"))
	if err := o.Close(); err == nil || !strings.Contains(err.Error(), io.ErrShortWrite.Error()) {
		t.Fatal(err)
	}
	r, err := o.Read(0, MaxRead)
	if err != nil || r.Error == "" || !r.Truncated || r.Next != 13 {
		t.Fatal(r, err)
	}
}

func TestTailCountsLinesAfterByteBounding(t *testing.T) {
	s := testStore(t)
	o := create(t, s, "tail", 1<<20)
	data := []byte(strings.Repeat("line\n", 10000) + "partial")
	put(t, o, data)
	finish(t, o)
	r, err := o.Tail(3, MaxRead)
	if err != nil || string(r.Data) != "line\nline\npartial" || r.Next != uint64(len(data)) {
		t.Fatal(r, err)
	}
	var records []byte
	for _, e := range r.Records {
		records = append(records, e.Data...)
	}
	if !bytes.Equal(records, r.Data) {
		t.Fatal("tail records include discarded lines")
	}
	r, err = o.Tail(0, 17)
	if err != nil || !bytes.Equal(r.Data, data[len(data)-17:]) {
		t.Fatal(r, err)
	}
	if _, err := o.Tail(-1, MaxRead); !errors.Is(err, ErrCursor) {
		t.Fatal(err)
	}
}

func TestEmptyRunAndLatestRunReplacement(t *testing.T) {
	s := testStore(t)
	first := create(t, s, "first", 4096)
	finish(t, first)
	restored := s.Restore("alias", "first", 4096, 0, "")
	t.Cleanup(restored.Release)
	r, err := restored.Read(0, MaxRead)
	if err != nil || r.Unavailable || r.Error != "" || len(r.Data) != 0 {
		t.Fatal(r, err)
	}
	first.Release()
	restored.Release()
	second := create(t, s, "second", 4096)
	put(t, second, []byte("new"))
	finish(t, second)
	old := s.Restore("alias", "first", 4096, 0, "")
	t.Cleanup(old.Release)
	r, err = old.Read(0, MaxRead)
	if err != nil || !r.Unavailable || len(r.Data) != 0 {
		t.Fatal("old run exposed new output", r, err)
	}
}

func TestTornAndCorruptRecordsReturnOnlyVerifiedPrefix(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			s := testStore(t)
			o := create(t, s, "damaged", 4096)
			put(t, o, []byte("first"))
			waitEnd(t, o, 5)
			put(t, o, []byte("second"))
			finish(t, o)
			name := o.dir.ProcPath("0.log")
			f, err := os.OpenFile(name, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			if corrupt {
				_, err = f.WriteAt([]byte{'!'}, headerBytes+recordBytes+5+recordBytes)
			} else {
				err = f.Truncate(headerBytes + recordBytes + 5 + recordBytes + 2)
			}
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			restored := s.Restore("alias", "damaged", 4096, o.End(), "")
			t.Cleanup(restored.Release)
			r, err := restored.Read(0, MaxRead)
			if err != nil || r.Error == "" || string(r.Data) != "first" || !r.Truncated || r.Next != 11 {
				t.Fatal(r, err)
			}
		})
	}
}

func TestUnsafePathsFailWithoutTouchingExternalFiles(t *testing.T) {
	for _, kind := range []string{"directory-symlink", "file-symlink", "hardlink", "permissions"} {
		t.Run(kind, func(t *testing.T) {
			s := testStore(t)
			outside := filepath.Join(t.TempDir(), "untouched")
			if err := os.WriteFile(outside, []byte("safe"), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "directory-symlink" {
				if err := os.Symlink(filepath.Dir(outside), filepath.Join(s.dir.Path, "logs")); err != nil {
					t.Fatal(err)
				}
			} else {
				d, err := s.directory("alias", true)
				if err != nil {
					t.Fatal(err)
				}
				d.Close()
				target := filepath.Join(s.dir.Path, "logs", fmt.Sprintf("%x", sha256.Sum256([]byte("alias"))), "0.log")
				switch kind {
				case "file-symlink":
					err = os.Symlink(outside, target)
				case "hardlink":
					err = os.Link(outside, target)
				case "permissions":
					err = os.WriteFile(target, []byte("public"), 0644)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			o := create(t, s, "unsafe", 1000)
			put(t, o, []byte("drained"))
			if err := o.Close(); err == nil {
				t.Fatal("accepted unsafe logs")
			}
			b, err := os.ReadFile(outside)
			if err != nil || string(b) != "safe" {
				t.Fatal("modified outside file", string(b), err)
			}
		})
	}
}

func TestIndexBoundForTinyWrites(t *testing.T) {
	s := testStore(t)
	o := create(t, s, "tiny-writes", 1<<20)
	for i := 0; i < maxRecords+2; i++ {
		put(t, o, []byte{'x'})
		waitEnd(t, o, uint64(i+1))
	}
	finish(t, o)
	total := 0
	for _, segment := range o.segments {
		if len(segment.entries) > maxRecords {
			t.Fatal("unbounded index")
		}
		total += len(segment.entries)
	}
	if total != maxRecords+2 {
		t.Fatal("premature rotation", total)
	}
}

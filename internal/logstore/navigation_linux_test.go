//go:build linux

package logstore

import (
	"bytes"
	"testing"
)

func TestWindowSeeksRetainedBytesAcrossGapsAndRestoration(t *testing.T) {
	s := testStore(t)
	o := create(t, s, "window", 1024)
	put(t, o, []byte("abcdefghij"))
	waitEnd(t, o, 10)
	o.mu.Lock()
	o.total = 20
	o.mu.Unlock() // explicit capture loss, not ten retained bytes
	put(t, o, []byte("klmnop"))
	finish(t, o)
	for _, out := range []*Output{o, s.Restore("alias", "window", 1024, 26, "")} {
		if out != o {
			t.Cleanup(out.Release)
		}
		r, err := out.Window(22, 7, 10)
		if err != nil || string(r.Data) != "fghijklmno" || r.First != 0 || r.End != 26 || r.Next != 25 || !r.Truncated {
			t.Fatal(r, err)
		}
		r, err = out.Window(0, 7, 10)
		if err != nil || string(r.Data) != "abcdefghij" {
			t.Fatal(r, err)
		}
		r, err = out.Window(26, 6, 6)
		if err != nil || string(r.Data) != "klmnop" {
			t.Fatal(r, err)
		}
		for _, args := range [][3]int{{27, 0, 4}, {0, -1, 4}, {0, 5, 4}, {0, 0, MaxRead + 1}} {
			if _, err = out.Window(uint64(args[0]), args[1], args[2]); err != ErrCursor {
				t.Fatal(args, err)
			}
		}
	}
}
func TestWindowReportsEarliestRetainedAndNeverReturnsAnotherRun(t *testing.T) {
	s := testStore(t)
	o := create(t, s, "old", 100)
	data := bytes.Repeat([]byte("1234567890"), 100)
	put(t, o, data)
	finish(t, o)
	r, err := o.Window(0, 0, MaxRead)
	if err != nil || r.First == 0 || len(r.Records) == 0 || r.Records[0].Cursor != r.First || r.End != uint64(len(data)) || !r.Truncated {
		t.Fatal(r, err)
	}
	o.Release()
	r, err = o.Window(0, 0, MaxRead)
	if err != nil || !r.Unavailable || len(r.Data) != 0 {
		t.Fatal(r, err)
	}
	next := s.Create("alias", "new", 100)
	t.Cleanup(next.Release)
	put(t, next, []byte("NEW"))
	finish(t, next)
	r, err = o.Window(0, 0, MaxRead)
	if err != nil || len(r.Data) != 0 {
		t.Fatal("released output read replacement", r, err)
	}
}
func TestEmptyWindowHasStableBounds(t *testing.T) {
	o := create(t, testStore(t), "silent", 100)
	finish(t, o)
	r, err := o.Window(0, 32, 64)
	if err != nil || r.First != 0 || r.End != 0 || r.Next != 0 || r.Unavailable || len(r.Data) != 0 {
		t.Fatal(r, err)
	}
}

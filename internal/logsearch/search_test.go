package logsearch

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func source(data []byte, chunk int) func(uint64, int) (Read, error) {
	return func(after uint64, limit int) (Read, error) {
		end := min(uint64(len(data)), after+uint64(min(limit, chunk)))
		var records []Record
		if end > after {
			records = []Record{{Cursor: after, Data: data[after:end]}}
		}
		return Read{Records: records, Next: end, End: uint64(len(data))}, nil
	}
}
func all(t *testing.T, req Request, read func(uint64, int) (Read, error)) []Match {
	t.Helper()
	var matches []Match
	for range 100000 {
		r, err := Scan(req, read)
		if err != nil {
			t.Fatal(err)
		}
		if r.Scanned > ScanBytes || len(r.Matches) > MaxMatches {
			t.Fatal("unbounded response", r)
		}
		matches = append(matches, r.Matches...)
		if r.Done {
			return matches
		}
		req.State = r.State
	}
	t.Fatal("search did not finish")
	return nil
}
func TestLiteralSearchCarriesAcrossANSIUTF8RecordsAndPages(t *testing.T) {
	data := []byte("secret\x1b]0;HIDDEN\a\nxx ca\x1b[31mfé\x1b[0m yy café\r\n\xff end")
	first := uint64(bytes.Index(data, []byte("ca")))
	second := uint64(bytes.LastIndex(data, []byte("café")))
	lineStart := uint64(bytes.Index(data, []byte("xx ")))
	expected := []Match{{Cursor: first, End: uint64(bytes.Index(data, []byte("\x1b[0m"))), LineStart: lineStart, Column: 3}, {Cursor: second, End: second + 5, LineStart: lineStart, Column: 11}}
	for chunk := 1; chunk <= len(data); chunk++ {
		got := all(t, Request{Query: "café"}, source(data, chunk))
		if !reflect.DeepEqual(got, expected) {
			t.Fatal(chunk, got, expected)
		}
	}
	if got := all(t, Request{Query: "HIDDEN"}, source(data, 1)); len(got) != 0 {
		t.Fatal("escape string was searched", got)
	}
	if got := all(t, Request{Query: "�"}, source(data, 1)); len(got) != 1 || got[0].Cursor != uint64(bytes.IndexByte(data, 255)) {
		t.Fatal(got)
	}
}
func TestSearchIsBoundedResumableAndKeepsOverlappingMatches(t *testing.T) {
	data := []byte(strings.Repeat("ab", 10000))
	got := all(t, Request{Query: "aba"}, source(data, 64*1024))
	if len(got) != 9999 {
		t.Fatal(len(got))
	}
	for i, m := range got {
		if m != (Match{Cursor: uint64(i * 2), End: uint64(i*2 + 3), Column: uint64(i * 2)}) {
			t.Fatal(i, m)
		}
	}
	data = bytes.Repeat([]byte("z"), ScanBytes*3+1)
	r, err := Scan(Request{Query: "absent"}, source(data, 64*1024))
	if err != nil || r.Done || r.Scanned != ScanBytes || r.State.Offset != ScanBytes {
		t.Fatal(r, err)
	}
	if got := all(t, Request{Query: "absent", State: r.State}, source(data, 64*1024)); len(got) != 0 {
		t.Fatal(got)
	}
}
func TestLastSearchIncludesOverlapsAndSnapshotsItsEnd(t *testing.T) {
	data := []byte("banana")
	before, cutoff := uint64(6), uint64(3)
	got := all(t, Request{Query: "ana", Before: &before, MatchBefore: &cutoff, Last: true}, source(data, 1))
	if !reflect.DeepEqual(got, []Match{{Cursor: 1, End: 4, Column: 1}}) {
		t.Fatal(got)
	}
	data = bytes.Repeat([]byte("x"), ScanBytes+4)
	r, err := Scan(Request{Query: "new"}, source(data, 64*1024))
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("new")...)
	got = all(t, Request{Query: "new", State: r.State}, source(data, 64*1024))
	if len(got) != 0 {
		t.Fatal("snapshot searched newly appended output", got)
	}
}
func TestSearchResetsOnLossAndRejectsInvalidContinuations(t *testing.T) {
	read := func(after uint64, _ int) (Read, error) {
		if after == 0 {
			return Read{Records: []Record{{0, []byte("foo")}, {10, []byte("bar")}}, Next: 13, End: 13}, nil
		}
		return Read{Next: 13, End: 13}, nil
	}
	r, err := Scan(Request{Query: "foobar"}, read)
	if err != nil || len(r.Matches) != 0 || !r.State.Gaps || !r.Done {
		t.Fatal(r, err)
	}
	for _, q := range []string{"", strings.Repeat("a", MaxQuery+1), "bad\n", "bad\x1b", "\xff", "\u202e"} {
		if _, err := Scan(Request{Query: q}, source(nil, 1)); err != ErrInvalid {
			t.Fatal(q, err)
		}
	}
	base := State{Query: "xx", End: 20, Ring: make([]uint64, 2), Columns: make([]uint64, 2)}
	for _, mutate := range []func(*State){func(s *State) { s.Head = 2 }, func(s *State) { s.Matched = 2 }, func(s *State) { s.Filter.Mode = 'z' }, func(s *State) { s.Filter.Pending = []byte{0xe2} }, func(s *State) { s.Ring = []uint64{99, 0} }, func(s *State) { s.Query = "yy" }} {
		s := base
		mutate(&s)
		if _, err := Scan(Request{Query: "xx", State: &s}, source(nil, 1)); err != ErrInvalid {
			t.Fatal(s, err)
		}
	}
}
func TestSearchColumnsAndTabExpansionStayBounded(t *testing.T) {
	data := []byte("\x1b[31m界a\u0301 needle\n")
	matches := all(t, Request{Query: "needle"}, source(data, 1))
	if len(matches) != 1 || matches[0].LineStart != 0 || matches[0].Column != 4 {
		t.Fatal(matches)
	}
	data = bytes.Repeat([]byte("\t"), 1000)
	matches = all(t, Request{Query: " "}, source(data, 64*1024))
	if len(matches) != 4000 {
		t.Fatal("lost tab matches at a response boundary", len(matches))
	}
}

func BenchmarkSearchPlain8MiB(b *testing.B) {
	data := bytes.Repeat([]byte("a log line with nothing unusual\n"), 8*1024*1024/32)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for range b.N {
		req := Request{Query: "missing needle"}
		for {
			r, err := Scan(req, source(data, 64*1024))
			if err != nil {
				b.Fatal(err)
			}
			if r.Done {
				break
			}
			req.State = r.State
		}
	}
}
func FuzzSearchContinuations(f *testing.F) {
	f.Add([]byte("abc\x1b[31mabc"), "abc", uint8(3))
	f.Fuzz(func(t *testing.T, data []byte, query string, size uint8) {
		if len(data) > 10000 || !ValidQuery(query) {
			return
		}
		one := all(t, Request{Query: query}, source(data, len(data)+1))
		split := all(t, Request{Query: query}, source(data, int(size)+1))
		if !reflect.DeepEqual(one, split) {
			t.Fatal(fmt.Sprint(one), fmt.Sprint(split))
		}
	})
}

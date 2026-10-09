// Package logsearch implements bounded, resumable literal searches of terminal
// text. Continuations live with the requesting client, never in supervisor jobs.
package logsearch

import (
	"errors"
	"unicode"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

const (
	MaxQuery     = 256
	ScanBytes    = 256 * 1024
	MaxMatches   = 64
	batchMatches = MaxMatches - 3 // a tab emits up to four searchable spaces
)

var ErrInvalid = errors.New("invalid log search query or continuation")
var ErrUnavailable = errors.New("logs unavailable for search")

type Match struct {
	Cursor    uint64 `json:"cursor"`
	End       uint64 `json:"end"`
	LineStart uint64 `json:"lineStart"`
	Column    uint64 `json:"column"`
}
type Filter struct {
	Mode    byte   `json:"mode"`
	CR      bool   `json:"cr"`
	Pending []byte `json:"pending,omitempty"`
}
type State struct {
	Query     string   `json:"query"`
	Offset    uint64   `json:"offset"`
	End       uint64   `json:"end"`
	Matched   int      `json:"matched"`
	Ring      []uint64 `json:"ring"`
	Columns   []uint64 `json:"columns"`
	LineStart uint64   `json:"lineStart"`
	Column    uint64   `json:"column"`
	Head      int      `json:"head"`
	Filter    Filter   `json:"filter"`
	Last      *Match   `json:"last,omitempty"`
	Gaps      bool     `json:"gaps"`
}
type Request struct {
	Query       string  `json:"query"`
	After       uint64  `json:"after,omitempty"`
	Before      *uint64 `json:"before,omitempty"`
	MatchBefore *uint64 `json:"matchBefore,omitempty"`
	Last        bool    `json:"last,omitempty"`
	State       *State  `json:"state,omitempty"`
}
type Result struct {
	RunID   string  `json:"runId"`
	State   *State  `json:"state"`
	Matches []Match `json:"matches,omitempty"`
	Done    bool    `json:"done"`
	First   uint64  `json:"first"`
	Scanned int     `json:"scanned"`
	Error   string  `json:"error,omitempty"`
}
type Record struct {
	Cursor uint64
	Data   []byte
}
type Read struct {
	Records          []Record
	Next, First, End uint64
	Unavailable      bool
	Error            string
}

func ValidQuery(q string) bool {
	if len(q) == 0 || len(q) > MaxQuery || !utf8.ValidString(q) {
		return false
	}
	for _, r := range q {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}
func valid(s *State, q string) bool {
	if s.Query != q || s.Offset > s.End || s.Matched < 0 || s.Matched >= len(q) || len(s.Ring) != len(q) || len(s.Columns) != len(q) || s.LineStart > s.Offset || s.Head < 0 || s.Head >= len(q) || len(s.Filter.Pending) > 3 || uint64(len(s.Filter.Pending)) > s.Offset || len(s.Filter.Pending) > 0 && utf8.FullRune(s.Filter.Pending) {
		return false
	}
	switch s.Filter.Mode {
	case 0, 'e', 'c', 'o', 't':
	default:
		return false
	}
	for _, p := range s.Ring {
		if p > s.Offset {
			return false
		}
	}
	if s.Last != nil && (s.Last.Cursor >= s.Last.End || s.Last.End > s.Offset) {
		return false
	}
	return true
}

// Scan does at most ScanBytes of work per call and yields at MaxMatches. Literal
// KMP matching is linear even for adversarial repeating input. Raw positions are
// retained across UTF-8, ANSI, record, and IPC boundaries; gaps reset the parser.
func Scan(req Request, read func(uint64, int) (Read, error)) (Result, error) {
	if !ValidQuery(req.Query) {
		return Result{}, ErrInvalid
	}
	var s State
	if req.State != nil {
		if !valid(req.State, req.Query) {
			return Result{}, ErrInvalid
		}
		s = *req.State
		s.Ring = append([]uint64(nil), s.Ring...)
		s.Columns = append([]uint64(nil), s.Columns...)
		s.Filter.Pending = append([]byte(nil), s.Filter.Pending...)
	} else {
		s = State{Query: req.Query, Offset: req.After, LineStart: req.After, Ring: make([]uint64, len(req.Query)), Columns: make([]uint64, len(req.Query))}
	}
	result := Result{State: &s}
	prefix := make([]int, len(req.Query))
	for i, j := 1, 0; i < len(prefix); i++ {
		for j > 0 && req.Query[i] != req.Query[j] {
			j = prefix[j-1]
		}
		if req.Query[i] == req.Query[j] {
			j++
		}
		prefix[i] = j
	}
	emit := func(r rune, start, end uint64) {
		var buf [4]byte
		n := utf8.EncodeRune(buf[:], r)
		for _, b := range buf[:n] {
			s.Ring[s.Head] = start
			s.Columns[s.Head] = s.Column
			s.Head = (s.Head + 1) % len(s.Ring)
			for s.Matched > 0 && b != req.Query[s.Matched] {
				s.Matched = prefix[s.Matched-1]
			}
			if b == req.Query[s.Matched] {
				s.Matched++
			}
			if s.Matched == len(req.Query) {
				m := Match{Cursor: s.Ring[s.Head], End: end, LineStart: s.LineStart, Column: s.Columns[s.Head]}
				if req.MatchBefore == nil || m.Cursor < *req.MatchBefore {
					s.Last = &m
				}
				if !req.Last && (len(result.Matches) == 0 || result.Matches[len(result.Matches)-1] != m) {
					result.Matches = append(result.Matches, m)
				}
				s.Matched = prefix[s.Matched-1]
			}
		}
		if r == '\n' {
			s.LineStart = end
			s.Column = 0
		} else if r >= 32 && r < 127 {
			s.Column++
		} else {
			s.Column += uint64(max(0, runewidth.RuneWidth(r)))
		}
	}
	for result.Scanned < ScanBytes && len(result.Matches) < batchMatches {
		r, err := read(s.Offset, min(64*1024, ScanBytes-result.Scanned))
		if err != nil {
			return result, err
		}
		if r.Unavailable {
			return result, ErrUnavailable
		}
		result.First = r.First
		result.Error = r.Error
		if req.State == nil && result.Scanned == 0 {
			s.End = r.End
			if req.Before != nil {
				if *req.Before > s.End {
					return result, ErrInvalid
				}
				s.End = *req.Before
			}
			if s.Offset > s.End {
				return result, ErrInvalid
			}
		}
		for _, record := range r.Records {
			if record.Cursor >= s.End {
				break
			}
			if record.Cursor != s.Offset {
				s.Filter = Filter{}
				s.Matched = 0
				s.Gaps = true
				s.Offset = record.Cursor
				s.LineStart = record.Cursor
				s.Column = 0
			}
			for _, b := range record.Data {
				if s.Offset >= s.End || result.Scanned == ScanBytes || len(result.Matches) >= batchMatches {
					break
				}
				at := s.Offset
				s.Offset++
				result.Scanned++
				s.Filter.byte(b, at, emit)
			}
			if s.Offset >= s.End || result.Scanned == ScanBytes || len(result.Matches) >= batchMatches {
				break
			}
		}
		if len(result.Matches) >= batchMatches || result.Scanned == ScanBytes {
			break
		}
		if s.Offset < min(r.Next, s.End) {
			s.Filter = Filter{}
			s.Matched = 0
			s.Gaps = true
			s.Offset = min(r.Next, s.End)
			s.LineStart = s.Offset
			s.Column = 0
		}
		if s.Offset >= s.End {
			if len(s.Filter.Pending) > 0 {
				emit(utf8.RuneError, s.Offset-uint64(len(s.Filter.Pending)), s.Offset)
				s.Filter.Pending = nil
			}
			result.Done = true
			if req.Last && s.Last != nil {
				result.Matches = []Match{*s.Last}
			}
			break
		}
		if len(r.Records) == 0 && r.Next <= s.Offset {
			return result, ErrUnavailable
		}
	}
	return result, nil
}

func (f *Filter) byte(b byte, at uint64, emit func(rune, uint64, uint64)) {
	if b < utf8.RuneSelf && len(f.Pending) == 0 {
		f.rune(rune(b), at, at+1, emit)
		return
	}
	f.Pending = append(f.Pending, b)
	for utf8.FullRune(f.Pending) {
		r, n := utf8.DecodeRune(f.Pending)
		start := at + 1 - uint64(len(f.Pending))
		f.Pending = f.Pending[n:]
		f.rune(r, start, start+uint64(n), emit)
	}
}
func (f *Filter) rune(r rune, start, end uint64, emit func(rune, uint64, uint64)) {
	cr := f.CR
	f.CR = false
	switch f.Mode {
	case 'e':
		switch r {
		case '[':
			f.Mode = 'c'
		case ']', 'P', 'X', '^', '_':
			f.Mode = 'o'
		case 0x1b:
		default:
			f.Mode = 0
		}
		return
	case 'c':
		if r == 0x1b {
			f.Mode = 'e'
		} else if r >= 0x40 && r <= 0x7e {
			f.Mode = 0
		}
		return
	case 'o':
		if r == '\a' || r == 0x9c {
			f.Mode = 0
		} else if r == 0x1b {
			f.Mode = 't'
		}
		return
	case 't':
		if r == '\\' || r == '\a' || r == 0x9c {
			f.Mode = 0
		} else if r != 0x1b {
			f.Mode = 'o'
		}
		return
	}
	switch r {
	case 0x1b:
		f.Mode = 'e'
	case 0x9b:
		f.Mode = 'c'
	case 0x90, 0x98, 0x9d, 0x9e, 0x9f:
		f.Mode = 'o'
	case '\n':
		if !cr {
			emit('\n', start, end)
		}
	case '\r':
		emit('\n', start, end)
		f.CR = true
	case '\t':
		for range 4 {
			emit(' ', start, end)
		}
	default:
		if r >= 32 && r < 127 || !unicode.IsControl(r) && !unicode.Is(unicode.Cf, r) && r != '\u2028' && r != '\u2029' {
			emit(r, start, end)
		}
	}
}

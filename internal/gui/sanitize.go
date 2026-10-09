package gui

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Sanitizer is a bounded streaming terminal filter. Only validated SGR styling
// reaches gocui. OSC (including clipboard/title/hyperlinks), DCS, cursor/erase
// instructions, C0/C1 controls and bidi formatting never reach the renderer.
// Escape strings are discarded without buffering; CSI and UTF-8 carry are bounded.
type Sanitizer struct {
	state   byte
	csi     []byte
	pending []byte
	cr      bool
}

func (s *Sanitizer) Reset() { *s = Sanitizer{} }

type lineBreak struct{ next uint64 }

func (s *Sanitizer) Feed(data []byte) string {
	text, _ := s.feedAt(data, 0, false)
	return text
}
func (s *Sanitizer) feedAt(data []byte, cursor uint64, track bool) (string, []lineBreak) {
	if track && uint64(len(s.pending)) <= cursor {
		cursor -= uint64(len(s.pending))
	}
	data = append(s.pending, data...)
	s.pending = nil
	var out strings.Builder
	var breaks []lineBreak
	for len(data) > 0 {
		if !utf8.FullRune(data) {
			s.pending = append([]byte(nil), data...)
			break
		}
		r, n := utf8.DecodeRune(data)
		data = data[n:]
		cursor += uint64(n)
		before := out.Len()
		s.rune(r, &out)
		if track && (r == '\n' || r == '\r') && out.Len() > before {
			breaks = append(breaks, lineBreak{next: cursor})
		}
	}
	return out.String(), breaks
}

func (s *Sanitizer) Finish() string {
	text := ""
	if len(s.pending) > 0 {
		text = "\ufffd"
	}
	s.Reset()
	return text
}

func (s *Sanitizer) rune(r rune, out *strings.Builder) {
	wasCR := s.cr
	s.cr = false
	switch s.state {
	case 'e':
		switch r {
		case '[':
			s.state = 'c'
			s.csi = nil
		case ']', 'P', 'X', '^', '_':
			s.state = 'o'
		case 0x1b: // stay in escape state
		default:
			s.state = 0
		}
		return
	case 'o':
		if r == '\a' || r == 0x9c {
			s.state = 0
		} else if r == 0x1b {
			s.state = 't'
		}
		return
	case 't':
		if r == '\\' || r == '\a' || r == 0x9c {
			s.state = 0
		} else if r != 0x1b {
			s.state = 'o'
		}
		return
	case 'c', 'd':
		if r == 0x1b {
			s.state = 'e'
			s.csi = nil
			return
		}
		if r >= 0x40 && r <= 0x7e {
			if s.state == 'c' && r == 'm' && validSGR(string(s.csi)) {
				out.WriteString("\x1b[")
				out.Write(s.csi)
				out.WriteByte('m')
			}
			s.state = 0
			s.csi = nil
		} else if s.state == 'c' {
			if len(s.csi) == 128 || !((r >= '0' && r <= '9') || r == ';') {
				s.state = 'd'
				s.csi = nil
			} else {
				s.csi = append(s.csi, byte(r))
			}
		}
		return
	}
	switch r {
	case 0x1b:
		s.state = 'e'
	case 0x9b:
		s.state = 'c'
		s.csi = nil
	case 0x90, 0x98, 0x9d, 0x9e, 0x9f:
		s.state = 'o'
	case '\n':
		if !wasCR {
			out.WriteByte('\n')
		}
	case '\r':
		out.WriteByte('\n') // never allow carriage-return overwriting
		s.cr = true
	case '\t':
		out.WriteString("    ")
	default:
		if !unicode.IsControl(r) && !unicode.Is(unicode.Cf, r) && r != '\u2028' && r != '\u2029' {
			out.WriteRune(r)
		}
	}
}

func sgrParams(s string) []int {
	if s == "" {
		return []int{0}
	}
	parts := strings.Split(s, ";")
	if len(parts) > 20 {
		return nil
	}
	values := make([]int, len(parts))
	for i, part := range parts {
		if part == "" || len(part) > 3 {
			return nil
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 255 {
			return nil
		}
		values[i] = n
	}
	return values
}
func validSGR(s string) bool {
	values := sgrParams(s)
	if values == nil {
		return false
	}
	for i := 0; i < len(values); i++ {
		n := values[i]
		switch {
		case n == 38 || n == 48:
			if i+2 < len(values) && values[i+1] == 5 {
				i += 2
			} else if i+4 < len(values) && values[i+1] == 2 {
				i += 4
			} else {
				return false
			}
		case n == 0, n >= 1 && n <= 9, n >= 21 && n <= 29, n >= 30 && n <= 37, n == 39, n >= 40 && n <= 47, n == 49, n >= 90 && n <= 97, n >= 100 && n <= 107:
		default:
			return false
		}
	}
	return true
}

// plain is also used for configuration, paths, errors and aliases: untrusted
// terminal text is not restricted to managed-command output.
func plain(s string) string {
	var filter Sanitizer
	t := filter.Feed([]byte(s)) + filter.Finish()
	var out strings.Builder
	for len(t) > 0 {
		if strings.HasPrefix(t, "\x1b[") {
			i := strings.IndexByte(t, 'm')
			t = t[i+1:]
			continue
		}
		r, n := utf8.DecodeRuneInString(t)
		out.WriteRune(r)
		t = t[n:]
	}
	return out.String()
}
func singleLine(s string) string { return strings.ReplaceAll(plain(s), "\n", " ") }

// style canonicalizes active styling so skipped/evicted lines do not lose
// colors or accumulate unbounded chains of SGR prefixes.
type style struct {
	fg, bg  string
	effects [10]bool
}

func (s *style) apply(params string) {
	v := sgrParams(params)
	for i := 0; i < len(v); i++ {
		n := v[i]
		switch {
		case n == 0:
			*s = style{}
		case n >= 1 && n <= 9:
			s.effects[n] = true
		case n >= 21 && n <= 29:
			s.effects[n-20] = false
		case n == 39:
			s.fg = ""
		case n == 49:
			s.bg = ""
		case n == 38 || n == 48:
			size := 3
			if v[i+1] == 2 {
				size = 5
			}
			parts := make([]string, size)
			for j := range size {
				parts[j] = strconv.Itoa(v[i+j])
			}
			value := strings.Join(parts, ";")
			if n == 38 {
				s.fg = value
			} else {
				s.bg = value
			}
			i += size - 1
		case n >= 30 && n <= 37 || n >= 90 && n <= 97:
			s.fg = strconv.Itoa(n)
		case n >= 40 && n <= 47 || n >= 100 && n <= 107:
			s.bg = strconv.Itoa(n)
		}
	}
}
func (s style) sequence() string {
	parts := []string{"0"}
	for n := 1; n <= 9; n++ {
		if s.effects[n] {
			parts = append(parts, strconv.Itoa(n))
		}
	}
	if s.fg != "" {
		parts = append(parts, s.fg)
	}
	if s.bg != "" {
		parts = append(parts, s.bg)
	}
	return "\x1b[" + strings.Join(parts, ";") + "m"
}

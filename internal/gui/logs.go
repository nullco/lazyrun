package gui

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
	"github.com/nullco/lazyrun/internal/model"
)

const (
	MaxBufferBytes = 2 * 1024 * 1024
	MaxBufferLines = 10000
)

type logLine struct {
	text, prefix string
	cursor       uint64
}
type logBuffer struct {
	lines      []logLine
	bytes      int
	filter     Sanitizer
	style      style
	evicted    bool
	timestamps bool
	rawNext    uint64
	rawChunk   uint64
}

func (b *logBuffer) reset(timestamps bool) { *b = logBuffer{timestamps: timestamps} }

// append retains sanitized bytes, not a potentially unbounded gocui buffer.
// Returns the number of old logical lines evicted, for paused scroll anchoring.
func (b *logBuffer) append(data []byte, at time.Time) int {
	return b.appendAt(data, at, b.rawNext)
}
func (b *logBuffer) appendAt(data []byte, at time.Time, cursor uint64) int {
	b.rawChunk = cursor
	b.rawNext = cursor + uint64(len(data))
	text, breaks := b.filter.feedAt(data, cursor, true)
	breakIndex := 0
	for len(text) > 0 {
		if len(b.lines) == 0 {
			b.newLine(at, cursor)
		}
		i := strings.IndexByte(text, '\n')
		part := text
		if i >= 0 {
			part = text[:i]
		}
		b.scanStyle(part)
		last := &b.lines[len(b.lines)-1]
		if last.text == "" && b.timestamps && !at.IsZero() {
			stamp := at.UTC().Format("2006-01-02T15:04:05.000Z ")
			last.text = stamp
			b.bytes += len(stamp)
		}
		last.text += part
		b.bytes += len(part)
		if i < 0 {
			break
		}
		b.newLine(time.Time{}, breaks[breakIndex].next)
		breakIndex++
		text = text[i+1:]
	}
	return b.trim()
}
func (b *logBuffer) scanStyle(text string) {
	for {
		i := strings.Index(text, "\x1b[")
		if i < 0 {
			return
		}
		text = text[i+2:]
		end := strings.IndexByte(text, 'm')
		b.style.apply(text[:end])
		text = text[end+1:]
	}
}
func (b *logBuffer) newLine(at time.Time, cursor uint64) {
	prefix := b.style.sequence()
	text := ""
	if b.timestamps && !at.IsZero() {
		text = at.UTC().Format("2006-01-02T15:04:05.000Z ")
	}
	b.lines = append(b.lines, logLine{text: text, prefix: prefix, cursor: cursor})
	b.bytes += len(prefix) + len(text) + 1
}
func (b *logBuffer) marker(text string) int { return b.markerAt(text, b.rawNext) }
func (b *logBuffer) markerAt(text string, cursor uint64) int {
	b.rawNext = cursor
	b.filter.Reset()
	b.style = style{}
	if len(b.lines) > 0 {
		last := &b.lines[len(b.lines)-1]
		if last.text != "" {
			b.newLine(time.Time{}, b.rawNext)
		} else {
			prefix := b.style.sequence()
			b.bytes += len(prefix) - len(last.prefix)
			last.prefix = prefix
			last.cursor = cursor
		}
	}
	dropped := b.append([]byte("["+text+"]\n"), time.Time{})
	b.rawNext = cursor
	b.rawChunk = cursor
	if len(b.lines) > 0 {
		b.lines[len(b.lines)-1].cursor = cursor
	}
	return dropped
}
func (b *logBuffer) trim() int {
	dropped := 0
	for len(b.lines)-dropped > 1 && (b.bytes > MaxBufferBytes || len(b.lines)-dropped > MaxBufferLines) {
		line := b.lines[dropped]
		b.bytes -= len(line.text) + len(line.prefix) + 1
		dropped++
	}
	if dropped > 0 {
		copy(b.lines, b.lines[dropped:])
		for i := len(b.lines) - dropped; i < len(b.lines); i++ {
			b.lines[i] = logLine{}
		}
		b.lines = b.lines[:len(b.lines)-dropped]
		b.evicted = true
	}
	if b.bytes > MaxBufferBytes {
		line := &b.lines[0]
		// A single huge partial line is byte-bounded too. Remove styling before
		// cutting so neither UTF-8 nor an SGR sequence is split by eviction.
		text := plain(line.text)
		limit := MaxBufferBytes - len(line.prefix) - 1
		if len(text) > limit {
			start := len(text) - limit
			for start < len(text) && !utf8.RuneStart(text[start]) {
				start++
			}
			text = text[start:]
		}
		line.text = strings.Clone(text)
		line.cursor = b.rawChunk // approximate anchor for a byte-bounded huge fragment
		b.bytes = len(text) + len(line.prefix) + 1
		b.evicted = true
	}
	return dropped
}

// consume uses per-record cursors to mark mid-response and trailing gaps. A gap
// resets parser/style carry, so a lost escape-string terminator cannot hide the
// next verified output. Intentional initial tail selection is not a loss.
func (b *logBuffer) consume(r model.LogRead, after uint64, initial bool) int {
	dropped := 0
	expected := after
	if initial && len(r.Records) > 0 {
		expected = r.Records[0].Cursor
	}
	if initial && r.Truncated {
		dropped += b.markerAt("output truncated or dropped", max(after, r.First))
	}
	for _, record := range r.Records {
		if record.Cursor != expected {
			dropped += b.markerAt("output truncated or dropped", record.Cursor)
		}
		dropped += b.appendAt(record.Data, record.Time, record.Cursor)
		expected = record.Cursor + uint64(len(record.Data))
	}
	if len(r.Records) == 0 && len(r.Data) > 0 {
		if r.Truncated {
			dropped += b.markerAt("output truncated or dropped", r.Next-uint64(len(r.Data)))
		}
		dropped += b.appendAt(r.Data, time.Time{}, r.Next-uint64(len(r.Data)))
		expected += uint64(len(r.Data))
	}
	if r.Next > expected {
		dropped += b.markerAt("output truncated or dropped", r.Next)
	}
	return dropped
}

// crop writes only the visible cells to gocui. Even a multi-megabyte line never
// becomes a multi-megabyte cell array, and horizontal scrolling preserves SGR.
func crop(text, prefix string, offset, width int) string {
	if width <= 0 {
		return ""
	}
	var current style
	if strings.HasPrefix(prefix, "\x1b[") {
		current.apply(prefix[2 : len(prefix)-1])
	}
	var out strings.Builder
	column := 0
	started := false
	combining := 0
	lastStyle := ""
	writeStyle := func() {
		sequence := current.sequence()
		if !started || sequence != lastStyle {
			out.WriteString(sequence)
			lastStyle = sequence
		}
		started = true
	}
	for len(text) > 0 {
		if strings.HasPrefix(text, "\x1b[") {
			i := strings.IndexByte(text, 'm')
			current.apply(text[2:i])
			// Canonicalize styling only when a visible rune is emitted. A run
			// of millions of color changes must not enter the view buffer.
			text = text[i+1:]
			continue
		}
		r, n := utf8.DecodeRuneInString(text)
		text = text[n:]
		w := runewidth.RuneWidth(r)
		if w == 0 {
			// gocui stores a cell per rune, including zero-width marks. Keep
			// normal combining accents, but cap pathological mark clusters.
			if started && column > offset && combining < 4 {
				writeStyle()
				out.WriteRune(r)
				combining++
			}
			continue
		}
		combining = 0
		if column+w > offset+width {
			break
		}
		if column >= offset {
			writeStyle()
			out.WriteRune(r)
		} else if column+w > offset { // clipped left half of a wide character
			writeStyle()
			out.WriteString(strings.Repeat(" ", column+w-offset))
		}
		column += w
	}
	if started {
		out.WriteString("\x1b[0m")
	}
	return out.String()
}

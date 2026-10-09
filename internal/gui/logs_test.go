package gui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nullco/lazyrun/internal/model"
)

func bufferText(b *logBuffer) string {
	var lines []string
	for _, line := range b.lines {
		lines = append(lines, plain(line.text))
	}
	return strings.Join(lines, "\n")
}
func TestLogBufferPartialLinesColorsTimestampsAndGaps(t *testing.T) {
	var b logBuffer
	b.reset(true)
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	b.consume(model.LogRead{RunID: "one", Next: 6, Records: []model.LogRecord{{Cursor: 0, Time: at, Data: []byte("\x1b[31mA")}}}, 0, true)
	b.consume(model.LogRead{RunID: "one", Next: 9, Records: []model.LogRecord{{Cursor: 6, Time: at, Data: []byte("BC\n")}}}, 6, false)
	if !strings.Contains(bufferText(&b), "2026-01-02T03:04:05.000Z ABC") || strings.Contains(bufferText(&b), "dropped") {
		t.Fatal(bufferText(&b))
	}
	if !strings.Contains(b.lines[1].prefix, "31") {
		t.Fatal("color lost across lines")
	}
	b.consume(model.LogRead{RunID: "one", Next: 24, Truncated: true, Records: []model.LogRecord{{Cursor: 20, Time: at, Data: []byte("end\n")}}}, 9, false)
	if !strings.Contains(bufferText(&b), "[output truncated or dropped]") || !strings.Contains(bufferText(&b), "end") {
		t.Fatal(bufferText(&b))
	}
	b.append([]byte("\x1b]unfinished"), at)
	b.consume(model.LogRead{Next: 31, Truncated: true, Records: []model.LogRecord{{Cursor: 30, Time: at, Data: []byte("Z")}}}, 24, false)
	if !strings.HasSuffix(bufferText(&b), "Z") {
		t.Fatal("lost control-string terminator swallowed newer output", bufferText(&b))
	}
}
func TestLogBufferIndependentByteAndLineBounds(t *testing.T) {
	var b logBuffer
	for range 40 {
		b.append([]byte(strings.Repeat("€", 32768)), time.Time{})
		if b.bytes > MaxBufferBytes {
			t.Fatal(b.bytes)
		}
	}
	if !b.evicted || len(b.lines) != 1 || !strings.Contains(bufferText(&b), "€") {
		t.Fatal("huge partial line not retained/bounded")
	}
	checkSafe(t, b.lines[0].text)
	b.reset(false)
	b.append([]byte(strings.Repeat("line\n", MaxBufferLines+100)), time.Time{})
	if len(b.lines) > MaxBufferLines || b.bytes > MaxBufferBytes || !b.evicted {
		t.Fatal(len(b.lines), b.bytes)
	}
	b.reset(false)
	if len(b.lines) != 0 || b.evicted || b.bytes != 0 {
		t.Fatal("new run inherited buffer")
	}
}
func TestViewportBoundsZeroWidthMarksAndStyleOnlyFloods(t *testing.T) {
	for _, text := range []string{
		strings.Repeat("\u0301", 100000),
		"a" + strings.Repeat("\u0301", 100000),
		"a" + strings.Repeat("\x1b[31m\x1b[32m", 100000) + "b",
	} {
		got := crop(text, "", 0, 20)
		checkSafe(t, got)
		if utf8.RuneCountInString(plain(got)) > 20*5 || len(got) > 20*128 {
			t.Fatal("off-screen cells/styles leaked into viewport", len(got))
		}
	}
	if got := plain(crop("e\u0301", "", 0, 20)); got != "e\u0301" {
		t.Fatal("normal accent lost", got)
	}
}

func TestViewportCroppingPreservesColorsAndWideCharacters(t *testing.T) {
	for _, test := range []struct {
		text, prefix  string
		offset, width int
		want          string
	}{
		{"abc", "", 0, 2, "ab"}, {"abcdef", "", 3, 2, "de"},
		{"界ab", "", 1, 3, " ab"}, {"界ab", "", 0, 1, ""},
		{"\x1b[31mred\x1b[32mgreen", "", 3, 5, "green"},
		{"abcdef", "\x1b[1;38;2;1;2;3m", 2, 2, "cd"},
	} {
		got := crop(test.text, test.prefix, test.offset, test.width)
		checkSafe(t, got)
		if plain(got) != test.want {
			t.Fatalf("%q != %q", got, test.want)
		}
	}
	got := crop("\x1b[31mred\x1b[32mgreen", "", 3, 5)
	if !strings.HasPrefix(got, "\x1b[0;32m") {
		t.Fatal("skipped styling was lost", got)
	}
}

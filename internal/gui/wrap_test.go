package gui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jesseduffield/gocui"
	"github.com/mattn/go-runewidth"
	"github.com/nullco/lazyrun/internal/model"
)

func TestWrapRowsPreserveTextColorsAndWideCombiningCharacters(t *testing.T) {
	for _, test := range []struct {
		text  string
		width int
		want  []string
	}{
		{"abcdefghij", 4, []string{"abcd", "efgh", "ij"}},
		{"abcd", 4, []string{"abcd"}},
		{"", 4, []string{""}},
		{"ab界c", 3, []string{"ab", "界c"}},
		{"e\u0301界a", 3, []string{"e\u0301界", "a"}},
		{"\x1b[31mabcd\x1b[32mefgh", 3, []string{"abc", "def", "gh"}},
	} {
		line := logLine{text: test.text}
		w := line.wrapped(test.width)
		got := w.visible(0, w.rows, "")
		if len(got) != len(test.want) {
			t.Fatal(test.text, len(got), test.want)
		}
		for i, row := range got {
			checkSafe(t, row.text)
			if plain(row.text) != test.want[i] || runewidth.StringWidth(plain(row.text)) > test.width {
				t.Fatal(test.text, i, row.text, test.want)
			}
		}
	}
	line := logLine{text: "\x1b[38;2;1;2;3mabcdef"}
	rows := line.wrapped(2).visible(1, 2, "")
	for _, row := range rows {
		if !strings.HasPrefix(row.text, "\x1b[0;38;2;1;2;3m") {
			t.Fatal("style lost across soft wrap", row.text)
		}
	}
}

func TestWrapSparseIndexAndViewportRemainBounded(t *testing.T) {
	line := logLine{text: strings.Repeat("x", MaxBufferBytes-1)}
	w := line.wrapped(41)
	if len(w.points) != (w.rows+wrapStride-1)/wrapStride || len(w.points) > 1000 {
		t.Fatal("expanded huge line into per-row storage", len(w.points))
	}
	for _, row := range []int{0, 63, 64, 129, w.rows - 1} {
		p := w.point(row)
		if p.row != row || p.start != row*41 || p.column != row*41 || w.rowAt(p.column) != row {
			t.Fatal(row, p)
		}
	}
	visible := w.visible(w.rows-10, 10, "xxx")
	if len(visible) != 10 {
		t.Fatal(len(visible))
	}
	for _, row := range visible {
		checkSafe(t, row.text)
		if len(row.text) > 41*128 || utf8.RuneCountInString(plain(row.text)) > 41*5 {
			t.Fatal("off-screen cells entered viewport", len(row.text))
		}
	}
	for _, text := range []string{
		strings.Repeat("\u0301", 10000),
		"a" + strings.Repeat("\u0301", 10000) + "b",
		strings.Repeat("\x1b[31m\x1b[32m", 10000) + "abc",
	} {
		line := logLine{text: text}
		for _, row := range line.wrapped(41).visible(0, 10, "") {
			if len(row.text) > 41*128 || utf8.RuneCountInString(plain(row.text)) > 41*5 {
				t.Fatal("combining/style flood entered viewport", len(row.text))
			}
		}
	}
}

func TestWrappedViewportScrollFollowResizeAndPartialAppend(t *testing.T) {
	d := navigationDashboard(t)
	d.logWidth, d.logHeight = 4, 2
	d.buffer.append([]byte("abcdefghijklmnopqrst"), time.Time{})
	d.follow = true
	rows := d.visibleLogs()
	if d.top != 0 || d.wrapTop != 3 || plain(rows[0].text) != "mnop" || plain(rows[1].text) != "qrst" {
		t.Fatal("follow did not show last wrapped rows", d.logPosition(), rows)
	}
	d.scrollLogs(-1)
	rows = d.visibleLogs()
	if d.follow || d.wrapTop != 2 || plain(rows[0].text) != "ijkl" {
		t.Fatal("scroll skipped a logical line instead of one visual row", d.logPosition(), rows)
	}
	d.buffer.append([]byte("uvwxyz"), time.Time{})
	rows = d.visibleLogs()
	if d.wrapTop != 2 || plain(rows[0].text) != "ijkl" {
		t.Fatal("paused partial append lost row anchor", rows)
	}
	d.resizeLogs(3)
	rows = d.visibleLogs()
	if d.wrapTop != 2 || plain(rows[0].text) != "ghi" {
		t.Fatal("resize did not retain the visible column", d.logPosition(), rows)
	}
	d.scrollHorizontal(10)
	if d.horizontal != 0 {
		t.Fatal("horizontal scrolling hid wrapped logs")
	}
	d.tab = 1
	d.scrollHorizontal(10)
	if d.horizontal != 10 {
		t.Fatal("Details lost horizontal scrolling")
	}
	d.tab = 0
	d.goFollow()
	rows = d.visibleLogs()
	if d.wrapTop != 7 || plain(rows[1].text) != "yz" {
		t.Fatal("G did not follow wrapped partial tail", d.logPosition(), rows)
	}
}

func TestWrappedHistoryEdgesAndSearchJump(t *testing.T) {
	d := navigationDashboard(t)
	d.logWidth, d.logHeight = 4, 2
	d.buffer.appendAt([]byte("abcdefghijklmnopqrst"), time.Time{}, 100)
	d.history, d.follow = true, false
	d.streamFirst, d.streamEnd, d.cursor = 0, 200, 120
	d.wrapTop = 2
	d.scrollLogs(-1)
	if d.windowBusy || d.wrapTop != 1 {
		t.Fatal("requested history before scrolling within the wrapped line")
	}
	d.scrollLogs(-2)
	job := <-d.jobs
	if !d.windowBusy || job.after != 100 {
		t.Fatal("wrapped top did not page older output", job)
	}
	d.windowBusy = false
	d.wrapTop = 3
	d.scrollLogs(1)
	job = <-d.jobs
	if !d.windowBusy || job.after != 120 {
		t.Fatal("wrapped bottom did not page newer output", job)
	}
	d.matches = []model.LogMatch{{Cursor: 10, End: 16, LineStart: 0, Column: 10}}
	d.matchIndex = 0
	d.searchQuery = "needle"
	d.jumpMatch()
	job = <-d.jobs
	data := []byte("abcdefghijneedle" + strings.Repeat("x", 40))
	d.consumeWindow(event{anchor: job.after, read: &model.LogRead{RunID: "run", Next: uint64(len(data)), End: uint64(len(data)), Records: []model.LogRecord{{Data: data}}}})
	rows := d.visibleLogs()
	if d.wrapTop != 2 || plain(rows[0].text) != "ijne" || plain(rows[1].text) != "edle" {
		t.Fatal("search did not reveal wrapped match", d.logPosition(), rows)
	}
	if !strings.Contains(rows[0].text, "\x1b[1;30;43mne") || !strings.Contains(rows[1].text, "\x1b[1;30;43medle") {
		t.Fatal("match spanning a soft wrap was not highlighted", rows)
	}
}

func TestWrappedWindowAnchorsWithinPartialLines(t *testing.T) {
	for _, delta := range []int{-1, 1} {
		d := navigationDashboard(t)
		d.logWidth, d.logHeight = 4, 2
		data := []byte("\x1b[31mabcdefghij" + strings.Repeat("z", 40))
		read := model.LogRead{RunID: "run", First: 0, End: 2000, Next: 1000 + uint64(len(data)), Records: []model.LogRecord{{Cursor: 1000, Data: data}}}
		d.consumeWindow(event{anchor: 1015, delta: delta, read: &read})
		if d.top != 0 || d.wrapTop != 2+delta {
			t.Fatal("partial-line page anchored to line start, not requested byte", delta, d.logPosition())
		}
	}
}

func TestWrappedPausedAnchorSurvivesLogicalLineEviction(t *testing.T) {
	d := navigationDashboard(t)
	d.logWidth, d.logHeight = 4, 2
	// Leave space for new output to evict exactly one logical line.
	d.buffer.append([]byte("old\n"+strings.Repeat("x", 40)+"\n"+strings.Repeat("row\n", MaxBufferLines-3)), time.Time{})
	d.loaded, d.follow = true, false
	d.top, d.wrapTop = 1, 5
	cursor := d.buffer.rawNext
	d.events <- event{generation: d.generation, after: cursor, read: &model.LogRead{RunID: "run", Next: cursor + 4, Records: []model.LogRecord{{Cursor: cursor, Data: []byte("new\n")}}}}
	d.drain()
	if d.top != 0 || d.wrapTop != 5 || plain(d.visibleLogs()[0].text) != "xxxx" {
		t.Fatal("eviction lost paused wrapped row", d.logPosition())
	}
}

func TestHeadlessLogPaneWrapsWithoutRetainingOffscreenCells(t *testing.T) {
	g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: MinWidth, Height: MinHeight, OutputMode: gocui.OutputTrue, SupportOverlaps: true})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	d := navigationDashboard(t)
	d.loaded, d.follow = true, false
	area := responsiveGeometry(MinWidth, MinHeight, d.focus, d.owner).areas["detail"]
	width := area.x1 - area.x0 - 1
	d.buffer.append([]byte(strings.Repeat("x", width)+"VISIBLE-SUFFIX"), time.Time{})
	if err := d.layout(g); err != nil {
		t.Fatal(err)
	}
	v, _ := g.View("detail")
	if v.Wrap || !strings.Contains(v.Buffer(), "VISIBLE-SUFFIX") {
		t.Fatal("log suffix was cropped instead of wrapped", v.Buffer())
	}
	// App rendering supplies only the bounded, already-wrapped viewport.
	d.buffer.reset(false)
	d.buffer.append([]byte(strings.Repeat("x", MaxBufferBytes-1)), time.Time{})
	d.follow = true
	if err := d.layout(g); err != nil {
		t.Fatal(err)
	}
	if len(v.Buffer()) > MinWidth*MinHeight {
		t.Fatal("huge line expanded into gocui cells", len(v.Buffer()))
	}
}

func TestRepeatedLogReflowPreservesColumnUntilScrolling(t *testing.T) {
	d := navigationDashboard(t)
	d.cancelWindow()
	d.loaded, d.follow = true, false
	d.buffer.append([]byte(strings.Repeat("x", 10000)), time.Time{})
	d.logWidth, d.logHeight, d.wrapTop = 64, 4, 5
	for _, width := range []int{38, 43, 48, 64} {
		d.resizeLogs(width)
		column := d.buffer.lines[0].wrapped(width).point(d.wrapTop).column
		if column > 320 || column+width <= 320 {
			t.Fatal("reflow drifted away from the original column", width, column)
		}
		_ = d.visibleLogs()
	}
	if d.wrapTop != 5 {
		t.Fatal("returning to the original width lost the paused row", d.wrapTop)
	}
	d.scrollLogs(1)
	d.resizeLogs(43)
	d.resizeLogs(64)
	if d.wrapTop != 6 {
		t.Fatal("scrolling did not establish a new reflow anchor", d.wrapTop)
	}
}

func FuzzWrappedViewport(f *testing.F) {
	f.Add("abc界e\u0301\x1b[31mdefgh", uint8(4), uint16(1))
	f.Add("a"+strings.Repeat("\u0301", 10)+"bc", uint8(2), uint16(0))
	f.Fuzz(func(t *testing.T, raw string, size uint8, offset uint16) {
		if len(raw) > 2048 {
			return
		}
		var filter Sanitizer
		text := filter.Feed([]byte(raw)) + filter.Finish()
		for _, part := range strings.Split(text, "\n") {
			line := logLine{text: part}
			width := int(size)%79 + 2
			w := line.wrapped(width)
			var all strings.Builder
			for _, row := range w.visible(0, w.rows, "") {
				checkSafe(t, row.text)
				all.WriteString(plain(row.text))
				if runewidth.StringWidth(plain(row.text)) > width || utf8.RuneCountInString(plain(row.text)) > width*5 {
					t.Fatal("unbounded wrapped row", row.text, width)
				}
			}
			if all.String() != plain(crop(part, "", 0, 4096)) {
				t.Fatal("wrapping lost or duplicated text", part, all.String())
			}
			row := int(offset) % w.rows
			if len(w.visible(row, 5, "abc")) > 5 || w.point(row).row != row {
				t.Fatal("invalid sparse seek")
			}
		}
	})
}

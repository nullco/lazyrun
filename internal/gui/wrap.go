package gui

import (
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// Keep sparse byte/style checkpoints, not a string or cell array per wrapped
// row. Even a huge partial line only materializes the visible viewport.
const wrapStride = 64

type wrapPoint struct {
	row, start, column int
	style              style
}
type wrappedLayout struct {
	text, prefix string
	width, rows  int
	points       []wrapPoint
}

func (line *logLine) wrapped(width int) *wrappedLayout {
	width = max(2, width) // supported log panes are wider than a wide rune
	if w := line.wrap; w != nil && w.width == width && w.text == line.text && w.prefix == line.prefix {
		return w
	}
	w := &wrappedLayout{text: line.text, prefix: line.prefix, width: width}
	var initial style
	if strings.HasPrefix(line.prefix, "\x1b[") {
		initial.apply(line.prefix[2 : len(line.prefix)-1])
	}
	w.scan(wrapPoint{style: initial}, func(p wrapPoint, _ int) bool {
		if p.row%wrapStride == 0 {
			w.points = append(w.points, p)
		}
		w.rows = p.row + 1
		return true
	})
	line.wrap = w
	return w
}

// scan breaks before a wide rune that cannot fit; combining marks stay with
// their base. SGR changes consume no columns and carry across row boundaries.
func (w *wrappedLayout) scan(p wrapPoint, visit func(wrapPoint, int) bool) {
	current := p.style
	column := p.column
	for at := p.start; at < len(w.text); {
		if strings.HasPrefix(w.text[at:], "\x1b[") {
			end := at + strings.IndexByte(w.text[at:], 'm')
			current.apply(w.text[at+2 : end])
			at = end + 1
			continue
		}
		r, n := utf8.DecodeRuneInString(w.text[at:])
		cells := max(0, runewidth.RuneWidth(r))
		if cells > 0 && column-p.column+cells > w.width {
			if !visit(p, at) {
				return
			}
			p = wrapPoint{row: p.row + 1, start: at, column: column, style: current}
		}
		column += cells
		at += n
	}
	visit(p, len(w.text))
}
func (w *wrappedLayout) point(row int) wrapPoint {
	row = max(0, min(row, w.rows-1))
	p := w.points[row/wrapStride]
	w.scan(p, func(next wrapPoint, _ int) bool {
		p = next
		return next.row < row
	})
	return p
}
func (w *wrappedLayout) rowAt(column int) int {
	p := w.points[0]
	for _, next := range w.points {
		if next.column > column {
			break
		}
		p = next
	}
	row := p.row
	w.scan(p, func(next wrapPoint, _ int) bool {
		if next.column > column {
			return false
		}
		row = next.row
		return true
	})
	return row
}
func (w *wrappedLayout) visible(row, count int, query string) []logLine {
	// A little adjacent text lets a search spanning a soft wrap highlight both
	// fragments. Never highlight across an actual application newline.
	pad := 0
	if query != "" {
		pad = (len(query)+w.width-2)/(w.width-1) + 1
	}
	start, end := max(0, row-pad), min(w.rows, row+count+pad)
	var lines []logLine
	w.scan(w.point(start), func(p wrapPoint, limit int) bool {
		if p.row >= end {
			return false
		}
		lines = append(lines, logLine{text: crop(w.text[p.start:limit], p.style.sequence(), 0, w.width)})
		return true
	})
	first, last := row-start, min(len(lines), row-start+count)
	if query != "" {
		var text strings.Builder
		offsets := make([]int, len(lines)+1)
		for i := range lines {
			text.WriteString(plain(lines[i].text))
			offsets[i+1] = text.Len()
		}
		joined := text.String()
		for i := first; i < last; i++ {
			left, right := offsets[i], offsets[i+1]
			before := joined[max(0, left-len(query)):left]
			after := joined[right:min(len(joined), right+len(query))]
			lines[i] = highlightSearchContext(lines[i], query, before, after)
		}
	}
	return lines[first:last]
}

// top stays a logical-line index; wrapTop locates the visual row within it.
// This keeps paused anchors stable as new lines arrive or old lines are evicted.
type logPosition struct{ line, row int }

func (d *dashboard) logPosition() logPosition     { return logPosition{d.top, d.wrapTop} }
func (d *dashboard) setLogPosition(p logPosition) { d.top, d.wrapTop = p.line, p.row }
func (d *dashboard) shiftLogPosition(p logPosition, delta int) logPosition {
	if len(d.buffer.lines) == 0 {
		return logPosition{}
	}
	p.line = max(0, min(p.line, len(d.buffer.lines)-1))
	p.row = max(0, min(p.row, d.buffer.lines[p.line].wrapped(d.logWidth).rows-1))
	p.row += delta
	for p.row < 0 && p.line > 0 {
		p.line--
		p.row += d.buffer.lines[p.line].wrapped(d.logWidth).rows
	}
	for {
		rows := d.buffer.lines[p.line].wrapped(d.logWidth).rows
		if p.row < rows || p.line == len(d.buffer.lines)-1 {
			p.row = max(0, min(p.row, rows-1))
			return p
		}
		p.row -= rows
		p.line++
	}
}
func (d *dashboard) logBottom() logPosition {
	if len(d.buffer.lines) == 0 {
		return logPosition{}
	}
	last := len(d.buffer.lines) - 1
	rows := d.buffer.lines[last].wrapped(d.logWidth).rows
	return d.shiftLogPosition(logPosition{last, rows - 1}, 1-max(1, d.logHeight))
}
func afterLogPosition(a, b logPosition) bool {
	return a.line > b.line || a.line == b.line && a.row > b.row
}
func (d *dashboard) resizeLogs(width int) {
	if width == d.logWidth {
		return
	}
	column := 0
	if d.top < len(d.buffer.lines) && d.logWidth > 0 {
		column = d.buffer.lines[d.top].wrapped(d.logWidth).point(d.wrapTop).column
	}
	d.logWidth = width
	if d.top < len(d.buffer.lines) {
		d.wrapTop = d.buffer.lines[d.top].wrapped(width).rowAt(column)
	}
}
func (d *dashboard) visibleLogs() []logLine {
	bottom := d.logBottom()
	if d.follow {
		d.setLogPosition(bottom)
	} else {
		d.setLogPosition(d.shiftLogPosition(d.logPosition(), 0))
		if afterLogPosition(d.logPosition(), bottom) {
			d.setLogPosition(bottom)
		}
	}
	var lines []logLine
	for i := d.top; i < len(d.buffer.lines) && len(lines) < d.logHeight; i++ {
		row := 0
		if i == d.top {
			row = d.wrapTop
		}
		lines = append(lines, d.buffer.lines[i].wrapped(d.logWidth).visible(row, d.logHeight-len(lines), d.searchQuery)...)
	}
	return lines
}

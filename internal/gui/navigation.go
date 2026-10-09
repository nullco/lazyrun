package gui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jesseduffield/gocui"
	"github.com/mattn/go-runewidth"
	"github.com/nullco/lazyrun/internal/logsearch"
	"github.com/nullco/lazyrun/internal/model"
)

// Eight KiB cannot produce more than the 10,000-line UI cap, even if every
// captured byte is a newline. Historical windows must keep their beginning.
const windowBytes = 8 * 1024

type navigation struct {
	history, windowBusy      bool
	logWidth, logHeight      int
	wrapTop                  int
	wrapTarget               *wrapTarget
	streamFirst, streamEnd   uint64
	searchEditing            bool
	searchDraft, searchQuery string
	searchBusy               bool
	searchGeneration         uint64
	searchCancel             context.CancelFunc
	searchJobs               chan searchJob
	searchResume             *logsearch.State
	searchDone               bool
	matches                  []model.LogMatch
	matchIndex               int
	searchOffset             uint64
}
type wrapTarget struct {
	anchor uint64
	column int
}

type searchJob struct {
	ctx          context.Context
	generation   uint64
	alias, runID string
	request      model.LogSearchRequest
}

func (d *dashboard) cancelWindow() {
	if d.viewCancel != nil {
		d.viewCancel()
		d.viewCancel = nil
	}
	d.generation++
	d.jobActive = false
	d.windowBusy = false
	select {
	case <-d.jobs:
	default:
	}
}
func (d *dashboard) cancelSearch() {
	if d.searchCancel != nil {
		d.searchCancel()
		d.searchCancel = nil
	}
	d.searchGeneration++
	d.searchBusy = false
	select {
	case <-d.searchJobs:
	default:
	}
}
func (d *dashboard) clearSearch() {
	d.cancelSearch()
	d.searchQuery = ""
	d.searchDraft = ""
	d.searchEditing = false
	d.matches = nil
	d.matchIndex = -1
	d.searchResume = nil
	d.searchDone = false
	d.searchOffset = 0
}
func (d *dashboard) resetNavigation() {
	d.clearSearch()
	d.history = false
	d.windowBusy = false
	d.streamFirst = 0
	d.streamEnd = 0
	d.wrapTop = 0
	d.wrapTarget = nil
}
func (d *dashboard) queueWindow(anchor uint64, before, delta int) {
	item, ok := d.current()
	if !ok || item.Run.ID == "" || d.tab != 0 || d.small {
		return
	}
	d.cancelWindow()
	d.wrapTarget = nil
	d.history = true
	d.follow = false
	d.windowBusy = true
	ctx, cancel := context.WithCancel(d.ctx)
	d.viewCancel = cancel
	d.jobs <- logJob{ctx: ctx, generation: d.generation, alias: d.logAlias, runID: d.logRun, after: anchor, window: true, before: min(before, windowBytes/2), delta: delta}
}
func (d *dashboard) scrollLogs(delta int) {
	d.follow = false
	if d.windowBusy {
		return
	}
	if delta < 0 && d.top == 0 && d.wrapTop+delta < 0 && len(d.buffer.lines) > 0 {
		anchor := d.buffer.lines[0].cursor
		if anchor > d.streamFirst {
			d.queueWindow(anchor, 32*1024, delta)
			return
		}
	}
	next, bottom := d.shiftLogPosition(d.logPosition(), delta), d.logBottom()
	if d.history && delta > 0 && (afterLogPosition(next, bottom) || !afterLogPosition(bottom, d.logPosition())) && d.cursor < d.streamEnd {
		// Seek from the page end, not the start of its last logical line:
		// a wrapped partial line can occupy the entire page.
		d.queueWindow(d.cursor, windowBytes/2, delta)
		return
	}
	d.setLogPosition(next)
}
func (d *dashboard) scrollHorizontal(delta int) {
	if d.owner == projectPane || d.tab != 0 {
		d.horizontal = max(0, d.horizontal+delta)
	}
}

// Geometry, not a retained gocui view, determines the visible page height.
func (d *dashboard) logViewport() (int, int) { return d.logWidth, max(1, d.logHeight) }
func (d *dashboard) goHome() {
	if !d.logPaneFocused() {
		return
	}
	d.cancelSearch()
	d.horizontal = 0
	d.queueWindow(0, 0, 0)
}
func (d *dashboard) goFollow() {
	if !d.logPaneFocused() {
		return
	}
	d.clearSearch()
	if d.history {
		d.cancelWindow()
		d.history = false
		d.buffer.reset(d.state.Project.Logs.Timestamps)
		d.loaded = false
		d.cursor = 0
	}
	d.follow = true
	d.syncLogView()
}
func (d *dashboard) consumeWindow(e event) {
	d.windowBusy = false
	if e.err != nil {
		d.logError = singleLine(e.err.Error())
		d.notify("Log page: " + d.logError)
		return
	}
	if e.read.RunID != d.logRun {
		return
	}
	d.buffer.reset(d.state.Project.Logs.Timestamps)
	d.buffer.consume(*e.read, 0, true)
	if item, ok := d.current(); ok && item.Run.Outcome != nil && e.read.Next >= item.Run.LogEnd {
		if text := d.buffer.filter.Finish(); text != "" {
			d.buffer.append([]byte(text), time.Time{})
		}
	}
	d.streamFirst, d.streamEnd = e.read.First, e.read.End
	d.cursor = e.read.Next
	d.loaded = true
	d.unavailable = e.read.Unavailable
	d.logError = singleLine(e.read.Error)
	d.top, d.wrapTop = 0, 0
	for i, line := range d.buffer.lines {
		if line.cursor <= e.anchor {
			d.top = i
		} else {
			break
		}
	}
	if d.top < len(d.buffer.lines) {
		column := d.windowColumn(*e.read, e.anchor)
		if target := d.wrapTarget; target != nil && target.anchor == e.anchor {
			column = target.column
		}
		d.wrapTop = d.buffer.lines[d.top].wrapped(d.logWidth).rowAt(column)
	}
	d.wrapTarget = nil
	d.setLogPosition(d.shiftLogPosition(d.logPosition(), e.delta))
	if e.anchor == 0 && e.delta == 0 && d.matchIndex < 0 {
		d.top, d.wrapTop = 0, 0
	}
	if e.read.First > e.anchor {
		d.notify("Original output is no longer retained; showing earliest available output")
	}
}

// A window can begin in the middle of a huge logical line. Sanitize just its
// bounded prefix to locate the requested raw anchor within the wrapped fragment.
func (d *dashboard) windowColumn(read model.LogRead, anchor uint64) int {
	prefix := read
	prefix.Records, prefix.Data = nil, nil
	prefix.Next = min(anchor, read.Next)
	for _, record := range read.Records {
		if record.Cursor >= anchor {
			break
		}
		record.Data = record.Data[:min(uint64(len(record.Data)), anchor-record.Cursor)]
		prefix.Records = append(prefix.Records, record)
	}
	if len(read.Records) == 0 && len(read.Data) > 0 {
		first := read.Next - uint64(len(read.Data))
		if anchor > first {
			prefix.Data = read.Data[:min(uint64(len(read.Data)), anchor-first)]
		}
	}
	var b logBuffer
	b.reset(d.state.Project.Logs.Timestamps)
	b.consume(prefix, 0, true)
	column := 0
	if len(b.lines) > 0 {
		for _, r := range plain(b.lines[len(b.lines)-1].text) {
			column += max(0, runewidth.RuneWidth(r))
		}
	}
	return column
}

// The shared detail view is a Logs pane only when focused on a command's
// Logs tab. Project, command lists and Details have no log-search shortcuts.
func (d *dashboard) logPaneFocused() bool {
	if d.focus != detailPane || d.owner == projectPane || d.tab != 0 || d.help || d.small {
		return false
	}
	item, ok := d.current()
	return ok && item.Run.ID != ""
}
func (d *dashboard) clearFocusedSearch() bool {
	if !d.logPaneFocused() || d.searchQuery == "" {
		return false
	}
	d.clearSearch()
	return true
}
func (d *dashboard) openSearch() {
	if !d.logPaneFocused() {
		return
	}
	d.searchDraft = ""
	d.searchEditing = true
}
func (d *dashboard) searchKey(key any) {
	switch k := key.(type) {
	case rune:
		d.searchRune(k)
	case gocui.Key:
		switch k {
		case gocui.KeyEnter:
			query := d.searchDraft
			d.searchEditing = false
			if !logsearch.ValidQuery(query) {
				d.notify("Search needs 1–256 bytes of literal text")
				return
			}
			d.clearSearch()
			d.searchQuery = query
			d.focus = detailPane
			d.follow = false
			d.requestSearch(model.LogSearchRequest{Query: query})
		case gocui.KeyEsc, gocui.KeyCtrlC:
			d.searchEditing = false
		case gocui.KeyCtrlU:
			d.searchDraft = ""
		case gocui.KeyBackspace, gocui.KeyBackspace2:
			if len(d.searchDraft) > 0 {
				_, n := utf8.DecodeLastRuneInString(d.searchDraft)
				d.searchDraft = d.searchDraft[:len(d.searchDraft)-n]
			}
		}
	}
}
func (d *dashboard) searchRune(r rune) {
	if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' || !utf8.ValidRune(r) {
		return
	}
	if len(d.searchDraft)+utf8.RuneLen(r) <= logsearch.MaxQuery {
		d.searchDraft += string(r)
	}
}

type searchEditor struct {
	d *dashboard
	g *gocui.Gui
}

func (e searchEditor) Edit(_ *gocui.View, key gocui.Key, ch rune, _ gocui.Modifier) bool {
	if ch != 0 {
		e.d.searchRune(ch)
	} else {
		e.d.searchKey(key)
	}
	if !e.d.searchEditing && e.g != nil {
		_, _ = e.g.SetCurrentView(paneNames[e.d.focus])
	}
	return true
}

func (d *dashboard) requestSearch(request model.LogSearchRequest) {
	if !d.logPaneFocused() || d.searchQuery == "" || d.logRun == "" {
		return
	}
	d.cancelSearch()
	d.searchBusy = true
	ctx, cancel := context.WithCancel(d.ctx)
	d.searchCancel = cancel
	d.searchJobs <- searchJob{ctx: ctx, generation: d.searchGeneration, alias: d.logAlias, runID: d.logRun, request: request}
}
func (d *dashboard) pollSearch() {
	for {
		select {
		case <-d.ctx.Done():
			return
		case job := <-d.searchJobs:
			for job.ctx.Err() == nil {
				ctx, cancel := context.WithTimeout(job.ctx, 2*time.Second)
				r, err := d.client.SearchLogs(ctx, job.alias, job.runID, job.request)
				cancel()
				if !d.send(job.ctx, event{search: &r, err: err, searchGeneration: job.generation, searchLast: job.request.Last}) {
					break
				}
				if err != nil || r.Done || len(r.Matches) > 0 {
					break
				}
				job.request.State = r.State
			}
		}
	}
}
func (d *dashboard) consumeSearch(e event) {
	if !d.logPaneFocused() {
		return
	}
	if e.err != nil {
		d.searchBusy = false
		d.notify("Search: " + e.err.Error())
		return
	}
	r := e.search
	if r.RunID != d.logRun {
		return
	}
	if r.State != nil {
		d.searchOffset = r.State.Offset
	}
	if r.Error != "" {
		d.logError = singleLine(r.Error)
	}
	if !r.Done && len(r.Matches) == 0 {
		return
	}
	d.searchBusy = false
	if len(r.Matches) == 0 {
		if !e.searchLast {
			d.searchDone = r.Done
			d.searchResume = r.State
		}
		d.notify("No further retained matches")
		return
	}
	d.searchDone = r.Done
	d.searchResume = r.State
	if r.State != nil && r.State.Gaps {
		d.notify("Search crossed missing/rotated output; only retained matches are available")
	}
	d.matches = append([]model.LogMatch(nil), r.Matches...)
	d.matchIndex = 0
	if e.searchLast {
		d.searchResume = nil
		d.searchDone = false
	}
	d.jumpMatch()
}
func (d *dashboard) jumpMatch() {
	if d.matchIndex < 0 || d.matchIndex >= len(d.matches) {
		return
	}
	m := d.matches[d.matchIndex]
	anchor := m.LineStart
	column := int(m.Column)
	if d.state.Project.Logs.Timestamps {
		column += 25
	}
	if m.Cursor-anchor >= windowBytes/2 || m.Column == 0 && anchor == 0 && m.Cursor > 0 {
		anchor = m.Cursor // a huge line is shown as a bounded fragment at the match
		column = 0
		if d.state.Project.Logs.Timestamps {
			column = 25
		}
	}
	d.queueWindow(anchor, 0, 0)
	d.wrapTarget = &wrapTarget{anchor: anchor, column: column}
}
func (d *dashboard) nextMatch(delta int) {
	if !d.logPaneFocused() || d.searchQuery == "" || d.searchBusy || d.windowBusy || d.matchIndex < 0 {
		return
	}
	next := d.matchIndex + delta
	if next >= 0 && next < len(d.matches) {
		d.matchIndex = next
		d.jumpMatch()
		return
	}
	current := d.matches[d.matchIndex]
	if delta > 0 {
		if d.searchDone {
			d.notify("No further retained matches")
			return
		}
		request := model.LogSearchRequest{Query: d.searchQuery, State: d.searchResume}
		if request.State == nil {
			request.After = current.Cursor + 1
		}
		d.requestSearch(request)
	} else {
		before, cutoff := current.End, current.Cursor
		d.requestSearch(model.LogSearchRequest{Query: d.searchQuery, Before: &before, MatchBefore: &cutoff, Last: true})
	}
}
func (d *dashboard) searchStatus() string {
	if d.searchQuery == "" {
		return ""
	}
	if d.searchBusy {
		return fmt.Sprintf("searching retained logs… (cursor %d) — Esc cancels", d.searchOffset)
	}
	if d.matchIndex >= 0 {
		return fmt.Sprintf("search %q — n/N next/previous, Esc clears", d.searchQuery)
	}
	return "no retained matches — / new search, Esc clears"
}

// Search overlays fixed styles without sacrificing the application colors that
// are restored at the end of each match. Only already-sanitized text enters it.
func highlightSearch(line logLine, query string) logLine {
	return highlightSearchContext(line, query, "", "")
}
func highlightSearchContext(line logLine, query, before, after string) logLine {
	if query == "" {
		return line
	}
	visible := plain(line.text)
	text := before + visible + after
	type span struct{ start, end int }
	var spans []span
	for start := 0; start < len(text); {
		i := strings.Index(text[start:], query)
		if i < 0 {
			break
		}
		i += start
		left, right := max(0, i-len(before)), min(len(visible), i+len(query)-len(before))
		if left < right {
			spans = append(spans, span{left, right})
		}
		start = i + len(query)
	}
	if len(spans) == 0 {
		return line
	}
	var current style
	if strings.HasPrefix(line.prefix, "\x1b[") {
		current.apply(line.prefix[2 : len(line.prefix)-1])
	}
	var out strings.Builder
	pos, index := 0, 0
	active := false
	for source := line.text; len(source) > 0; {
		if strings.HasPrefix(source, "\x1b[") {
			end := strings.IndexByte(source, 'm')
			current.apply(source[2:end])
			if !active {
				out.WriteString(source[:end+1])
			}
			source = source[end+1:]
			continue
		}
		want := index < len(spans) && pos >= spans[index].start && pos < spans[index].end
		if want != active {
			if want {
				out.WriteString("\x1b[1;30;43m")
			} else {
				out.WriteString(current.sequence())
			}
			active = want
		}
		r, n := utf8.DecodeRuneInString(source)
		out.WriteRune(r)
		pos += n
		source = source[n:]
		if index < len(spans) && pos >= spans[index].end {
			index++
		}
	}
	if active {
		out.WriteString(current.sequence())
	}
	line.text = out.String()
	return line
}

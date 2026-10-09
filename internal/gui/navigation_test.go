package gui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jesseduffield/gocui"
	"github.com/nullco/lazyrun/internal/logsearch"
	"github.com/nullco/lazyrun/internal/model"
)

func navigationDashboard(t *testing.T) *dashboard {
	d := testDashboard(t, &fakeClient{})
	d.state.Commands[0].Run.ID = "run"
	d.setFocus(servicesPane)
	d.setFocus(detailPane)
	d.logWidth, d.logHeight = 60, 8
	return d
}
func TestLogSearchShortcutsOnlyAffectFocusedLogs(t *testing.T) {
	for _, test := range []struct {
		name              string
		focus, owner      pane
		tab               int
		help, small, idle bool
	}{
		{"project", projectPane, projectPane, 0, false, false, false},
		{"services", servicesPane, servicesPane, 0, false, false, false},
		{"tasks", tasksPane, tasksPane, 0, false, false, false},
		{"project details", detailPane, projectPane, 0, false, false, false},
		{"command details", detailPane, servicesPane, 1, false, false, false},
		{"help", detailPane, servicesPane, 0, true, false, false},
		{"minimum size", detailPane, servicesPane, 0, false, true, false},
		{"not started", detailPane, servicesPane, 0, false, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := navigationDashboard(t)
			d.cancelWindow()
			d.focus, d.owner, d.tab, d.help, d.small = test.focus, test.owner, test.tab, test.help, test.small
			if test.idle {
				d.state.Commands[0].Run.ID = ""
			}
			d.searchQuery, d.searchDraft = "saved", "draft"
			d.matches = []model.LogMatch{{Cursor: 10, End: 15}, {Cursor: 30, End: 35}}
			d.matchIndex, d.follow, d.history = 0, false, true
			d.openSearch()
			d.nextMatch(1)
			d.nextMatch(-1)
			d.goHome()
			d.goFollow()
			d.requestSearch(model.LogSearchRequest{Query: "saved"})
			if d.clearFocusedSearch() || d.searchEditing || d.searchBusy || d.windowBusy || d.matchIndex != 0 || d.searchQuery != "saved" || d.searchDraft != "draft" || d.follow || !d.history || len(d.jobs) != 0 || len(d.searchJobs) != 0 || d.notice != "" {
				t.Fatal("a non-Logs pane handled a log-search shortcut")
			}
		})
	}
	d := navigationDashboard(t)
	d.openSearch()
	if !d.searchEditing {
		t.Fatal("focused Logs did not open its own search")
	}
	d.searchEditing = false
	d.searchQuery = "saved"
	if !d.clearFocusedSearch() || d.searchQuery != "" {
		t.Fatal("focused Logs did not clear its own search")
	}
}

func TestLogSearchUsesGenericFooterInputAndStatusIsLocal(t *testing.T) {
	for _, size := range [][2]int{{MinWidth, MinHeight}, {100, 30}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: size[0], Height: size[1], OutputMode: gocui.OutputTrue, SupportOverlaps: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(g.Close)
			d := navigationDashboard(t)
			d.openSearch()
			if err := d.layout(g); err != nil {
				t.Fatal(err)
			}
			x0, y0, x1, y1, err := g.ViewPosition("search")
			if err != nil {
				t.Fatal(err)
			}
			log := geometry(size[0], size[1])["detail"]
			if x0 != -1 || x1 != size[0] || y0 != size[1]-2 || y1 != size[1] || y0 < log.y1 {
				t.Fatal("input did not stay in the full-width bottom row", x0, y0, x1, y1, log)
			}
			for _, draft := range []string{"", "needle", strings.Repeat("x", 252) + "TAIL", strings.Repeat("界", 80) + "TAIL", strings.Repeat("e\u0301", 80) + "TAIL"} {
				d.searchDraft = draft
				if err := d.layout(g); err != nil {
					t.Fatal(err)
				}
				input, _ := g.View("search")
				footer, _ := g.View("footer")
				x, y := input.Cursor()
				if input.Frame || input.Title != "" || !strings.HasPrefix(input.Buffer(), "Filter: ") || strings.Contains(input.Buffer(), "Logs") || strings.TrimSpace(footer.Buffer()) != "" {
					t.Fatal("input was not a generic borderless footer", input.Buffer(), footer.Buffer())
				}
				if x < len("Filter: ") || x >= size[0] || y != 0 || !g.Cursor || g.CurrentView().Name() != "search" || d.focus != detailPane {
					t.Fatal("input cursor or target pane changed", x, y)
				}
				if strings.HasSuffix(draft, "TAIL") && !strings.Contains(input.Buffer(), "TAIL") {
					t.Fatal("long input hid the typed suffix", input.Buffer())
				}
			}
			input, _ := g.View("search")
			input.Editor.Edit(input, gocui.KeyEsc, 0, gocui.ModNone)
			if err := d.layout(g); err != nil {
				t.Fatal(err)
			}
			footer, _ := g.View("footer")
			if _, err := g.View("search"); err == nil || g.Cursor || g.CurrentView().Name() != "detail" || !strings.Contains(footer.Buffer(), "Tab focus") {
				t.Fatal("cancel did not restore the footer and owning pane", footer.Buffer())
			}
			d.searchQuery = "saved"
			d.matchIndex = 0
			d.setFocus(servicesPane)
			if err := d.layout(g); err != nil {
				t.Fatal(err)
			}
			v, _ := g.View("notification")
			if strings.Contains(v.Buffer(), "search") || strings.Contains(v.Buffer(), "n/N") {
				t.Fatal("another pane advertised global log-search shortcuts", v.Buffer())
			}
		})
	}
}

func TestLeavingLogsRejectsPendingSearchMatchWindow(t *testing.T) {
	d := navigationDashboard(t)
	d.searchQuery = "needle"
	d.matches = []model.LogMatch{{Cursor: 10, End: 16}, {Cursor: 30, End: 36}}
	d.matchIndex = 0
	d.jumpMatch()
	job := <-d.jobs
	d.setFocus(servicesPane)
	if d.windowBusy || d.wrapTarget != nil || d.searchQuery != "needle" {
		t.Fatal("search match page remained active outside Logs")
	}
	d.events <- event{generation: job.generation, window: true, read: &model.LogRead{RunID: "run", Data: []byte("OLD"), Next: 3}}
	d.events <- event{searchGeneration: d.searchGeneration, search: &model.LogSearchResult{RunID: "run", Matches: []model.LogMatch{{Cursor: 100, End: 106}}}}
	d.drain()
	if len(d.buffer.lines) != 0 || d.matches[0].Cursor != 10 || d.windowBusy {
		t.Fatal("background search result affected another pane")
	}
	d.setFocus(detailPane)
	d.nextMatch(1)
	if d.matchIndex != 1 || !d.windowBusy {
		t.Fatal("returning to Logs lost its saved search")
	}
}

func TestLineAnchorsTrackRawNewlinesNotHiddenControlPayloads(t *testing.T) {
	data := []byte("A\x1b]0;hidden\nlines\aB\nC\r\nD café\n")
	for size := 1; size <= len(data); size++ {
		var b logBuffer
		for i := 0; i < len(data); i += size {
			end := min(len(data), i+size)
			b.consume(model.LogRead{Records: []model.LogRecord{{Cursor: uint64(i), Data: data[i:end]}}, Next: uint64(end)}, uint64(i), i == 0)
		}
		expected := []uint64{0, uint64(strings.Index(string(data), "C\r")), uint64(strings.Index(string(data), "\r\n")) + 1, uint64(len(data))}
		if len(b.lines) != len(expected) {
			t.Fatal(size, b.lines)
		}
		for i, line := range b.lines {
			if line.cursor != expected[i] {
				t.Fatal(size, i, line, expected)
			}
		}
	}
}
func TestBackwardPagingHomeForwardAndFollowRejectStalePages(t *testing.T) {
	d := navigationDashboard(t)
	d.buffer.appendAt([]byte("recent\nlast\n"), time.Time{}, 90000)
	d.streamFirst = 20
	d.streamEnd = 200000
	d.cursor = 90012
	d.loaded = true
	d.scrollLogs(-1)
	if !d.history || !d.windowBusy || d.follow {
		t.Fatal("edge did not request older history")
	}
	job := <-d.jobs
	if job.after != 90000 || job.before != windowBytes/2 || !job.window {
		t.Fatal(job)
	}
	generation := d.generation
	page := model.LogRead{RunID: "run", First: 20, End: 200000, Next: 90020, Records: []model.LogRecord{{Cursor: 89000, Data: []byte("older\nrecent\nlast\n")}}}
	d.events <- event{generation: generation - 1, window: true, read: &page}
	d.drain()
	if !d.windowBusy || d.buffer.lines[0].text != "recent" {
		t.Fatal("stale page replaced current view")
	}
	d.events <- event{generation: generation, window: true, read: &page, anchor: 89006, delta: -1}
	d.drain()
	if d.windowBusy || d.buffer.lines[0].text != "older" || d.top != 0 {
		t.Fatal("older page not anchored", d.top, d.buffer.lines)
	}
	d.goHome()
	job = <-d.jobs
	if job.after != 0 || job.before != 0 {
		t.Fatal("Home did not seek earliest output", job)
	}
	d.consumeWindow(event{window: true, anchor: 0, read: &model.LogRead{RunID: "run", First: 50, End: 200000, Next: 60, Truncated: true, Records: []model.LogRecord{{Cursor: 50, Data: []byte("begin\n")}}}})
	if !strings.Contains(d.notice, "no longer retained") || d.top != 0 {
		t.Fatal(d.notice, d.top)
	}
	d.goFollow()
	if d.history || !d.follow || d.loaded || d.cursor != 0 || !d.jobActive {
		t.Fatal("G did not return to a fresh live tail")
	}
}
func TestSearchPromptIsBoundedAndDoesNotExecuteLifecycleKeys(t *testing.T) {
	d := navigationDashboard(t)
	d.openSearch()
	if !d.searchEditing {
		t.Fatal("search did not open")
	}
	for _, r := range "qrsS123?[]nN/" {
		d.searchKey(r)
	}
	if d.searchDraft != "qrsS123?[]nN/" || d.busy {
		t.Fatal("search keys escaped prompt", d.searchDraft)
	}
	d.searchRune('\x1b')
	d.searchRune('\u202e')
	for range 1000 {
		d.searchRune('é')
	}
	if len(d.searchDraft) > logsearch.MaxQuery {
		t.Fatal("unbounded prompt")
	}
	d.searchKey(gocui.KeyBackspace2)
	if !strings.HasSuffix(d.searchDraft, "é") {
		t.Fatal("backspace split UTF-8")
	}
	d.searchKey(gocui.KeyEsc)
	if d.searchEditing || d.searchQuery != "" {
		t.Fatal("cancel submitted search")
	}
	d.openSearch()
	d.searchDraft = "needle"
	d.searchKey(gocui.KeyEnter)
	job := <-d.searchJobs
	if !d.searchBusy || d.follow || job.request.Query != "needle" || job.request.After != 0 {
		t.Fatal("search did not scan full retained stream", job)
	}
	generation := d.searchGeneration
	d.events <- event{searchGeneration: generation - 1, search: &model.LogSearchResult{RunID: "run", Matches: []model.LogMatch{{Cursor: 1, End: 7}}}}
	d.drain()
	if len(d.matches) != 0 {
		t.Fatal("stale search results accepted")
	}
	d.consumeSearch(event{searchGeneration: generation, search: &model.LogSearchResult{RunID: "run", State: &logsearch.State{Offset: 500}, Matches: []model.LogMatch{{Cursor: 100, End: 106}, {Cursor: 300, End: 306}}}})
	if d.searchBusy || d.matchIndex != 0 || !d.history {
		t.Fatal("search did not jump to first result")
	}
	window := <-d.jobs
	if window.after != 100 {
		t.Fatal(window)
	}
	d.windowBusy = false
	d.nextMatch(1)
	window = <-d.jobs
	if window.after != 300 {
		t.Fatal("next match did not seek", window)
	}
	d.windowBusy = false
	d.nextMatch(-1)
	window = <-d.jobs
	if window.after != 100 {
		t.Fatal("previous cached match did not seek", window)
	}
	d.windowBusy = false
	d.nextMatch(-1)
	job = <-d.searchJobs
	if !job.request.Last || *job.request.Before != 106 || *job.request.MatchBefore != 100 {
		t.Fatal("previous page did not include overlapping matches", job)
	}
	d.clearSearch()
	if d.searchBusy || d.searchQuery != "" || len(d.matches) != 0 {
		t.Fatal("Esc did not clear/cancel")
	}
}
func TestSearchJumpShowsLongLineMatchesAndPreviousBoundaryKeepsForwardCursor(t *testing.T) {
	d := navigationDashboard(t)
	d.matches = []model.LogMatch{{Cursor: 9000, End: 9006, LineStart: 8900, Column: 100}}
	d.matchIndex = 0
	d.jumpMatch()
	job := <-d.jobs
	if job.after != 8900 || d.wrapTarget == nil || d.wrapTarget.column != 100 || d.horizontal != 0 {
		t.Fatal("match did not target its wrapped row", job, d.wrapTarget)
	}
	d.matches[0] = model.LogMatch{Cursor: 40000, End: 40006, LineStart: 0, Column: 40000}
	d.jumpMatch()
	job = <-d.jobs
	if job.after != 40000 || d.horizontal != 0 {
		t.Fatal("huge line did not seek a bounded match fragment", job, d.horizontal)
	}
	resume := &logsearch.State{Offset: 12345}
	d.searchResume = resume
	d.searchDone = false
	d.consumeSearch(event{searchLast: true, search: &model.LogSearchResult{RunID: "run", Done: true}})
	if d.searchResume != resume || d.searchDone {
		t.Fatal("previous boundary destroyed forward continuation")
	}
}
func TestSearchWorkerCancellationOnFocusSelectionAndDetails(t *testing.T) {
	c := &fakeClient{reads: make(chan string, 4)}
	d := testDashboard(t, c)
	d.state.Commands[0].Run.ID = "one"
	d.state.Commands[1].Run.ID = "two"
	d.setFocus(servicesPane)
	d.setFocus(detailPane)
	d.spawn(d.pollSearch)
	d.searchQuery = "needle"
	d.requestSearch(model.LogSearchRequest{Query: "needle"})
	select {
	case a := <-c.reads:
		if a != "api" {
			t.Fatal(a)
		}
	case <-time.After(time.Second):
		t.Fatal("search worker did not request")
	}
	generation := d.searchGeneration
	d.setFocus(servicesPane)
	if d.searchBusy || d.searchGeneration == generation || d.searchQuery != "needle" {
		t.Fatal("leaving Logs failed to cancel local search or cleared its saved query")
	}
	d.move(1)
	if d.searchBusy || d.searchQuery != "" || d.searchGeneration == generation {
		t.Fatal("selection failed to cancel search")
	}
	d.setFocus(detailPane)
	d.searchQuery = "needle"
	d.requestSearch(model.LogSearchRequest{Query: "needle"})
	select {
	case a := <-c.reads:
		if a != "worker" {
			t.Fatal(a)
		}
	case <-time.After(time.Second):
		t.Fatal("coalesced worker did not switch requests")
	}
	d.tab = 1
	d.syncLogView()
	if d.searchBusy {
		t.Fatal("Details did not cancel search")
	}
	d.cancel()
}

func TestHomeWindowRetainsBeginningEvenForNewlineFlood(t *testing.T) {
	d := navigationDashboard(t)
	data := []byte(strings.Repeat("\n", windowBytes))
	d.cancelWindow()
	d.history = true
	d.consumeWindow(event{window: true, read: &model.LogRead{RunID: "run", Next: windowBytes, End: 100000, Records: []model.LogRecord{{Data: data}}}})
	if d.buffer.evicted || d.buffer.lines[0].cursor != 0 || len(d.buffer.lines) > MaxBufferLines {
		t.Fatal("Home discarded the original beginning")
	}
	d.scrollLogs(100000)
	job := <-d.jobs
	if !job.window || job.after == 0 || job.before > windowBytes/2 {
		t.Fatal("forward history did not page", job)
	}
}

func FuzzSearchAgreesWithTerminalSanitizer(f *testing.F) {
	f.Add([]byte("a\x1b[31mb\x1b[0mc\n"), "abc")
	f.Add([]byte("\t\xff\xe2"), "�")
	f.Fuzz(func(t *testing.T, data []byte, query string) {
		if len(data) > 10000 || !logsearch.ValidQuery(query) {
			return
		}
		text := plain(string(data))
		expected := 0
		for i := 0; i < len(text); {
			at := strings.Index(text[i:], query)
			if at < 0 {
				break
			}
			expected++
			i += at + 1
		}
		request := logsearch.Request{Query: query}
		actual := 0
		for range 10000 {
			r, err := logsearch.Scan(request, func(after uint64, limit int) (logsearch.Read, error) {
				end := min(uint64(len(data)), after+uint64(limit))
				var records []logsearch.Record
				if end > after {
					records = []logsearch.Record{{Cursor: after, Data: data[after:end]}}
				}
				return logsearch.Read{Records: records, Next: end, End: uint64(len(data))}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			actual += len(r.Matches)
			if r.Done {
				if actual != expected {
					t.Fatal("search/filter mismatch", actual, expected, data, query)
				}
				return
			}
			request.State = r.State
		}
		t.Fatal("search failed to finish")
	})
}

func TestHighlightPreservesColorsAndNeverAddsUntrustedControls(t *testing.T) {
	line := logLine{text: "\x1b[31mone needle two\x1b[0m"}
	got := highlightSearch(line, "needle").text
	checkSafe(t, got)
	if plain(got) != "one needle two" || !strings.Contains(got, "\x1b[1;30;43mneedle\x1b[0;31m") {
		t.Fatal(got)
	}
	if highlightSearch(line, "").text != line.text {
		t.Fatal("empty search changed colors")
	}
}

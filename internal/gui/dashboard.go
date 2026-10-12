// Package gui is a client-only dashboard: no rendering context owns processes,
// signals, capture pipes, or supervisor startup/recovery.
package gui

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/jesseduffield/gocui"
	"github.com/nullco/lazyrun/internal/model"
)

// Client is deliberately independent of the runtime and Unix transport.
type Client interface {
	Sync(context.Context, []byte) error
	State(context.Context) (model.State, error)
	Start(context.Context, string, []string) (model.Run, error)
	Stop(context.Context, string) (model.Run, error)
	Restart(context.Context, string, []string) (model.Run, error)
	Logs(context.Context, string, string, uint64, int) (model.LogRead, error)
	TailLogs(context.Context, string, string, int, int) (model.LogRead, error)
	WindowLogs(context.Context, string, string, uint64, int, int) (model.LogRead, error)
	SearchLogs(context.Context, string, string, model.LogSearchRequest) (model.LogSearchResult, error)
}

type Options struct {
	Environment   []string
	Version       string
	ProjectClient func(string) Client
}
type pane int

const (
	projectPane pane = iota
	servicesPane
	tasksPane
	detailPane
)

var paneNames = []string{"project", "services", "tasks", "detail"}

type event struct {
	reload           bool
	window           bool
	anchor           uint64
	delta            int
	search           *model.LogSearchResult
	searchGeneration uint64
	searchLast       bool
	state            *model.State
	projects         []model.ProjectSummary
	stateGeneration  uint64
	scopedState      bool
	read             *model.LogRead
	err              error
	generation       uint64
	initial          bool
	after            uint64
	action           string
	alias            string
}
type logJob struct {
	client       Client
	window       bool
	before       int
	delta        int
	ctx          context.Context
	generation   uint64
	alias, runID string
	after        uint64
	tail         int
	initial      bool
}

type dashboard struct {
	navigation
	client                     Client
	projects                   []model.ProjectSummary
	stateScopes                chan stateScope
	stateGeneration            uint64
	opts                       Options
	ctx                        context.Context
	cancel                     context.CancelFunc
	workers                    sync.WaitGroup
	events                     chan event
	wake                       chan struct{}
	jobs                       chan logJob
	state                      model.State
	connected                  bool
	loadingProject             bool
	connectionError            string
	focus, owner               pane
	selected                   [4]string
	visibleAliases             [4][]string // last rendered command rows, not current state order
	collapsed                  [4]bool     // last rendered pane visibility, including mouse headers
	tab                        int
	help                       bool
	small                      bool
	busy                       bool
	reloading                  bool
	notice                     string
	buffer                     logBuffer
	logAlias, logRun           string
	logError                   string
	unavailable                bool
	loaded                     bool
	cursor                     uint64
	generation                 uint64
	viewCancel                 context.CancelFunc
	jobActive                  bool
	follow                     bool
	top, horizontal, detailTop int
	helpTop, helpSelection     int
	helpRows                   []int // last rendered popup rows; headings are -1
}

func newDashboard(ctx context.Context, client Client, state model.State, opts Options) *dashboard {
	ctx, cancel := context.WithCancel(ctx)
	d := &dashboard{client: client, opts: opts, ctx: ctx, cancel: cancel,
		events: make(chan event, 16), wake: make(chan struct{}, 1), jobs: make(chan logJob, 1), state: state,
		navigation: navigation{searchJobs: make(chan searchJob, 1), matchIndex: -1},
		connected:  true, focus: projectPane, owner: projectPane, follow: true,
		stateScopes: make(chan stateScope, 1),
		projects:    []model.ProjectSummary{{ID: state.Project.ID, Name: state.Project.Name, Root: state.Project.Root}}}
	d.opts.Environment = append([]string(nil), opts.Environment...)
	d.reselect()
	return d
}

// Run returns after restoring the terminal. Only local readers/requests are
// canceled on exit; an accepted lifecycle operation remains supervisor-owned.
func Run(ctx context.Context, client Client, opts Options) error {
	initialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	state, err := client.State(initialCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("dashboard state: %w", err)
	}
	g, err := gocui.NewGui(gocui.NewGuiOpts{OutputMode: gocui.OutputTrue, SupportOverlaps: true})
	if err != nil {
		return err
	}
	d := newDashboard(ctx, client, state, opts)
	defer func() { d.cancel(); d.workers.Wait(); g.Close() }()
	g.Cursor = false
	g.ShowListFooter = true
	g.SetManagerFunc(d.layout)
	if err := d.bindings(g); err != nil {
		return err
	}
	// Draw the already-loaded state without waiting for another RPC to finish.
	d.send(d.ctx, event{})
	d.spawn(func() { d.pollState(client) })
	d.spawn(d.pollLogs)
	d.spawn(d.pollSearch)
	d.spawn(func() { d.updateLoop(g.UpdateAsync) })
	err = g.MainLoop()
	if err == gocui.ErrQuit {
		return nil
	}
	return err
}

func (d *dashboard) spawn(fn func()) { d.workers.Add(1); go func() { defer d.workers.Done(); fn() }() }
func (d *dashboard) send(ctx context.Context, e event) bool {
	select {
	case <-ctx.Done():
		return false
	case d.events <- e:
		select {
		case d.wake <- struct{}{}:
		default:
		}
		return true
	}
}
func wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func (d *dashboard) pollState(initial Client) {
	var previous model.State
	var previousProjects []model.ProjectSummary
	var previousErr error
	havePrevious := false
	scope := stateScope{client: initial}
	for {
		ctx, cancel := context.WithTimeout(d.ctx, 2*time.Second)
		state, err := scope.client.State(ctx)
		var projects []model.ProjectSummary
		if err == nil {
			if listing, ok := scope.client.(projectLister); ok {
				projects, err = listing.Projects(ctx)
			}
		}
		cancel()
		if !havePrevious || !sameError(previousErr, err) || err == nil && (!reflect.DeepEqual(previous, state) || !reflect.DeepEqual(previousProjects, projects)) {
			if !d.send(d.ctx, event{state: &state, projects: projects, err: err, scopedState: true, stateGeneration: scope.generation}) {
				return
			}
			previous, previousProjects, previousErr, havePrevious = state, projects, err, true
		}
		timer := time.NewTimer(400 * time.Millisecond)
		select {
		case <-d.ctx.Done():
			timer.Stop()
			return
		case scope = <-d.stateScopes:
			timer.Stop()
			havePrevious = false
		case <-timer.C:
		}
	}
}
func (d *dashboard) pollLogs() {
	for {
		select {
		case <-d.ctx.Done():
			return
		case job := <-d.jobs:
			client := job.client
			var previous model.LogRead
			var previousErr error
			havePrevious := false
			for job.ctx.Err() == nil {
				ctx, cancel := context.WithTimeout(job.ctx, 2*time.Second)
				var r model.LogRead
				var err error
				if job.window {
					r, err = client.WindowLogs(ctx, job.alias, job.runID, job.after, job.before, windowBytes)
				} else if job.initial {
					r, err = client.TailLogs(ctx, job.alias, job.runID, job.tail, 64*1024)
				} else {
					r, err = client.Logs(ctx, job.alias, job.runID, job.after, 64*1024)
				}
				cancel()
				if job.window || !havePrevious || !sameError(previousErr, err) || err == nil && (job.initial || logReadChanged(previous, r)) {
					if !d.send(job.ctx, event{read: &r, err: err, generation: job.generation, initial: job.initial, after: job.after, window: job.window, anchor: job.after, delta: job.delta}) {
						break
					}
					previous, previousErr, havePrevious = r, err, true
					previous.Data, previous.Records = nil, nil
				}
				if job.window {
					break
				}
				if err == nil {
					job.initial = false
					job.after = r.Next
				}
				if !wait(job.ctx, 200*time.Millisecond) {
					break
				}
			}
		}
	}
}
func (d *dashboard) drain() {
drain:
	for range cap(d.events) {
		select {
		case e := <-d.events:
			switch {
			case e.reload:
				d.reloading = false
				if e.err != nil {
					d.notify("Config reload failed: " + e.err.Error())
				} else {
					d.notify("Config reloaded; running commands unchanged")
				}
			case e.action != "":
				d.busy = false
				if e.err != nil {
					d.notify(e.action + " " + e.alias + ": " + e.err.Error() + "; query state before retrying")
				} else {
					d.notify(e.action + " requested for " + e.alias)
				}
			case e.state != nil:
				if e.scopedState && e.stateGeneration != d.stateGeneration {
					continue
				}
				d.loadingProject = false
				d.connected = e.err == nil
				if e.err != nil {
					d.connectionError = singleLine(e.err.Error())
				} else {
					d.connectionError = ""
					d.state = *e.state
					if e.projects != nil {
						d.projects = e.projects
					}
					d.reselect()
				}
			case e.search != nil && e.searchGeneration == d.searchGeneration:
				d.consumeSearch(e)
			case e.read != nil && e.generation == d.generation:
				if e.window {
					d.consumeWindow(e)
					continue
				}
				if e.err != nil {
					d.logError = singleLine(e.err.Error())
					continue
				}
				if e.read.RunID != d.logRun {
					continue
				}
				d.logError = singleLine(e.read.Error)
				d.unavailable = e.read.Unavailable
				d.trimLogView(d.buffer.consume(*e.read, e.after, e.initial))
				d.cursor = e.read.Next
				d.streamFirst, d.streamEnd = e.read.First, e.read.End
				d.loaded = true
			}
		default:
			break drain
		}
	}
	d.syncLogView()
	d.finishLogs()
}

func (d *dashboard) trimLogView(dropped int) {
	if !d.follow {
		if dropped > d.top {
			d.wrapTop = 0
		}
		d.top = max(0, d.top-dropped)
	}
}

// An exit-state update must finish pending UTF-8/escape parsing even when the
// log worker suppresses an unchanged empty response at the final cursor.
func (d *dashboard) finishLogs() {
	item, ok := d.current()
	if !ok || !d.loaded || item.Run.ID != d.logRun || item.Run.Outcome == nil || d.cursor < item.Run.LogEnd {
		return
	}
	if text := d.buffer.filter.Finish(); text != "" {
		d.trimLogView(d.buffer.append([]byte(text), time.Time{}))
	}
}

func (d *dashboard) notify(text string) {
	d.notice = singleLine(text)
}

func (d *dashboard) items(p pane) []model.CommandState {
	kind := model.Service
	if p == tasksPane {
		kind = model.Task
	} else if p != servicesPane {
		return nil
	}
	var result []model.CommandState
	for _, item := range d.state.Commands {
		if item.DisplayKind() == kind {
			result = append(result, item)
		}
	}
	return result
}
func (d *dashboard) reselect() {
	for _, p := range []pane{servicesPane, tasksPane} {
		items := d.items(p)
		found := false
		for _, item := range items {
			if item.Run.Definition.Alias == d.selected[p] {
				found = true
				break
			}
		}
		if !found {
			d.selected[p] = ""
			if len(items) > 0 {
				d.selected[p] = items[0].Run.Definition.Alias
			}
		}
	}
}
func (d *dashboard) current() (model.CommandState, bool) {
	if d.owner == projectPane {
		return model.CommandState{}, false
	}
	for _, item := range d.items(d.owner) {
		if item.Run.Definition.Alias == d.selected[d.owner] {
			return item, true
		}
	}
	return model.CommandState{}, false
}
func (d *dashboard) syncLogView() {
	item, ok := d.current()
	alias, runID := "", ""
	if ok {
		alias, runID = item.Run.Definition.Alias, item.Run.ID
	}
	changed := alias != d.logAlias || runID != d.logRun
	if changed {
		d.resetNavigation()
		d.buffer.reset(d.state.Project.Logs.Timestamps)
		d.logAlias, d.logRun = alias, runID
		d.logError = ""
		d.unavailable = false
		d.loaded = false
		d.cursor = 0
		d.follow = true
		d.top, d.wrapTop, d.horizontal, d.detailTop = 0, 0, 0, 0
	}
	if !d.logPaneFocused() {
		if d.searchBusy || d.searchEditing {
			d.cancelSearch()
			d.searchEditing = false
		}
		if d.windowBusy && d.wrapTarget != nil {
			d.cancelWindow()
			d.wrapTarget = nil
		}
	}
	want := ok && runID != "" && d.tab == 0 && !d.small && !d.history
	if d.history && !changed {
		if d.tab != 0 || d.small || !ok {
			d.cancelWindow()
			d.cancelSearch()
		}
		return
	}
	if !changed && want == d.jobActive {
		return
	}
	if d.viewCancel != nil {
		d.viewCancel()
		d.viewCancel = nil
	}
	d.generation++
	d.jobActive = want
	// Coalesce rapid selection changes; the worker never queues view goroutines.
	select {
	case <-d.jobs:
	default:
	}
	if want {
		ctx, cancel := context.WithCancel(d.ctx)
		d.viewCancel = cancel
		d.jobs <- logJob{client: d.client, ctx: ctx, generation: d.generation, alias: alias, runID: runID, after: d.cursor, tail: d.state.Project.Logs.Tail, initial: !d.loaded}
	}
}
func (d *dashboard) action(action string) {
	if d.help || d.small || d.searchEditing {
		return
	}
	if d.loadingProject {
		d.notify("Loading project; actions disabled until state arrives")
		return
	}
	item, ok := d.current()
	if !ok {
		d.notify("Select a service or task; project-wide actions are not supported")
		return
	}
	if !d.connected {
		d.notify("Disconnected: actions disabled; reopen the dashboard if the supervisor was lost")
		return
	}
	if d.reloading {
		d.notify("Config reload pending; no lifecycle request was queued")
		return
	}
	if d.busy {
		d.notify("A lifecycle request is pending; no replacement was queued")
		return
	}
	if item.Removed && action != "stop" {
		d.notify("Removed from config: only stop is available")
		return
	}
	alias := item.Run.Definition.Alias
	label := action
	if item.DisplayKind() == model.Task {
		if action == "start" {
			label = "run"
		} else if action == "restart" {
			label = "rerun"
		}
	}
	d.busy = true
	d.notify(label + " pending for " + alias)
	client := d.client
	d.spawn(func() {
		ctx := d.ctx
		if action != "restart" {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
		}
		// Restart's transport deadline follows the current service timeout;
		// closing the dashboard still cancels the client's wait.
		var err error
		switch action {
		case "start":
			_, err = client.Start(ctx, alias, d.opts.Environment)
		case "stop":
			_, err = client.Stop(ctx, alias)
		case "restart":
			_, err = client.Restart(ctx, alias, d.opts.Environment)
		}
		d.send(d.ctx, event{action: label, alias: alias, err: err})
	})
}

func (d *dashboard) setFocus(p pane) {
	d.focus = p
	if p != detailPane {
		d.owner = p
		d.detailTop = 0
	}
	d.syncLogView()
}
func (d *dashboard) move(delta int) {
	if d.focus == projectPane {
		for i, p := range d.projects {
			if p.ID == d.state.Project.ID {
				d.selectProject(d.projects[max(0, min(len(d.projects)-1, i+delta))].ID)
				break
			}
		}
		return
	}
	if d.focus == detailPane {
		if d.owner != projectPane && d.tab == 0 {
			d.scrollLogs(delta)
		} else {
			d.detailTop = max(0, d.detailTop+delta)
		}
		return
	}
	items := d.items(d.focus)
	for i, item := range items {
		if item.Run.Definition.Alias == d.selected[d.focus] {
			d.selected[d.focus] = items[max(0, min(len(items)-1, i+delta))].Run.Definition.Alias
			break
		}
	}
	d.syncLogView()
}
func (d *dashboard) bindings(g *gocui.Gui) error {
	bindings := []struct {
		key any
		fn  func()
	}{
		{'1', func() { d.setFocus(projectPane) }}, {'2', func() { d.setFocus(servicesPane) }}, {'3', func() { d.setFocus(tasksPane) }},
		{gocui.KeyTab, func() { d.setFocus((d.focus + 1) % 4) }}, {gocui.KeyBacktab, func() { d.setFocus((d.focus + 3) % 4) }},
		{gocui.KeyEnter, func() { d.setFocus(detailPane) }}, {gocui.KeyEsc, func() { d.setFocus(d.owner) }},
		{'j', func() { d.move(1) }}, {'k', func() { d.move(-1) }}, {gocui.KeyArrowDown, func() { d.move(1) }}, {gocui.KeyArrowUp, func() { d.move(-1) }},
		{gocui.KeyPgdn, func() { d.move(10) }}, {gocui.KeyPgup, func() { d.move(-10) }},
		{gocui.KeyArrowLeft, func() { d.scrollHorizontal(-10) }}, {gocui.KeyArrowRight, func() { d.scrollHorizontal(10) }},
		{'[', func() { d.tab = (d.tab + 1) % 2; d.detailTop = 0; d.syncLogView() }}, {']', func() { d.tab = (d.tab + 1) % 2; d.detailTop = 0; d.syncLogView() }},
		{'S', func() { d.action("start") }}, {'s', func() { d.action("stop") }}, {'r', func() { d.action("restart") }},
		{'R', d.reloadConfig},
		{'G', d.goFollow}, {gocui.KeyHome, d.goHome},
		{'/', d.openSearch}, {'n', func() { d.nextMatch(1) }}, {'N', func() { d.nextMatch(-1) }},
		{gocui.KeyBackspace, func() {}}, {gocui.KeyBackspace2, func() {}},
	}
	for _, binding := range bindings {
		fn := binding.fn
		viewName := ""
		switch binding.key {
		case '/', 'n', 'N', 'G', gocui.KeyHome:
			viewName = "detail" // reserve other panes for their own future filters
		}
		if err := g.SetKeybinding(viewName, binding.key, gocui.ModNone, func(*gocui.Gui, *gocui.View) error {
			if d.searchEditing {
				d.searchKey(binding.key)
				return nil
			}
			if binding.key == gocui.KeyEsc && d.clearFocusedSearch() {
				return nil
			}
			if d.help {
				switch binding.key {
				case gocui.KeyEsc:
					d.help = false
				case 'j', gocui.KeyArrowDown:
					d.moveHelp(1)
				case 'k', gocui.KeyArrowUp:
					d.moveHelp(-1)
				case gocui.KeyPgdn:
					d.moveHelp(10)
				case gocui.KeyPgup:
					d.moveHelp(-10)
				}
				return nil
			}
			if !d.small {
				fn()
				// Activate the editor synchronously: a terminal can deliver /term and
				// Enter in one event batch, before the next manager repaint.
				if binding.key == '/' && d.searchEditing {
					return d.layout(g)
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	for _, key := range []any{'q', gocui.KeyCtrlC} {
		if err := g.SetKeybinding("", key, gocui.ModNone, func(*gocui.Gui, *gocui.View) error {
			if d.searchEditing {
				d.searchKey(key)
				return nil
			}
			return gocui.ErrQuit
		}); err != nil {
			return err
		}
	}
	if err := g.SetKeybinding("", '?', gocui.ModNone, func(*gocui.Gui, *gocui.View) error {
		if d.searchEditing {
			d.searchKey('?')
			return nil
		}
		if !d.small {
			d.toggleHelp()
		}
		return nil
	}); err != nil {
		return err
	}
	return d.mouseBindings(g)
}

func outcome(r model.Run) string {
	text := r.Label()
	if r.Outcome != nil {
		switch {
		case r.Outcome.ExitCode != nil:
			text += fmt.Sprintf(" (%d)", *r.Outcome.ExitCode)
		case r.Outcome.Signal != 0:
			text += fmt.Sprintf(" (signal %d)", r.Outcome.Signal)
		}
	}
	return singleLine(text)
}
func itemLabel(item model.CommandState) string {
	return itemLabelWithStatus(item, outcome(item.Run))
}
func itemLabelWithStatus(item model.CommandState, status string) string {
	text := singleLine(item.Run.Definition.Alias) + "  " + status
	if item.Removed {
		text += " [removed from config]"
	} else if item.Definition != nil && item.Definition.Kind != item.Run.Definition.Kind && item.Run.Lifecycle.Active() {
		text += " [moved in config]"
	}
	if item.Run.Error != "" || item.Run.MetadataError != "" || item.Run.LogError != "" {
		text += " !"
	}
	return text
}
func projectDetails(s model.State, connection, version string) string {
	p := s.Project
	return fmt.Sprintf("%s\n\nRoot: %s\nConfig: %s\nProject ID: %s\nShell: %s\nConnection: %s\nVersion: %s\n\n%d configured services, %d configured tasks\nLog retention: %d bytes per latest run\nInitial tail: %d lines; timestamps: %t\n\nOpening this dashboard starts nothing.\nSelect a service or task for Logs / Details.\nQuitting leaves commands running.\nNo project-wide lifecycle action or log stream.\nConfig is synchronized on open or with R to reload.\nReload leaves running commands unchanged.\nNo automatic restart or force-kill.", singleLine(p.Name), singleLine(p.Root), singleLine(p.ConfigPath), singleLine(p.ID), singleLine(p.Shell), singleLine(connection), singleLine(version), len(p.Services), len(p.Tasks), p.Logs.MaxBytes, p.Logs.Tail, p.Logs.Timestamps)
}
func commandDetails(item model.CommandState) string {
	r := item.Run
	var out strings.Builder
	fmt.Fprintf(&out, "Alias: %s\nKind: %s\nStatus: %s\nRun ID: %s\nPID: %d  PGID: %d\nStop requested: %t\n", singleLine(r.Definition.Alias), singleLine(string(r.Definition.Kind)), outcome(r), singleLine(r.ID), r.Identity.PID, r.Identity.PGID, r.StopRequested)
	if item.Definition != nil {
		fmt.Fprintf(&out, "Restart timeout: %s\n", item.Definition.RestartWait())
	}
	if !r.StartedAt.IsZero() {
		fmt.Fprintf(&out, "Started: %s\n", r.StartedAt.Format(time.RFC3339))
	}
	if r.EndedAt != nil {
		fmt.Fprintf(&out, "Ended: %s\n", r.EndedAt.Format(time.RFC3339))
	}
	fmt.Fprintf(&out, "\n%s definition:\nShell: %s\nWorking directory: %s\nCommand:\n%s\n", map[bool]string{true: "Latest run", false: "Configured"}[r.ID != ""], singleLine(r.Shell), singleLine(r.Definition.Cwd), plain(r.Definition.Command))
	if item.Removed {
		out.WriteString("\nRemoved from config; retained while active. Only stop is available.\n")
	}
	if item.Definition != nil && (item.Definition.Kind != r.Definition.Kind || item.Definition.Command != r.Definition.Command || item.Definition.Cwd != r.Definition.Cwd) {
		fmt.Fprintf(&out, "\nCurrent configuration (next run only):\nKind: %s\nWorking directory: %s\nCommand:\n%s\n", singleLine(string(item.Definition.Kind)), singleLine(item.Definition.Cwd), plain(item.Definition.Command))
	}
	for _, e := range []struct{ label, text string }{{"Run error", r.Error}, {"Metadata error", r.MetadataError}, {"Log error", r.LogError}} {
		if e.text != "" {
			fmt.Fprintf(&out, "\n%s: %s\n", e.label, plain(e.text))
		}
	}
	if r.Outcome != nil && r.Outcome.Error != "" {
		fmt.Fprintf(&out, "\nOutcome: %s: %s\n", singleLine(string(r.Outcome.Kind)), plain(r.Outcome.Error))
	}
	return out.String()
}

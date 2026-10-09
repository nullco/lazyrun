package gui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jesseduffield/gocui"
	"github.com/nullco/lazyrun/internal/model"
)

type fakeClient struct {
	mu      sync.Mutex
	calls   []string
	env     []string
	blocked chan struct{}
	reads   chan string
}

func (c *fakeClient) State(context.Context) (model.State, error) { return fixtureState(), nil }
func (c *fakeClient) mutation(ctx context.Context, action, alias string, env []string) (model.Run, error) {
	c.mu.Lock()
	c.calls = append(c.calls, action+" "+alias)
	c.env = append([]string(nil), env...)
	c.mu.Unlock()
	if c.blocked != nil {
		select {
		case <-ctx.Done():
			return model.Run{}, ctx.Err()
		case <-c.blocked:
		}
	}
	return model.Run{}, nil
}
func (c *fakeClient) Start(ctx context.Context, a string, e []string) (model.Run, error) {
	return c.mutation(ctx, "start", a, e)
}
func (c *fakeClient) Stop(ctx context.Context, a string) (model.Run, error) {
	return c.mutation(ctx, "stop", a, nil)
}
func (c *fakeClient) Restart(ctx context.Context, a string, e []string) (model.Run, error) {
	return c.mutation(ctx, "restart", a, e)
}
func (c *fakeClient) Logs(ctx context.Context, a, r string, after uint64, limit int) (model.LogRead, error) {
	if c.reads != nil {
		select {
		case c.reads <- a:
		case <-ctx.Done():
		}
	}
	<-ctx.Done()
	return model.LogRead{}, ctx.Err()
}
func (c *fakeClient) TailLogs(ctx context.Context, a, r string, tail, limit int) (model.LogRead, error) {
	return c.Logs(ctx, a, r, 0, limit)
}
func (c *fakeClient) WindowLogs(ctx context.Context, a, r string, anchor uint64, before, limit int) (model.LogRead, error) {
	return c.Logs(ctx, a, r, anchor, limit)
}
func (c *fakeClient) SearchLogs(ctx context.Context, a, r string, request model.LogSearchRequest) (model.LogSearchResult, error) {
	if c.reads != nil {
		select {
		case c.reads <- a:
		case <-ctx.Done():
		}
	}
	<-ctx.Done()
	return model.LogSearchResult{}, ctx.Err()
}
func fixtureState() model.State {
	p := model.Project{Name: "demo", Root: "/demo", Logs: model.DefaultLogSettings()}
	p.Services = []model.Definition{{Alias: "api", Kind: model.Service, Command: "echo api", Cwd: "/demo"}, {Alias: "worker", Kind: model.Service}}
	p.Tasks = []model.Definition{{Alias: "tests", Kind: model.Task, Command: "echo tests", Cwd: "/demo"}}
	s := model.State{Project: p}
	for _, def := range p.Definitions() {
		d := def
		s.Commands = append(s.Commands, model.CommandState{Definition: &d, Run: model.Run{Definition: def, Lifecycle: model.NotStarted}})
	}
	return s
}
func testDashboard(t *testing.T, c *fakeClient) *dashboard {
	t.Helper()
	d := newDashboard(context.Background(), c, fixtureState(), Options{Environment: []string{"SNAPSHOT=new"}})
	t.Cleanup(func() { d.cancel(); d.workers.Wait() })
	return d
}
func eventually(t *testing.T, fn func() bool) {
	t.Helper()
	end := time.Now().Add(3 * time.Second)
	for time.Now().Before(end) {
		if fn() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestNavigationActionsAndBoundedMutationRequests(t *testing.T) {
	c := &fakeClient{blocked: make(chan struct{})}
	d := testDashboard(t, c)
	d.action("start")
	if d.busy {
		t.Fatal("project started a command")
	}
	d.setFocus(servicesPane)
	d.move(1)
	if d.selected[servicesPane] != "worker" {
		t.Fatal(d.selected)
	}
	d.setFocus(detailPane)
	d.action("start")
	d.action("restart")
	eventually(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.calls) == 1 })
	c.mu.Lock()
	if c.calls[0] != "start worker" || c.env[0] != "SNAPSHOT=new" {
		t.Fatal(c.calls, c.env)
	}
	c.mu.Unlock()
	if !d.busy || !strings.Contains(d.notice, "pending") {
		t.Fatal(d.notice)
	}
	close(c.blocked)
	eventually(t, func() bool { d.drain(); return !d.busy })
	d.connected = false
	d.action("stop")
	if d.busy {
		t.Fatal("disconnected mutation allowed")
	}
	d.connected = true
	d.help = true
	d.action("start")
	if d.busy {
		t.Fatal("popup mutation allowed")
	}
	d.help = false
	d.setFocus(projectPane)
	d.setFocus(detailPane)
	d.action("restart")
	if d.busy {
		t.Fatal("project details started a command")
	}
}
func TestRemovedMovedAndKindAwareOutcomes(t *testing.T) {
	d := testDashboard(t, &fakeClient{})
	item := d.state.Commands[0]
	item.Run.ID = "old"
	item.Run.Lifecycle = model.Running
	def := item.Run.Definition
	def.Kind = model.Task
	def.Command = "new"
	item.Definition = &def
	d.state.Commands[0] = item
	if displayKind(item) != model.Service || !strings.Contains(itemLabel(item), "moved") {
		t.Fatal(itemLabel(item))
	}
	item.Run.Lifecycle = model.Exited
	code := 0
	item.Run.Outcome = &model.Outcome{Kind: model.Success, ExitCode: &code}
	if displayKind(item) != model.Task || outcome(item.Run) != "exited (0)" {
		t.Fatal(itemLabel(item))
	}
	if !strings.Contains(commandDetails(item), "next run only") {
		t.Fatal(commandDetails(item))
	}
	item.Run.Definition.Kind = model.Task
	if outcome(item.Run) != "completed (0)" {
		t.Fatal(outcome(item.Run))
	}
	code = 4
	item.Run.Outcome.Kind = model.NonzeroExit
	if outcome(item.Run) != "failed (4)" {
		t.Fatal(outcome(item.Run))
	}
	item.Run.StopRequested = true
	if outcome(item.Run) != "stopped (4)" {
		t.Fatal("fabricated stop outcome")
	}
	item.Run.Lifecycle = model.Running
	item.Removed = true
	item.Definition = nil
	d.state.Commands[0] = item
	d.reselect()
	d.setFocus(tasksPane)
	d.selected[tasksPane] = "api"
	d.action("restart")
	if d.busy || !strings.Contains(d.notice, "only stop") {
		t.Fatal(d.notice)
	}
}
func TestViewCancellationAndStaleRunResponses(t *testing.T) {
	c := &fakeClient{reads: make(chan string, 10)}
	d := testDashboard(t, c)
	d.state.Commands[0].Run.ID = "run-one"
	d.state.Commands[1].Run.ID = "run-two"
	d.spawn(d.pollLogs)
	d.setFocus(servicesPane)
	select {
	case alias := <-c.reads:
		if alias != "api" {
			t.Fatal(alias)
		}
	case <-time.After(time.Second):
		t.Fatal("no initial tail")
	}
	generation := d.generation
	d.move(1)
	select {
	case alias := <-c.reads:
		if alias != "worker" {
			t.Fatal(alias)
		}
	case <-time.After(time.Second):
		t.Fatal("old read blocked view switch")
	}
	d.events <- event{generation: generation, read: &model.LogRead{RunID: "run-one", Data: []byte("OLD"), Next: 3}}
	d.drain()
	if strings.Contains(bufferText(&d.buffer), "OLD") {
		t.Fatal("stale output applied")
	}
	d.events <- event{generation: d.generation, read: &model.LogRead{RunID: "run-two", Next: 3, Records: []model.LogRecord{{Data: []byte("NEW")}}}}
	d.drain()
	if bufferText(&d.buffer) != "NEW" {
		t.Fatal(bufferText(&d.buffer))
	}
	d.setFocus(detailPane)
	d.move(-1)
	if d.follow {
		t.Fatal("manual scroll did not pause")
	}
	d.events <- event{generation: d.generation, after: 3, read: &model.LogRead{RunID: "run-two", Next: 7, Records: []model.LogRecord{{Cursor: 3, Data: []byte("more")}}}}
	d.drain()
	if d.follow || !strings.HasSuffix(bufferText(&d.buffer), "more") {
		t.Fatal("pause blocked ingestion")
	}
	d.tab = 1
	d.syncLogView()
	if d.jobActive {
		t.Fatal("details kept log job")
	}
	d.state.Commands[1].Run.ID = "replacement"
	d.tab = 0
	d.syncLogView()
	if !d.follow || len(d.buffer.lines) > 0 || d.cursor != 0 {
		t.Fatal("replacement inherited old logs")
	}
}
func TestHistoricalMetadataIsSanitizedPerField(t *testing.T) {
	item := fixtureState().Commands[0]
	item.Run.ID = "old\x1b]52;c;unterminated"
	item.Run.Definition.Cwd = "/safe\x1b[31"
	item.Run.Error = "problem\x1b[2J\a"
	text := commandDetails(item)
	checkSafe(t, text)
	if strings.Contains(text, "\x1b") || !strings.Contains(text, "Run ID: old\nPID:") || !strings.Contains(text, "Command:\necho api") {
		t.Fatal(text)
	}
}

func TestConnectionFailurePreservesStateAndDoesNotRetryActions(t *testing.T) {
	d := testDashboard(t, &fakeClient{})
	d.events <- event{state: &model.State{}, err: errors.New("supervisor lost")}
	d.drain()
	if d.connected || d.state.Project.Name != "demo" || d.connectionError != "supervisor lost" {
		t.Fatal("lost cached state")
	}
	d.events <- event{state: func() *model.State { s := fixtureState(); return &s }()}
	d.drain()
	if !d.connected {
		t.Fatal("read reconnect did not recover")
	}
	d.busy = true
	d.events <- event{action: "restart", alias: "api", err: errors.New("response lost")}
	d.drain()
	if d.busy || !strings.Contains(d.notice, "query state before retrying") {
		t.Fatal(d.notice)
	}
}
func TestFullMailboxAndBlockedRequestShutdownIsBounded(t *testing.T) {
	c := &fakeClient{blocked: make(chan struct{})}
	d := testDashboard(t, c)
	for range cap(d.events) {
		d.events <- event{err: errors.New("queued")}
	}
	d.spawn(d.pollState)
	d.state.Commands[0].Run.ID = "one"
	d.spawn(d.pollLogs)
	d.setFocus(servicesPane)
	d.action("restart")
	d.cancel()
	done := make(chan struct{})
	go func() { d.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dashboard exit blocked behind requests or full mailbox")
	}
}

func TestHeadlessLayoutEmptyStatesHelpAndViewportBound(t *testing.T) {
	g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: 100, Height: 30, OutputMode: gocui.OutputTrue, SupportOverlaps: true})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	d := testDashboard(t, &fakeClient{})
	d.state.Project.Name = "safe\x1b]0;bad\a"
	d.state.Commands = nil
	if err := d.layout(g); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"project", "services", "tasks", "detail", "footer"} {
		v, err := g.View(name)
		if err != nil {
			t.Fatal(err)
		}
		checkSafe(t, v.Buffer())
	}
	v, _ := g.View("services")
	if !strings.Contains(v.Buffer(), "no configured services") {
		t.Fatal(v.Buffer())
	}
	d.help = true
	if err := d.layout(g); err != nil {
		t.Fatal(err)
	}
	v, _ = g.View("help")
	if !strings.Contains(v.Buffer(), "SIGTERM") {
		t.Fatal(v.Buffer())
	}
	d.help = false
	d.state = fixtureState()
	d.state.Commands[0].Run.ID = "one"
	d.setFocus(servicesPane)
	d.loaded = true
	d.buffer.append([]byte(strings.Repeat("x", 3*MaxBufferBytes)), time.Time{})
	if err := d.layout(g); err != nil {
		t.Fatal(err)
	}
	v, _ = g.View("detail")
	if len(v.Buffer()) > 100*30 {
		t.Fatal("gocui retained off-screen cells", len(v.Buffer()))
	}
	for width := 0; width < 160; width++ {
		for height := 0; height < 50; height++ {
			areas := geometry(width, height)
			if width < wideWidth || height < wideHeight {
				if areas != nil {
					t.Fatal(width, height)
				}
				continue
			}
			for name, r := range areas {
				if r.x1 <= r.x0+1 || r.y1 <= r.y0+1 {
					t.Fatal(name, r, width, height)
				}
			}
		}
	}
}

package gui

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jesseduffield/gocui"
	"github.com/nullco/lazyrun/internal/model"
)

func nextUpdate(t *testing.T, updates <-chan func(*gocui.Gui) error) func(*gocui.Gui) error {
	t.Helper()
	select {
	case update := <-updates:
		return update
	case <-time.After(3 * time.Second):
		t.Fatal("missing UI update")
		return nil
	}
}

func TestUpdateLoopDoesNotRedrawWhileIdle(t *testing.T) {
	d := testDashboard(t, &fakeClient{})
	updates := make(chan func(*gocui.Gui) error, 2)
	d.spawn(func() { d.updateLoop(func(fn func(*gocui.Gui) error) { updates <- fn }) })
	select {
	case <-updates:
		t.Fatal("idle dashboard scheduled a redraw")
	case <-time.After(2 * updateInterval):
	}
	d.cancel()
	if err := nextUpdate(t, updates)(nil); err != gocui.ErrQuit {
		t.Fatalf("cancellation did not wake MainLoop: %v", err)
	}
}

func TestUpdateLoopBoundsCallbacksAndPreservesPendingEvents(t *testing.T) {
	d := testDashboard(t, &fakeClient{})
	updates := make(chan func(*gocui.Gui) error, 2)
	d.spawn(func() { d.updateLoop(func(fn func(*gocui.Gui) error) { updates <- fn }) })
	d.send(d.ctx, event{action: "start", alias: "first"})
	first := nextUpdate(t, updates)
	for range cap(d.events) - 1 {
		d.send(d.ctx, event{action: "start", alias: "queued"})
	}
	select {
	case <-updates:
		t.Fatal("scheduled another callback before acknowledgement")
	case <-time.After(updateInterval):
	}
	// This producer cannot enqueue until the callback starts draining. Since a
	// callback consumes at most the mailbox capacity, its result needs a wakeup
	// after acknowledgement, regardless of whether it arrives during the drain.
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		d.send(d.ctx, event{action: "stop", alias: "last"})
	}()
	started := time.Now()
	if err := first(nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("producer remained blocked after drain")
	}
	second := nextUpdate(t, updates)
	if time.Since(started) < updateInterval/2 {
		t.Fatal("active stream bypassed the redraw rate limit")
	}
	if err := second(nil); err != nil {
		t.Fatal(err)
	}
	if d.notice != "stop requested for last" {
		t.Fatal("lost event queued during callback", d.notice)
	}
	select {
	case <-updates:
		t.Fatal("stale wakeup scheduled an empty redraw")
	case <-time.After(2 * updateInterval):
	}
}

func TestUpdateLoopCancellationDoesNotWaitForCallback(t *testing.T) {
	d := testDashboard(t, &fakeClient{})
	updates := make(chan func(*gocui.Gui) error, 2)
	d.spawn(func() { d.updateLoop(func(fn func(*gocui.Gui) error) { updates <- fn }) })
	d.send(d.ctx, event{action: "start", alias: "api"})
	_ = nextUpdate(t, updates) // Simulate MainLoop quitting before executing it.
	d.cancel()
	done := make(chan struct{})
	go func() { d.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown waited for a stranded callback")
	}
	if err := nextUpdate(t, updates)(nil); err != gocui.ErrQuit {
		t.Fatalf("missing shutdown wakeup: %v", err)
	}
}

func TestLogReadChangedPreservesMetadataAndOutput(t *testing.T) {
	base := model.LogRead{RunID: "run", First: 2, Next: 8, End: 8}
	if logReadChanged(base, base) {
		t.Fatal("identical empty read changed")
	}
	previous := base
	previous.Data = []byte("old")
	previous.Records = []model.LogRecord{{Data: []byte("old")}}
	if logReadChanged(previous, base) {
		t.Fatal("unchanged empty read after output changed")
	}
	changes := map[string]func(*model.LogRead){
		"run":         func(r *model.LogRead) { r.RunID = "replacement" },
		"first":       func(r *model.LogRead) { r.First++ },
		"next":        func(r *model.LogRead) { r.Next++ },
		"end":         func(r *model.LogRead) { r.End++ },
		"truncated":   func(r *model.LogRead) { r.Truncated = true },
		"unavailable": func(r *model.LogRead) { r.Unavailable = true },
		"warning":     func(r *model.LogRead) { r.Error = "log retention failed" },
		"data":        func(r *model.LogRead) { r.Data = []byte("new") },
		"records":     func(r *model.LogRead) { r.Records = []model.LogRecord{{Data: []byte("new")}} },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			r := base
			change(&r)
			if !logReadChanged(base, r) {
				t.Fatal("suppressed changed log read")
			}
		})
	}
	if !sameError(errors.New("lost"), errors.New("lost")) || sameError(nil, errors.New("")) || sameError(errors.New("lost"), nil) {
		t.Fatal("error comparison lost an error/recovery transition")
	}
}

type stateReply struct {
	state model.State
	err   error
}
type logReply struct {
	read model.LogRead
	err  error
}
type requestedLog struct {
	initial bool
	after   uint64
	reply   chan logReply
}
type pollingClient struct {
	fakeClient
	states chan chan stateReply
	logs   chan requestedLog
}

func (c *pollingClient) State(ctx context.Context) (model.State, error) {
	reply := make(chan stateReply)
	select {
	case c.states <- reply:
	case <-ctx.Done():
		return model.State{}, ctx.Err()
	}
	select {
	case result := <-reply:
		return result.state, result.err
	case <-ctx.Done():
		return model.State{}, ctx.Err()
	}
}
func (c *pollingClient) read(ctx context.Context, initial bool, after uint64) (model.LogRead, error) {
	reply := make(chan logReply)
	select {
	case c.logs <- requestedLog{initial: initial, after: after, reply: reply}:
	case <-ctx.Done():
		return model.LogRead{}, ctx.Err()
	}
	select {
	case result := <-reply:
		return result.read, result.err
	case <-ctx.Done():
		return model.LogRead{}, ctx.Err()
	}
}
func (c *pollingClient) Logs(ctx context.Context, _, _ string, after uint64, _ int) (model.LogRead, error) {
	return c.read(ctx, false, after)
}
func (c *pollingClient) TailLogs(ctx context.Context, _, _ string, _, _ int) (model.LogRead, error) {
	return c.read(ctx, true, 0)
}

func nextRequest[T any](t *testing.T, requests <-chan T) T {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(3 * time.Second):
		t.Fatal("missing polling request")
		var zero T
		return zero
	}
}

func assertMailbox(t *testing.T, d *dashboard, changed bool) event {
	t.Helper()
	if !changed {
		select {
		case e := <-d.events:
			t.Fatalf("unchanged response queued an event: %+v", e)
		default:
		}
		return event{}
	}
	select {
	case e := <-d.events:
		return e
	default:
		t.Fatal("changed response did not queue an event")
		return event{}
	}
}

func TestPollStateSuppressesDuplicatesButPreservesRecovery(t *testing.T) {
	c := &pollingClient{states: make(chan chan stateReply)}
	d := newDashboard(context.Background(), c, fixtureState(), Options{})
	t.Cleanup(func() { d.cancel(); d.workers.Wait() })
	d.spawn(func() { d.pollState(c) })
	changed := fixtureState()
	changed.Commands[0].Run.Lifecycle = model.Running
	steps := []struct {
		name    string
		reply   stateReply
		changed bool
	}{
		{"initial", stateReply{state: fixtureState()}, true},
		{"duplicate", stateReply{state: fixtureState()}, false},
		{"lifecycle", stateReply{state: changed}, true},
		{"disconnected", stateReply{err: errors.New("lost")}, true},
		{"same error", stateReply{err: errors.New("lost")}, false},
		{"recovery", stateReply{state: changed}, true},
		{"unchanged recovery", stateReply{state: changed}, false},
	}
	request := nextRequest(t, c.states)
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			request <- step.reply
			// The next request is a barrier: the previous result was processed.
			request = nextRequest(t, c.states)
			e := assertMailbox(t, d, step.changed)
			if step.changed && (e.state == nil || !sameError(e.err, step.reply.err) || !reflect.DeepEqual(*e.state, step.reply.state)) {
				t.Fatal("changed state response was altered", e)
			}
		})
	}
}

func TestPollLogsSuppressesEmptyReadsButPreservesOutputAndErrors(t *testing.T) {
	c := &pollingClient{logs: make(chan requestedLog)}
	d := newDashboard(context.Background(), c, fixtureState(), Options{})
	t.Cleanup(func() { d.cancel(); d.workers.Wait() })
	d.state.Commands[0].Run.ID = "run"
	d.setFocus(servicesPane)
	d.spawn(d.pollLogs)
	empty := model.LogRead{RunID: "run"}
	data := model.LogRead{RunID: "run", Data: []byte("new"), Records: []model.LogRecord{{Data: []byte("new")}}, Next: 3, End: 3}
	afterData := data
	afterData.Data, afterData.Records = nil, nil
	warning := afterData
	warning.Error = "retention failed"
	steps := []struct {
		name    string
		reply   logReply
		changed bool
	}{
		{"initial", logReply{read: empty}, true},
		{"empty", logReply{read: empty}, false},
		{"output", logReply{read: data}, true},
		{"empty after output", logReply{read: afterData}, false},
		{"warning", logReply{read: warning}, true},
		{"same warning", logReply{read: warning}, false},
		{"request error", logReply{err: errors.New("lost")}, true},
		{"same request error", logReply{err: errors.New("lost")}, false},
		{"recovery", logReply{read: warning}, true},
	}
	request := nextRequest(t, c.logs)
	if !request.initial {
		t.Fatal("first read was not a tail request")
	}
	var after uint64
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			request.reply <- step.reply
			request = nextRequest(t, c.logs)
			if step.reply.err == nil {
				after = step.reply.read.Next
			}
			if request.initial || request.after != after {
				t.Fatalf("lost cursor progression: %+v", request)
			}
			e := assertMailbox(t, d, step.changed)
			if step.changed && (e.read == nil || !sameError(e.err, step.reply.err) || !reflect.DeepEqual(*e.read, step.reply.read)) {
				t.Fatal("changed log response was altered", e)
			}
		})
	}
}

func TestExitStateFinishesLogsWithoutAnotherRead(t *testing.T) {
	d := testDashboard(t, &fakeClient{})
	d.state.Commands[0].Run.ID = "run"
	d.state.Commands[0].Run.Lifecycle = model.Running
	d.setFocus(servicesPane)
	d.events <- event{generation: d.generation, initial: true, read: &model.LogRead{RunID: "run", Records: []model.LogRecord{{Data: []byte{0xe2, 0x82}}}, Next: 2, End: 2}}
	d.drain()
	if bufferText(&d.buffer) != "" {
		t.Fatal("incomplete UTF-8 rendered before exit")
	}
	state := fixtureState()
	state.Commands[0].Run = d.state.Commands[0].Run.Clone()
	state.Commands[0].Run.Lifecycle = model.Exited
	state.Commands[0].Run.Outcome = &model.Outcome{Kind: model.Success}
	state.Commands[0].Run.LogEnd = 2
	d.events <- event{state: &state}
	d.drain()
	if bufferText(&d.buffer) != "�" {
		t.Fatal("exit state did not finish pending UTF-8", bufferText(&d.buffer))
	}
	d.drain()
	if bufferText(&d.buffer) != "�" {
		t.Fatal("finished pending UTF-8 more than once", bufferText(&d.buffer))
	}
}

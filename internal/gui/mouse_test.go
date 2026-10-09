package gui

import (
	"fmt"
	"testing"
	"time"

	"github.com/jesseduffield/gocui"
	"github.com/nullco/lazyrun/internal/model"
)

func mouseDashboard(t *testing.T) (*dashboard, *gocui.Gui, *fakeClient) {
	t.Helper()
	g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: 100, Height: 30, OutputMode: gocui.OutputTrue, SupportOverlaps: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	c := &fakeClient{}
	d := testDashboard(t, c)
	if err := d.bindings(g); err != nil {
		t.Fatal(err)
	}
	if !g.Mouse {
		t.Fatal("mouse reporting not enabled")
	}
	if err := d.layout(g); err != nil {
		t.Fatal(err)
	}
	return d, g, c
}

func TestMouseClickFocusSelectionAndGuards(t *testing.T) {
	d, g, c := mouseDashboard(t)
	d.state.Commands[1].Run.ID = "worker-run"
	d.mouseClick(g, servicesPane, 0, 1)
	if d.focus != servicesPane || d.owner != servicesPane || d.selected[servicesPane] != "worker" || d.logAlias != "worker" || d.logRun != "worker-run" {
		t.Fatal("row click did not select the visible command", d.selected, d.logAlias, d.logRun)
	}
	d.mouseClick(g, detailPane, 0, 0)
	if d.focus != detailPane || d.owner != servicesPane {
		t.Fatal("details click changed its owner")
	}
	d.mouseClick(g, tasksPane, 0, 0)
	if d.focus != tasksPane || d.owner != tasksPane || d.selected[tasksPane] != "tests" {
		t.Fatal("tasks click did not focus/select")
	}
	for _, point := range [][2]int{{-1, 0}, {0, -1}, {100, 0}, {0, 5}} {
		d.mouseClick(g, servicesPane, point[0], point[1])
		if d.focus != servicesPane || d.selected[servicesPane] != "worker" {
			t.Fatal("border/blank click selected a different row")
		}
	}
	d.mouseClick(g, projectPane, 0, 0)
	if d.focus != projectPane || d.owner != projectPane {
		t.Fatal("project click did not clear command context")
	}
	for _, guard := range []string{"help", "small"} {
		d.help, d.small = guard == "help", guard == "small"
		d.mouseClick(g, tasksPane, 0, 0)
		d.mouseWheel(servicesPane, 3)
		if d.focus != projectPane || d.owner != projectPane {
			t.Fatal("modal/minimum click leaked through", guard)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) != 0 || d.busy {
		t.Fatal("mouse navigation executed a lifecycle action", c.calls)
	}
}

func TestMouseSelectsLastRenderedAliasAcrossScrollAndStateChanges(t *testing.T) {
	d, g, _ := mouseDashboard(t)
	// A reordered state may arrive before the next redraw. Row 1 still shows
	// worker, even though it is now index 0 in the current state.
	d.state.Commands[0], d.state.Commands[1] = d.state.Commands[1], d.state.Commands[0]
	d.mouseClick(g, servicesPane, 0, 1)
	if d.selected[servicesPane] != "worker" {
		t.Fatal("click hit the new order instead of the displayed row")
	}
	// A disappeared row must not select the replacement at its former index.
	d.state.Commands = d.state.Commands[1:]
	d.reselect()
	d.mouseClick(g, servicesPane, 0, 1)
	if d.selected[servicesPane] != "api" {
		t.Fatal("click resurrected a disappeared alias")
	}
	for i := range 30 {
		def := model.Definition{Alias: fmt.Sprintf("extra-%02d", i), Kind: model.Service}
		d.state.Commands = append(d.state.Commands, model.CommandState{Definition: &def, Run: model.Run{Definition: def, Lifecycle: model.NotStarted}})
	}
	d.selected[servicesPane] = "extra-29"
	if err := d.layout(g); err != nil {
		t.Fatal(err)
	}
	v, _ := g.View("services")
	_, height := v.Size()
	if len(d.visibleAliases[servicesPane]) != height {
		t.Fatal("hit map did not match visible scroll window")
	}
	first := d.visibleAliases[servicesPane][0]
	d.mouseClick(g, servicesPane, 0, 0)
	if d.selected[servicesPane] != first {
		t.Fatal("scrolled click selected an offscreen index", d.selected, first)
	}
	d.state.Commands = nil
	if err := d.layout(g); err != nil {
		t.Fatal(err)
	}
	if len(d.visibleAliases[servicesPane]) != 0 || len(d.visibleAliases[tasksPane]) != 0 {
		t.Fatal("empty panes retained old hit targets")
	}
}

func TestMouseWheelNavigatesHoveredPaneAndPausesLogs(t *testing.T) {
	d, _, c := mouseDashboard(t)
	d.mouseWheel(servicesPane, 3)
	if d.focus != servicesPane || d.selected[servicesPane] != "worker" {
		t.Fatal("wheel did not target hovered list")
	}
	d.mouseWheel(servicesPane, -3)
	if d.selected[servicesPane] != "api" {
		t.Fatal("wheel did not clamp at first row")
	}
	d.buffer.append([]byte("a\nb\nc\nd\ne\nf\n"), time.Time{})
	d.follow = true
	d.mouseWheel(detailPane, 3)
	if d.focus != detailPane || d.owner != servicesPane || d.follow || d.top != 3 {
		t.Fatal("log wheel did not pause/scroll")
	}
	d.mouseWheel(detailPane, -3)
	if d.top != 0 {
		t.Fatal("log scroll did not clamp")
	}
	d.tab = 1
	d.mouseWheel(detailPane, 3)
	if d.detailTop != 3 {
		t.Fatal("details wheel did not scroll")
	}
	if len(c.calls) != 0 {
		t.Fatal("wheel executed a command")
	}
}

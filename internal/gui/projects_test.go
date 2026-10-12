package gui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/jesseduffield/gocui"
	"github.com/nullco/lazyrun/internal/model"
)

func multiDashboard(t *testing.T) (*dashboard, *fakeClient, *fakeClient) {
	t.Helper()
	first, second := &fakeClient{}, &fakeClient{}
	state := fixtureState()
	state.Project.ID = "first"
	d := newDashboard(context.Background(), first, state, Options{ProjectClient: func(id string) Client {
		if id == "second" {
			return second
		}
		return first
	}})
	d.projects = []model.ProjectSummary{{ID: "first", Name: "demo", Root: "/demo"}, {ID: "second", Name: "other", Root: "/other"}}
	t.Cleanup(func() { d.cancel(); d.workers.Wait() })
	return d, first, second
}

func TestProjectSwitchRejectsLateResponsesAndRoutesActions(t *testing.T) {
	d, first, second := multiDashboard(t)
	d.state.Commands[0].Run.ID = "old-run"
	d.setFocus(servicesPane)
	oldLogGeneration, oldSearchGeneration := d.generation, d.searchGeneration
	d.buffer.append([]byte("old output"), time.Time{})
	d.setFocus(projectPane)
	d.move(1)
	if d.state.Project.ID != "second" || d.client != second || !d.connected || !d.loadingProject || len(d.state.Commands) != 0 || len(d.buffer.lines) != 0 {
		t.Fatal("project switch retained old view")
	}
	scope := <-d.stateScopes
	if scope.client != second || scope.generation != d.stateGeneration {
		t.Fatal("wrong polling scope")
	}
	stale := fixtureState()
	stale.Project.ID = "first"
	d.events <- event{state: &stale, scopedState: true, stateGeneration: 0, err: errors.New("old disconnect")}
	d.events <- event{read: &model.LogRead{RunID: "old-run", Records: []model.LogRecord{{Data: []byte("late output")}}}, generation: oldLogGeneration}
	d.events <- event{search: &model.LogSearchResult{RunID: "old-run"}, searchGeneration: oldSearchGeneration}
	d.drain()
	if d.state.Project.ID != "second" || !d.loadingProject || !d.connected || d.connectionError != "" || len(d.buffer.lines) != 0 {
		t.Fatal("late result overwrote new selection")
	}
	state := fixtureState()
	state.Project.ID, state.Project.Name, state.Project.Root = "second", "other", "/other"
	d.events <- event{state: &state, scopedState: true, stateGeneration: d.stateGeneration, projects: d.projects}
	d.drain()
	if !d.connected || d.loadingProject || d.selected[servicesPane] != "api" {
		t.Fatal("new state did not load")
	}
	d.setFocus(servicesPane)
	d.action("start")
	d.workers.Wait()
	d.drain()
	if len(first.calls) != 0 || len(second.calls) != 1 || second.calls[0] != "start api" {
		t.Fatal("action sent to wrong project", first.calls, second.calls)
	}
}

func TestProjectSwitchShowsLoadingUntilStateArrives(t *testing.T) {
	for _, size := range [][2]int{{100, 30}, {100, 17}, {80, 20}, {40, 10}} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%dx%d/failure=%t", size[0], size[1], failed), func(t *testing.T) {
				d, first, second := multiDashboard(t)
				g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: size[0], Height: size[1], OutputMode: gocui.OutputTrue, SupportOverlaps: true})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(g.Close)
				d.selectProject("second")
				d.setFocus(detailPane)
				if err := d.layout(g); err != nil {
					t.Fatal(err)
				}
				project, _ := g.View("project")
				label := project.Title + plain(project.Buffer())
				if strings.Contains(label, "Loading") || strings.Contains(label, "DISCONNECTED") {
					t.Fatal("project pane showed loading or connection status", label)
				}
				if size == [2]int{100, 30} {
					detail, _ := g.View("detail")
					if !strings.Contains(detail.Buffer(), "Connection: Loading project...") {
						t.Fatal("project details lost loading status", detail.Buffer())
					}
				}
				d.setFocus(servicesPane)
				if err := d.layout(g); err != nil {
					t.Fatal(err)
				}
				services, _ := g.View("services")
				if !strings.Contains(services.Buffer(), "Loading project...") {
					t.Fatal("loading commands appeared empty", services.Buffer())
				}
				d.action("start")
				d.reloadConfig()
				if d.busy || d.reloading || len(first.calls) != 0 || len(second.calls) != 0 || strings.Contains(d.footerHints(200), "R: reload") {
					t.Fatal("loading project allowed a mutation")
				}
				state := fixtureState()
				state.Project.ID, state.Project.Name, state.Project.Root = "second", "other", "/other"
				e := event{state: &state, scopedState: true, stateGeneration: d.stateGeneration}
				if failed {
					e.err = errors.New("supervisor unavailable")
				}
				d.events <- e
				d.drain()
				if d.loadingProject || d.connected == failed {
					t.Fatal("project response did not finish loading", d.loadingProject, d.connected)
				}
				if err := d.layout(g); err != nil {
					t.Fatal(err)
				}
				project, _ = g.View("project")
				label = project.Title + plain(project.Buffer())
				if strings.Contains(label, "Loading") || strings.Contains(label, "DISCONNECTED") {
					t.Fatal("project pane showed loading or connection status after response", label)
				}
				if size == [2]int{100, 30} {
					d.setFocus(projectPane)
					d.setFocus(detailPane)
					if err := d.layout(g); err != nil {
						t.Fatal(err)
					}
					detail, _ := g.View("detail")
					status := "connected"
					if failed {
						status = "DISCONNECTED: supervisor unavailable"
					}
					if !strings.Contains(detail.Buffer(), "Connection: "+status) {
						t.Fatal("project details lost connection status after response", detail.Buffer())
					}
				}
			})
		}
	}
}

func TestProjectSwitchCoalescesAndWaitsForMutations(t *testing.T) {
	d, first, _ := multiDashboard(t)
	for _, pending := range []string{"action", "reload"} {
		d.busy, d.reloading = pending == "action", pending == "reload"
		d.selectProject("second")
		if d.state.Project.ID != "first" {
			t.Fatal("switched during pending mutation")
		}
	}
	d.busy, d.reloading = false, false
	d.selectProject("second")
	d.selectProject("first")
	if len(d.stateScopes) != 1 {
		t.Fatal("queued stale scopes")
	}
	scope := <-d.stateScopes
	if scope.client != first || scope.generation != 2 {
		t.Fatal("did not coalesce switches", scope)
	}
}

func TestProjectListRenderingNavigationAndMouse(t *testing.T) {
	for _, size := range [][2]int{{100, 30}, {120, 24}, {80, 20}, {40, 10}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			d, _, _ := multiDashboard(t)
			d.projects = nil
			for i := range 30 {
				d.projects = append(d.projects, model.ProjectSummary{ID: fmt.Sprint(i), Name: fmt.Sprintf("project-%02d", i), Root: fmt.Sprintf("/project-%02d", i)})
			}
			d.state.Project.ID = "0"
			g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: size[0], Height: size[1], OutputMode: gocui.OutputTrue, SupportOverlaps: true})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			d.move(29)
			if d.state.Project.ID != "29" {
				t.Fatal("keyboard did not switch project")
			}
			if err := d.layout(g); err != nil {
				t.Fatal(err)
			}
			v, _ := g.View("project")
			if !strings.Contains(plain(v.Buffer()), "project-29") || strings.Contains(plain(v.Buffer()), "> ") || len(d.visibleAliases[projectPane]) < 1 {
				t.Fatal("selected project not visible", v.Buffer())
			}
			selected := d.visibleAliases[projectPane][0]
			d.mouseClick(g, projectPane, 1, 0)
			if d.state.Project.ID != selected {
				t.Fatal("mouse did not select rendered project", selected, d.state.Project.ID)
			}
			d.mouseClick(g, projectPane, -1, -1)
			if d.state.Project.ID != selected {
				t.Fatal("border click selected a project")
			}
		})
	}
}

func TestProjectPaneCompactsWhenUnfocusedAndKeepsSelection(t *testing.T) {
	for _, size := range [][2]int{{100, 30}, {120, 24}, {100, 17}, {80, 20}, {40, 10}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			d, _, _ := multiDashboard(t)
			for i := range 6 {
				d.projects = append(d.projects, model.ProjectSummary{ID: fmt.Sprint(i), Name: fmt.Sprintf("extra-%d", i)})
			}
			d.selectProject("second")
			d.connected = true
			generation := d.stateGeneration
			g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: size[0], Height: size[1], OutputMode: gocui.OutputTrue, SupportOverlaps: true})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			for _, focus := range []pane{servicesPane, tasksPane, detailPane} {
				d.setFocus(projectPane)
				if err := d.layout(g); err != nil {
					t.Fatal(err)
				}
				project, _ := g.View("project")
				_, expandedHeight := project.Size()
				d.setFocus(focus)
				if err := d.layout(g); err != nil {
					t.Fatal(err)
				}
				project, _ = g.View("project")
				text := plain(project.Buffer())
				if !strings.Contains(text, "other") || strings.Contains(text, "demo") || strings.Contains(text, "extra-") {
					t.Fatal("unfocused pane must show only the selected project", focus, text)
				}
				if !d.collapsed[projectPane] {
					if strings.TrimSuffix(text, "\n") != "other" || len(d.visibleAliases[projectPane]) != 1 || d.visibleAliases[projectPane][0] != "second" {
						t.Fatal("compact display must show only the selected project", text, d.visibleAliases[projectPane])
					}
					if size[0] >= wideWidth && size[1] >= wideHeight {
						_, compactHeight := project.Size()
						if compactHeight != 1 || compactHeight >= expandedHeight {
							t.Fatal("compact pane did not give space back to commands", compactHeight, expandedHeight)
						}
					}
				} else if len(d.visibleAliases[projectPane]) != 0 {
					t.Fatal("collapsed header retained clickable list rows")
				}
				d.setFocus(projectPane)
				if err := d.layout(g); err != nil {
					t.Fatal(err)
				}
				project, _ = g.View("project")
				_, restoredHeight := project.Size()
				if restoredHeight != expandedHeight || !strings.Contains(plain(project.Buffer()), "other") || d.state.Project.ID != "second" || d.stateGeneration != generation {
					t.Fatal("returning to Projects did not restore the list and selection")
				}
				if expandedHeight > 1 && len(d.visibleAliases[projectPane]) < 2 {
					t.Fatal("project list remained compact after regaining focus")
				}
			}
		})
	}
}

func TestCompactProjectMouseAndWheelExpandBeforeNavigation(t *testing.T) {
	for _, size := range [][2]int{{100, 30}, {100, 17}, {80, 20}, {40, 10}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			d, _, _ := multiDashboard(t)
			d.selectProject("second")
			g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: size[0], Height: size[1], OutputMode: gocui.OutputTrue, SupportOverlaps: true})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			d.setFocus(tasksPane)
			if err := d.layout(g); err != nil {
				t.Fatal(err)
			}
			d.mouseClick(g, projectPane, 0, 0)
			if d.focus != projectPane || d.state.Project.ID != "second" {
				t.Fatal("clicking compact display changed selection")
			}
			d.setFocus(tasksPane)
			if err := d.layout(g); err != nil {
				t.Fatal(err)
			}
			d.mouseWheel(projectPane, -1)
			if d.focus != projectPane || d.state.Project.ID != "second" {
				t.Fatal("first wheel event navigated a hidden project list")
			}
			if err := d.layout(g); err != nil {
				t.Fatal(err)
			}
			d.mouseWheel(projectPane, -1)
			if d.state.Project.ID != "first" {
				t.Fatal("expanded project list did not respond to wheel")
			}
		})
	}
}

func TestCompactProjectSelectionSurvivesResize(t *testing.T) {
	d, _, _ := multiDashboard(t)
	d.selectProject("second")
	d.setFocus(servicesPane)
	g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: 120, Height: 30, OutputMode: gocui.OutputTrue, SupportOverlaps: true})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	g.SetManagerFunc(d.layout)
	screen := gocui.Screen.(tcell.SimulationScreen)
	for _, size := range [][2]int{{120, 30}, {40, 10}, {100, 17}, {80, 20}, {100, 24}, {120, 30}} {
		screen.SetSize(size[0], size[1])
		if err := g.ForceLayoutAndRedraw(); err != nil {
			t.Fatal(err)
		}
		project, _ := g.View("project")
		if d.state.Project.ID != "second" || !strings.Contains(plain(project.Buffer()), "other") || strings.Contains(plain(project.Buffer()), "demo") {
			t.Fatal("resize changed compact project or exposed other projects", size, project.Buffer())
		}
	}
	d.setFocus(projectPane)
	if err := g.ForceLayoutAndRedraw(); err != nil {
		t.Fatal(err)
	}
	project, _ := g.View("project")
	if len(d.visibleAliases[projectPane]) != 2 || !strings.Contains(plain(project.Buffer()), "demo") || !strings.Contains(plain(project.Buffer()), "other") {
		t.Fatal("resize prevented project list restoration", project.Buffer())
	}
}

type projectPollingClient struct {
	*pollingClient
	projects []model.ProjectSummary
}

func (c *projectPollingClient) Projects(context.Context) ([]model.ProjectSummary, error) {
	return c.projects, nil
}

func TestProjectPollingSwitchIgnoresInFlightOldState(t *testing.T) {
	first := &pollingClient{states: make(chan chan stateReply)}
	second := &projectPollingClient{pollingClient: &pollingClient{states: make(chan chan stateReply)}, projects: []model.ProjectSummary{{ID: "first"}, {ID: "second"}}}
	state := fixtureState()
	state.Project.ID = "first"
	d := newDashboard(context.Background(), first, state, Options{ProjectClient: func(string) Client { return second }})
	d.projects = second.projects
	t.Cleanup(func() { d.cancel(); d.workers.Wait() })
	d.spawn(func() { d.pollState(first) })
	oldRequest := nextRequest(t, first.states)
	d.selectProject("second")
	oldRequest <- stateReply{state: state}
	newRequest := nextRequest(t, second.states)
	d.drain()
	if d.state.Project.ID != "second" || !d.connected || !d.loadingProject {
		t.Fatal("in-flight old state was accepted")
	}
	state.Project.ID = "second"
	newRequest <- stateReply{state: state}
	_ = nextRequest(t, second.states) // Barrier: the new result has been queued.
	d.drain()
	if !d.connected || d.loadingProject || d.state.Project.ID != "second" || len(d.projects) != 2 {
		t.Fatal("new scope did not load")
	}
}

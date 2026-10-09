package gui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/jesseduffield/gocui"
	"github.com/mattn/go-runewidth"
	"github.com/nullco/lazyrun/internal/model"
)

func TestResponsiveGeometryTilesScreenAndExpandsFocusedPane(t *testing.T) {
	for width := MinWidth; width <= 110; width++ {
		for height := MinHeight; height <= 30; height++ {
			for _, focus := range []pane{projectPane, servicesPane, tasksPane, detailPane} {
				for _, owner := range []pane{projectPane, servicesPane, tasksPane} {
					plan := responsiveGeometry(width, height, focus, owner)
					cells := make([]bool, width*height)
					mark := func(x0, y0, x1, y1 int) {
						t.Helper()
						if x0 < 0 || y0 < 0 || x1 >= width || y1 >= height {
							t.Fatalf("%dx%d: rectangle outside screen: %d,%d-%d,%d", width, height, x0, y0, x1, y1)
						}
						for y := y0; y <= y1; y++ {
							for x := x0; x <= x1; x++ {
								if cells[y*width+x] {
									t.Fatalf("%dx%d: overlapping panes at %d,%d", width, height, x, y)
								}
								cells[y*width+x] = true
							}
						}
					}
					for p, name := range paneNames {
						r := plan.areas[name]
						if plan.collapsed[p] {
							if r.y1-r.y0-1 != 1 {
								t.Fatal("collapsed header must occupy one row", name, r)
							}
							mark(r.x0+1, r.y0+1, r.x1-1, r.y1-1)
						} else {
							if r.x1-r.x0-1 < 2 || r.y1-r.y0-1 < 1 {
								t.Fatal("expanded pane must have usable content", name, r)
							}
							mark(r.x0, r.y0, r.x1, r.y1)
						}
					}
					footer := plan.areas["footer"]
					mark(footer.x0+1, footer.y0+1, footer.x1-1, footer.y1-1)
					for i, covered := range cells {
						if !covered {
							t.Fatalf("%dx%d: unused cell at %d,%d", width, height, i%width, i/width)
						}
					}
					if plan.collapsed[focus] {
						t.Fatal("focused pane must be expanded", focus)
					}
					if width < wideWidth {
						r := plan.areas[paneNames[focus]]
						if r.x0 != 0 || r.x1 != width-1 {
							t.Fatal("narrow layout must give the focused pane full width", r)
						}
						detail, tasks := plan.areas["detail"], plan.areas["tasks"]
						lastListRow := tasks.y1
						if plan.collapsed[tasksPane] {
							lastListRow-- // borderless headers use virtual borders
						}
						if detail.x0 != 0 || detail.x1 != width-1 || detail.y0 != lastListRow+1 || detail.y1 != height-2 || detail.y1-detail.y0-1 < 2 {
							t.Fatal("output must stay expanded beneath the accordion with usable content", detail)
						}
						if focus == detailPane && detail.y0 != 3 {
							t.Fatal("focusing output must collapse all three lists to headers", detail)
						}
					} else if height < wideHeight {
						active := focus
						if active == detailPane {
							active = owner
						}
						if plan.collapsed[active] || plan.collapsed[detailPane] {
							t.Fatal("short layout must expand the active list and keep details visible")
						}
					}
				}
			}
		}
	}
}

func TestResponsiveLayoutRendersHeadersFocusAndMouseExpansion(t *testing.T) {
	for _, size := range [][2]int{{40, 10}, {50, 24}, {70, 18}, {80, 20}, {100, 10}, {100, 17}, {100, 24}, {100, 30}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: size[0], Height: size[1], OutputMode: gocui.OutputTrue, SupportOverlaps: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(g.Close)
			d := testDashboard(t, &fakeClient{})
			g.SetManagerFunc(d.layout)
			for _, focus := range []pane{projectPane, servicesPane, tasksPane, detailPane} {
				if focus == detailPane {
					d.setFocus(servicesPane)
				}
				d.setFocus(focus)
				if err := g.ForceLayoutAndRedraw(); err != nil {
					t.Fatal(err)
				}
				plan := responsiveGeometry(size[0], size[1], d.focus, d.owner)
				if g.CurrentView().Name() != paneNames[focus] {
					t.Fatal("focus did not follow the expanded pane")
				}
				for p, name := range paneNames {
					v, _ := g.View(name)
					r := plan.areas[name]
					if plan.collapsed[p] {
						x, y := r.x0+1, r.y0+1
						got, _, _, _ := gocui.Screen.GetContent(x, y)
						if got != '─' || !strings.HasPrefix(v.Buffer(), "─ ") {
							t.Fatal("collapsed header was not drawn", name, v.Buffer(), got)
						}
						hit, err := g.VisibleViewByPosition(x, y)
						if err != nil || hit.Name() != name {
							t.Fatal("header is not a mouse target", name, err)
						}
					} else {
						got, _, style, _ := gocui.Screen.GetContent(r.x0, r.y0)
						fg, _, _ := style.Decompose()
						want := g.FrameColor
						if pane(p) == focus {
							want = g.SelFrameColor
						}
						if got != '╭' || fg.Hex() != want.Hex() {
							t.Fatal("expanded rounded pane/focus color changed", name, got, fg)
						}
					}
				}
			}
			// Clicking a header expands it and preserves its selected alias rather
			// than treating the header as the first command row.
			d.selected[servicesPane] = "worker"
			d.setFocus(projectPane)
			if err := g.ForceLayoutAndRedraw(); err != nil {
				t.Fatal(err)
			}
			d.mouseClick(g, servicesPane, 0, 0)
			if d.focus != servicesPane || (size[0] < wideWidth || size[1] < wideHeight) && d.selected[servicesPane] != "worker" {
				t.Fatal("header click lost selection", d.focus, d.selected)
			}
			if err := g.ForceLayoutAndRedraw(); err != nil {
				t.Fatal(err)
			}
			list, _ := g.View("services")
			_, listHeight := list.Size()
			rows := min(2, listHeight)
			if !list.Highlight || len(d.visibleAliases[servicesPane]) != rows {
				t.Fatal("expanded list did not render selectable commands", list.Buffer())
			}
			alias := d.visibleAliases[servicesPane][rows-1]
			d.mouseClick(g, servicesPane, 0, rows-1)
			if d.selected[servicesPane] != alias {
				t.Fatal("expanded list row click did not select its visible command")
			}
			if size[0] < wideWidth || size[1] < wideHeight {
				d.mouseWheel(tasksPane, 3)
				if err := g.ForceLayoutAndRedraw(); err != nil {
					t.Fatal(err)
				}
				list, _ = g.View("tasks")
				if d.focus != tasksPane || !list.Highlight || !strings.Contains(list.Buffer(), "> tests") {
					t.Fatal("wheel did not expand the hovered header", list.Buffer())
				}
			}
		})
	}
}

func TestAccordionKeepsLogsAndDetailsVisibleBelowLists(t *testing.T) {
	for _, size := range [][2]int{{MinWidth, MinHeight}, {70, 18}, {80, 40}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: size[0], Height: size[1], OutputMode: gocui.OutputTrue, SupportOverlaps: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(g.Close)
			d := navigationDashboard(t)
			d.loaded = true
			d.buffer.append([]byte("PREVIEW-LOG"), time.Time{})
			g.SetManagerFunc(d.layout)
			for _, focus := range []pane{servicesPane, detailPane, servicesPane} {
				d.setFocus(focus)
				if err := g.ForceLayoutAndRedraw(); err != nil {
					t.Fatal(err)
				}
				v, _ := g.View("detail")
				plan := responsiveGeometry(size[0], size[1], d.focus, d.owner)
				r := plan.areas["detail"]
				if !v.Frame || r.y1 != size[1]-2 || !strings.Contains(v.Buffer(), "PREVIEW-LOG") {
					t.Fatal("Logs must stay expanded at the bottom while navigating lists", focus, v.Buffer(), r)
				}
				for i, want := range "PREVIEW-LOG" {
					got, _, _, _ := gocui.Screen.GetContent(r.x0+1+i, r.y0+1)
					if got != want {
						t.Fatal("output preview was not drawn beneath the lists", focus, got, want)
					}
				}
			}
			d.tab = 1
			d.syncLogView()
			if err := g.ForceLayoutAndRedraw(); err != nil {
				t.Fatal(err)
			}
			v, _ := g.View("detail")
			if d.focus != servicesPane || !v.Frame || !strings.Contains(v.Buffer(), "Alias: api") {
				t.Fatal("Details must stay visible while the command list has focus", v.Buffer())
			}
			d.move(1)
			if err := g.ForceLayoutAndRedraw(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(v.Buffer(), "Alias: worker") {
				t.Fatal("visible Details did not follow list selection", v.Buffer())
			}
		})
	}
}

func TestResponsiveResizePreservesPausedHistorySearchAndRun(t *testing.T) {
	g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: 100, Height: 24, OutputMode: gocui.OutputTrue, SupportOverlaps: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	d := navigationDashboard(t)
	d.cancelWindow()
	d.loaded, d.follow, d.history = true, false, true
	d.buffer.append([]byte(strings.Repeat("x", 10000)+"\nTAIL\n"), time.Time{})
	d.logWidth, d.logHeight, d.wrapTop = 64, 20, 19 // column 1216, aligned at widths 64 and 38
	d.searchQuery, d.searchDone = "saved", true
	d.matches = []model.LogMatch{{Cursor: 1216, End: 1221}}
	d.matchIndex = 0
	g.SetManagerFunc(d.layout)
	screen := gocui.Screen.(tcell.SimulationScreen)
	for _, size := range [][2]int{{100, 24}, {40, 10}, {100, 12}, {50, 24}, {100, 24}} {
		screen.SetSize(size[0], size[1])
		if err := g.ForceLayoutAndRedraw(); err != nil {
			t.Fatal(err)
		}
		width, height := d.logViewport()
		line := d.buffer.lines[d.top]
		column := line.wrapped(width).point(d.wrapTop).column
		if d.top != 0 || column > 1216 || column+width <= 1216 || d.follow || !d.history || d.searchQuery != "saved" || d.matchIndex != 0 || d.logRun != "run" || d.logAlias != "api" {
			t.Fatal("resize lost paused history/search/run", size, d.logPosition(), column, width)
		}
		v, _ := g.View("detail")
		if len(v.Buffer()) > width*height+height || len(d.buffer.lines[0].text) != 10000 {
			t.Fatal("resize changed retained output or rendered off-screen cells", size)
		}
	}
	screen.SetSize(40, 10)
	d.setFocus(servicesPane)
	if err := g.ForceLayoutAndRedraw(); err != nil {
		t.Fatal(err)
	}
	column := d.buffer.lines[d.top].wrapped(d.logWidth).point(d.wrapTop).column
	if d.top != 0 || column != 1216 || d.logWidth != 38 || d.searchQuery != "saved" {
		t.Fatal("list focus lost the output preview's anchor or completed search")
	}
	d.setFocus(detailPane)
	if err := g.ForceLayoutAndRedraw(); err != nil {
		t.Fatal(err)
	}
	if d.focus != detailPane || d.owner != servicesPane || d.searchQuery != "saved" || d.logWidth != 38 {
		t.Fatal("focusing Logs did not preserve its owner, search and full-width viewport")
	}
	d.help = true
	if err := g.ForceLayoutAndRedraw(); err != nil {
		t.Fatal(err)
	}
	help, _ := g.View("help")
	if g.CurrentView().Name() != "help" || !strings.Contains(help.Buffer(), "Local") {
		t.Fatal("compact Help did not take focus", help.Buffer())
	}
	d.help = false
	d.openSearch()
	if err := g.ForceLayoutAndRedraw(); err != nil {
		t.Fatal(err)
	}
	input, _ := g.View("search")
	if g.CurrentView().Name() != "search" || !strings.HasPrefix(input.Buffer(), "Filter: ") {
		t.Fatal("compact search did not use the footer editor", input.Buffer())
	}
	screen.SetSize(MinWidth-1, MinHeight-1)
	if err := g.ForceLayoutAndRedraw(); err != nil {
		t.Fatal(err)
	}
	minimum, _ := g.View("minimum")
	if g.CurrentView().Name() != "minimum" || !strings.Contains(minimum.Buffer(), "need 40x10") {
		t.Fatal("undersized terminal did not show its resize guidance", minimum.Buffer())
	}
	screen.SetSize(MinWidth, MinHeight)
	if err := g.ForceLayoutAndRedraw(); err != nil {
		t.Fatal(err)
	}
	if g.CurrentView().Name() != "detail" || d.owner != servicesPane || d.searchQuery != "saved" || !d.history {
		t.Fatal("resizing back did not restore Logs and completed search")
	}
}

func TestCollapsedProjectHeaderDisplaysConnectionWarning(t *testing.T) {
	g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: MinWidth, Height: MinHeight, OutputMode: gocui.OutputTrue, SupportOverlaps: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	d := navigationDashboard(t)
	d.connected = false
	if err := d.layout(g); err != nil {
		t.Fatal(err)
	}
	header, _ := g.View("project")
	if !strings.Contains(header.Buffer(), "1 Project - DISCONNECTED") {
		t.Fatal("collapsed project must retain its connection warning", header.Buffer())
	}
}

func TestCompactFooterKeepsContextualActionsComplete(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*dashboard)
		want  []string
	}{
		{"services", func(d *dashboard) { d.setFocus(servicesPane) }, []string{"S: start", "s: stop"}},
		{"tasks", func(d *dashboard) { d.setFocus(tasksPane) }, []string{"S: run", "s: stop"}},
		{"logs", func(d *dashboard) { d.setFocus(servicesPane); d.setFocus(detailPane) }, []string{"/: search", "G: follow"}},
		{"search", func(d *dashboard) { d.setFocus(servicesPane); d.setFocus(detailPane); d.searchQuery = "saved" }, []string{"/: search", "n/N: jump"}},
		{"project", func(d *dashboard) { d.setFocus(projectPane) }, []string{"Tab: focus"}},
		{"details", func(d *dashboard) { d.setFocus(servicesPane); d.setFocus(detailPane); d.tab = 1 }, []string{"↑ ↓: scroll", "← →: pan"}},
		{"help", func(d *dashboard) { d.help = true }, []string{"↑ ↓: navigate", "Esc/?: close"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := navigationDashboard(t)
			test.setup(d)
			for width := MinWidth; width < wideWidth; width++ {
				text := d.footerHints(width)
				for _, hint := range append(append([]string{}, test.want...), "q: quit") {
					if !strings.Contains(text, hint) {
						t.Fatal("compact footer lost an essential hint", width, hint, text)
					}
				}
				if !d.help && !strings.Contains(text, "?: help") || runewidth.StringWidth(text) > width {
					t.Fatal("compact footer must fit and retain help", width, text)
				}
				for _, hint := range strings.Split(text, ", ") {
					if len(strings.Split(hint, ": ")) != 2 {
						t.Fatal("compact footer clipped a shortcut", text)
					}
				}
			}
		})
	}
}

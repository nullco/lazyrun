package gui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/jesseduffield/gocui"
	"github.com/mattn/go-runewidth"
)

func TestKeybindingsDescribeFocusedPaneAndGlobalControls(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*dashboard)
		want  []string
	}{
		{"project", func(d *dashboard) { d.setFocus(projectPane) }, []string{"View project details"}},
		{"services", func(d *dashboard) { d.setFocus(servicesPane) }, []string{"Select command", "Start service", "Stop group (SIGTERM only)", "Restart service"}},
		{"tasks", func(d *dashboard) { d.setFocus(tasksPane) }, []string{"Run task", "Rerun task"}},
		{"logs", func(d *dashboard) { d.setFocus(servicesPane); d.setFocus(detailPane) }, []string{"Scroll logs", "Search retained output", "Earliest retained output", "Follow live output", "Next/previous match"}},
		{"search", func(d *dashboard) { d.setFocus(servicesPane); d.setFocus(detailPane); d.searchQuery = "saved" }, []string{"Clear search or return"}},
		{"details", func(d *dashboard) { d.setFocus(servicesPane); d.setFocus(detailPane); d.tab = 1 }, []string{"Scroll details", "Scroll sideways"}},
		{"project details", func(d *dashboard) { d.setFocus(projectPane); d.setFocus(detailPane) }, []string{"Scroll details", "Return to owning pane"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := navigationDashboard(t)
			test.setup(d)
			d.toggleHelp()
			entries := d.helpEntries()
			var descriptions []string
			for _, entry := range entries {
				descriptions = append(descriptions, entry.description)
			}
			text := strings.Join(descriptions, "\n")
			for _, want := range append(append([]string{}, test.want...), "Local", "Global", "Quit (commands continue)", "Next/previous pane") {
				if !strings.Contains(text, want) {
					t.Fatal("keybindings lost a local/global control", want, text)
				}
			}
		})
	}
}

func TestKeybindingsPopupCentersSizesAndColorsItsColumns(t *testing.T) {
	for _, size := range [][2]int{{MinWidth, MinHeight}, {70, 18}, {100, 30}, {200, 60}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: size[0], Height: size[1], OutputMode: gocui.OutputTrue, SupportOverlaps: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(g.Close)
			d := navigationDashboard(t)
			d.toggleHelp()
			g.SetManagerFunc(d.layout)
			if err := g.ForceLayoutAndRedraw(); err != nil {
				t.Fatal(err)
			}
			v, _ := g.View("help")
			r, rows, count := helpGeometry(size[0], size[1], d.helpEntries())
			popupWidth, popupHeight := r.x1-r.x0+1, r.y1-r.y0+1
			if r.x0 != (size[0]-popupWidth)/2 || r.y0 != (size[1]-1-popupHeight)/2 || popupWidth > 80 || popupHeight != min(size[1]-3, len(rows)+2) {
				t.Fatal("keybindings popup must be centered and content-sized", r)
			}
			if v.Title != "Keybindings" || v.Subtitle != fmt.Sprintf("1 of %d", count) || !v.Highlight || g.CurrentView().Name() != "help" {
				t.Fatal("keybindings popup lost its title, selection or counter", v.Title, v.Subtitle)
			}
			innerWidth, pageHeight := v.Size()
			if len(d.helpRows) != pageHeight || len(plain(v.Buffer())) > (innerWidth+1)*pageHeight+1 {
				t.Fatal("popup retained more than its visible page", v.Buffer())
			}
			var sawHeading, sawKey, sawSelection bool
			for y := r.y0 + 1; y < r.y1; y++ {
				for x := r.x0 + 1; x < r.x1; x++ {
					ch, _, style, _ := gocui.Screen.GetContent(x, y)
					fg, bg, attributes := style.Decompose()
					if ch != ' ' && fg.Hex() == gocui.ColorGreen.Hex() && attributes&tcell.AttrBold != 0 {
						sawHeading = true
					}
					if ch != ' ' && fg.Hex() == gocui.ColorCyan.Hex() {
						sawKey = true
					}
					if bg.Hex() == gocui.ColorBlue.Hex() && fg.Hex() == (gocui.ColorWhite+8).Hex() {
						sawSelection = true
					}
				}
			}
			if !sawHeading || !sawKey || !sawSelection {
				t.Fatal("popup must render green sections, cyan keys and a blue selection", sawHeading, sawKey, sawSelection)
			}
			if size[1] >= 30 && !strings.Contains(v.Buffer(), "Global") {
				t.Fatal("content-sized popup must show its global section", v.Buffer())
			}
		})
	}
}

func TestKeybindingsNavigationMouseAndResizeKeepSelectionVisible(t *testing.T) {
	g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: 40, Height: 10, OutputMode: gocui.OutputTrue, SupportOverlaps: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	d := navigationDashboard(t)
	d.toggleHelp()
	g.SetManagerFunc(d.layout)
	d.moveHelp(10)
	if err := g.ForceLayoutAndRedraw(); err != nil {
		t.Fatal(err)
	}
	if d.helpSelection != 10 || d.helpTop < 1 {
		t.Fatal("page navigation did not advance and scroll selection into view", d.helpSelection, d.helpTop)
	}
	selectedRow := -1
	for row, binding := range d.helpRows {
		if binding == d.helpSelection {
			selectedRow = row
			break
		}
	}
	if selectedRow < 0 {
		t.Fatal("selection did not have a visible row")
	}
	d.helpClick(selectedRow)
	if d.helpSelection != 10 {
		t.Fatal("clicking a binding lost the selected entry")
	}
	d.moveHelp(100)
	if err := g.ForceLayoutAndRedraw(); err != nil {
		t.Fatal(err)
	}
	v, _ := g.View("help")
	if !strings.Contains(plain(v.Buffer()), "Quit (commands") || !strings.Contains(plain(v.Buffer()), "continue)") {
		t.Fatal("last binding's wrapped description must remain visible", v.Buffer())
	}
	selection := d.helpSelection
	gocui.Screen.(tcell.SimulationScreen).SetSize(160, 40)
	if err := g.ForceLayoutAndRedraw(); err != nil {
		t.Fatal(err)
	}
	if d.helpSelection != selection || !strings.Contains(plain(v.Buffer()), "Quit (commands continue)") {
		t.Fatal("resize lost selection or failed to reflow the description", v.Buffer())
	}
	d.toggleHelp()
	if err := g.ForceLayoutAndRedraw(); err != nil {
		t.Fatal(err)
	}
	if g.CurrentView().Name() != "detail" || d.focus != detailPane || d.owner != servicesPane {
		t.Fatal("closing keybindings did not restore the original pane and owner")
	}
	d.toggleHelp()
	if err := g.ForceLayoutAndRedraw(); err != nil {
		t.Fatal(err)
	}
	if d.helpSelection != 0 || d.helpTop != 0 {
		t.Fatal("reopening keybindings must select the first local binding")
	}
}

func TestKeybindingsDescriptionsWrapWithoutLosingText(t *testing.T) {
	d := navigationDashboard(t)
	for width := 16; width <= 40; width++ {
		for _, entry := range d.helpEntries() {
			lines := wrapHelpDescription(entry.description, width)
			if strings.Join(lines, " ") != entry.description {
				t.Fatal("wrapping lost description text", entry, lines)
			}
			for _, line := range lines {
				if runewidth.StringWidth(line) > width {
					t.Fatal("description exceeds its column", width, line)
				}
			}
		}
	}
}

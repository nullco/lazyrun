package gui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jesseduffield/gocui"
	"github.com/mattn/go-runewidth"
)

func TestFooterHintsShowFocusedPaneActionsAndFitOneRow(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*dashboard)
		want  []string
	}{
		{"project", func(d *dashboard) { d.setFocus(projectPane) }, []string{"Tab: focus", "1/2/3: panes"}},
		{"services", func(d *dashboard) { d.setFocus(servicesPane) }, []string{"↑ ↓: navigate", "S: start", "s: stop", "r: restart"}},
		{"tasks", func(d *dashboard) { d.setFocus(tasksPane) }, []string{"S: run", "r: rerun"}},
		{"logs", func(d *dashboard) {
			d.state.Commands[0].Run.ID = "run"
			d.setFocus(servicesPane)
			d.setFocus(detailPane)
		}, []string{"PgUp/PgDn: scroll", "/: search", "G: follow"}},
		{"search", func(d *dashboard) {
			d.state.Commands[0].Run.ID = "run"
			d.setFocus(servicesPane)
			d.setFocus(detailPane)
			d.searchQuery = "needle"
		}, []string{"/: search", "n/N: jump"}},
		{"details", func(d *dashboard) { d.setFocus(servicesPane); d.setFocus(detailPane); d.tab = 1 }, []string{"↑ ↓: scroll", "← →: pan"}},
		{"help", func(d *dashboard) { d.help = true }, []string{"↑ ↓: scroll", "Esc/?: close"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := testDashboard(t, &fakeClient{})
			test.setup(d)
			for width := wideWidth; width <= 200; width++ {
				text := d.footerHints(width)
				if runewidth.StringWidth(text) > width {
					t.Fatal("footer exceeds one row", width, text)
				}
				for _, hint := range append(append([]string{}, test.want...), "q: quit") {
					if !strings.Contains(text, hint) {
						t.Fatal("missing primary hint", width, hint, text)
					}
				}
				if !d.help && !strings.Contains(text, "?: help") {
					t.Fatal("missing help hint", text)
				}
				for _, hint := range strings.Split(text, ", ") {
					if parts := strings.Split(hint, ": "); len(parts) != 2 || parts[0] == "" || parts[1] == "" {
						t.Fatal("incomplete key/action hint", hint)
					}
				}
			}
		})
	}
}

func TestFooterLayoutRendersSingleBlueShortcutRow(t *testing.T) {
	for _, size := range [][2]int{{MinWidth, MinHeight}, {wideWidth, wideHeight}, {100, 24}, {180, 50}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: size[0], Height: size[1], OutputMode: gocui.OutputTrue, SupportOverlaps: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(g.Close)
			d := testDashboard(t, &fakeClient{})
			for _, focus := range []pane{projectPane, servicesPane, tasksPane, detailPane} {
				d.setFocus(focus)
				if err := d.layout(g); err != nil {
					t.Fatal(err)
				}
				footer, _ := g.View("footer")
				_, height := footer.Size()
				if height != 1 || footer.FgColor != footerColor || strings.TrimSuffix(footer.Buffer(), "\n") != d.footerHints(size[0]) {
					t.Fatal("shortcut footer layout/style changed", footer.Buffer(), height)
				}
			}
		})
	}
}

func TestDetailsDisplaysLatestMessageAndLogWarning(t *testing.T) {
	g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: MinWidth, Height: MinHeight, OutputMode: gocui.OutputTrue, SupportOverlaps: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	d := navigationDashboard(t)
	d.logError = "retention problem"
	d.notify("An action failed")
	d.tab, d.detailTop = 1, 1000
	if err := d.layout(g); err != nil {
		t.Fatal(err)
	}
	detail, _ := g.View("detail")
	if !strings.Contains(detail.Buffer(), "Last message: An action failed") || !strings.Contains(detail.Buffer(), "Log warning: retention problem") {
		t.Fatal("Details message/warning rendering changed", detail.Buffer())
	}
}

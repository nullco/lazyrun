package gui

import (
	"fmt"
	"testing"

	"github.com/jesseduffield/gocui"
)

func TestGeometryKeepsSeparateAdjacentBordersWithoutBlankGaps(t *testing.T) {
	for width := MinWidth; width <= 200; width++ {
		for height := MinHeight; height <= 65; height++ {
			areas := geometry(width, height)
			project, services, tasks, detail := areas["project"], areas["services"], areas["tasks"], areas["detail"]
			if services.y0-project.y1 != 1 || tasks.y0-services.y1 != 1 {
				t.Fatalf("%dx%d: pane borders are shared or have a blank row: %+v", width, height, areas)
			}
			for _, name := range []string{"project", "services", "tasks"} {
				r := areas[name]
				if detail.x0-r.x1 != 1 {
					t.Fatalf("%dx%d: borders are shared or have a blank column beside %s: %+v", width, height, name, areas)
				}
			}
			for _, name := range []string{"project", "services", "tasks", "detail"} {
				r := areas[name]
				if r.x0 < 0 || r.y0 < 0 || r.x1 >= width || r.y1 >= height || r.x1-r.x0-1 < 1 || r.y1-r.y0-1 < 2 {
					t.Fatalf("%dx%d: unusable framed pane %s: %+v", width, height, name, r)
				}
			}
			if project.y1-project.y0-1 != 3 || tasks.y1 != detail.y1 || detail.y1 != height-4 {
				t.Fatalf("%dx%d: project/status/footer space changed: %+v", width, height, areas)
			}
		}
	}
}

func TestHeadlessLayoutAppliesCompactRoundedPanesAtMinimumAndLargerSizes(t *testing.T) {
	for _, size := range [][2]int{{MinWidth, MinHeight}, {100, 30}, {180, 50}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: size[0], Height: size[1], OutputMode: gocui.OutputTrue, SupportOverlaps: true})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			d := testDashboard(t, &fakeClient{})
			for _, focus := range []pane{projectPane, servicesPane, tasksPane, detailPane} {
				d.setFocus(focus)
				if err := d.layout(g); err != nil {
					t.Fatal(err)
				}
				if d.small {
					t.Fatal("compact layout made a supported terminal too small")
				}
				for name, want := range geometry(size[0], size[1]) {
					x0, y0, x1, y1, err := g.ViewPosition(name)
					if err != nil {
						t.Fatal(err)
					}
					if got := (rectangle{x0, y0, x1, y1}); got != want {
						t.Fatalf("%s position: %+v != %+v", name, got, want)
					}
					v, _ := g.View(name)
					if v.Frame && string(v.FrameRunes) != "─│╭╮╰╯" {
						t.Fatalf("%s did not use rounded borders: %q", name, string(v.FrameRunes))
					}
				}
			}
		})
	}
}

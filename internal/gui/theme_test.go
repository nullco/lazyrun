package gui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jesseduffield/gocui"
	"github.com/nullco/lazyrun/internal/model"
)

func TestRunStylesKeepOutcomeSemanticsAndSafeLabels(t *testing.T) {
	for _, test := range []struct {
		name  string
		run   model.Run
		style string
	}{
		{"idle", model.Run{Lifecycle: model.NotStarted}, styleMuted},
		{"starting", model.Run{Lifecycle: model.Starting}, styleYellow},
		{"running", model.Run{Lifecycle: model.Running}, styleGreen},
		{"stopping", model.Run{Lifecycle: model.Stopping}, styleYellow},
		{"unknown", model.Run{Lifecycle: model.Unknown}, styleRed},
		{"missing outcome", model.Run{Lifecycle: model.Exited}, styleMuted},
		{"successful service exit", model.Run{Lifecycle: model.Exited, Outcome: &model.Outcome{Kind: model.Success}}, styleCyan},
		{"completed task", model.Run{Definition: model.Definition{Kind: model.Task}, Lifecycle: model.Exited, Outcome: &model.Outcome{Kind: model.Success}}, styleGreen},
		{"failure", model.Run{Lifecycle: model.Exited, Outcome: &model.Outcome{Kind: model.NonzeroExit}}, styleRed},
		{"signal", model.Run{Lifecycle: model.Exited, Outcome: &model.Outcome{Kind: model.Signaled}}, styleRed},
		{"requested stop", model.Run{Lifecycle: model.Exited, StopRequested: true, Outcome: &model.Outcome{Kind: model.Signaled}}, styleYellow},
		{"launch failure", model.Run{Lifecycle: model.Exited, StopRequested: true, Outcome: &model.Outcome{Kind: model.LaunchFailed}}, styleRed},
		{"runtime error", model.Run{Lifecycle: model.Running, Error: "error"}, styleRed},
		{"metadata warning", model.Run{Lifecycle: model.Running, MetadataError: "error"}, styleRed},
		{"log warning", model.Run{Lifecycle: model.Running, LogError: "error"}, styleRed},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.run.Definition.Kind == "" {
				test.run.Definition.Kind = model.Service
			}
			test.run.Definition.Alias = "api\x1b]52;c;bad\a"
			item := model.CommandState{Run: test.run, Removed: true}
			text := coloredItemLabel(item)
			checkSafe(t, text)
			if runStyle(test.run) != test.style || !strings.Contains(text, test.style) || plain(text) != itemLabel(item) {
				t.Fatal("styling changed status or metadata", text, itemLabel(item))
			}
		})
	}
}

func TestRenderedThemeTracksFocusSelectionAndHelp(t *testing.T) {
	// gocui keeps a package-global Screen and Close does not join MainLoop's
	// event poller. Like the actual CLI, render with one GUI per process; a
	// later headless test must not replace Screen beneath that closing poller.
	const childFlag = "LAZYRUN_TEST_THEME_RENDER_CHILD"
	if os.Getenv(childFlag) != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRenderedThemeTracksFocusSelectionAndHelp$")
		cmd.Env = append(os.Environ(), childFlag+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("render subprocess: %v\n%s", err, output)
		}
		return
	}
	g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: 100, Height: 30, OutputMode: gocui.OutputTrue, SupportOverlaps: true})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	d := testDashboard(t, &fakeClient{})
	d.state.Commands[0].Run.ID = "api-run"
	d.state.Commands[0].Run.Lifecycle = model.Running
	d.state.Commands[2].Run.ID = "tests-run"
	d.state.Commands[2].Run.Lifecycle = model.Exited
	d.state.Commands[2].Run.Outcome = &model.Outcome{Kind: model.Success}
	frames := []struct {
		focus        pane
		worker, help bool
	}{
		{focus: projectPane}, {focus: servicesPane}, {focus: servicesPane, worker: true},
		{focus: tasksPane}, {focus: detailPane}, {focus: servicesPane, help: true}, {focus: servicesPane},
	}
	checkCell := func(x, y int, foreground, background gocui.Attribute) {
		t.Helper()
		_, _, style, _ := gocui.Screen.GetContent(x, y)
		fg, bg, _ := style.Decompose()
		if fg.Hex() != foreground.Hex() || bg.Hex() != background.Hex() {
			t.Fatalf("cell %d,%d: fg=%#x bg=%#x; want fg=%#x bg=%#x", x, y, fg.Hex(), bg.Hex(), foreground.Hex(), background.Hex())
		}
	}
	checkRoundedCorners := func(r rectangle) {
		t.Helper()
		for _, corner := range []struct {
			x, y int
			want rune
		}{{r.x0, r.y0, '╭'}, {r.x1, r.y0, '╮'}, {r.x0, r.y1, '╰'}, {r.x1, r.y1, '╯'}} {
			got, _, _, _ := gocui.Screen.GetContent(corner.x, corner.y)
			if got != corner.want {
				t.Fatalf("corner %d,%d: %q != %q", corner.x, corner.y, got, corner.want)
			}
		}
	}
	step := 0
	g.SetManagerFunc(func(g *gocui.Gui) error {
		// This manager runs before drawing. Check the previous completed frame,
		// then render the next one; this tests actual tcell pixels, not just fields.
		if step > 0 {
			frame := frames[step-1]
			areas := geometry(100, 30)
			for x, want := range []rune(d.footerHints(100)) {
				got, _, _, _ := gocui.Screen.GetContent(x, 29)
				if got != want {
					t.Fatalf("footer cell %d: %q != %q", x, got, want)
				}
				checkCell(x, 29, footerColor, gocui.ColorDefault)
			}
			if frame.help {
				x0, y0, x1, y1, err := g.ViewPosition("help")
				if err != nil {
					t.Fatal(err)
				}
				checkRoundedCorners(rectangle{x0, y0, x1, y1})
				checkCell(x0, y0+1, g.SelFrameColor, gocui.ColorDefault)
				if g.CurrentView().Name() != "help" {
					t.Fatal("help did not take focus")
				}
			} else {
				for _, name := range paneNames {
					r := areas[name]
					color := g.FrameColor
					if name == paneNames[frame.focus] {
						color = g.SelFrameColor
					}
					checkRoundedCorners(r)
					checkCell(r.x0, r.y0+1, color, gocui.ColorDefault)
				}
				// Unselected/cached rows must retain status colors rather than
				// accidentally inheriting the focused pane's green title color.
				if frame.focus == detailPane {
					detail := areas["detail"]
					checkCell(detail.x0+1, detail.y0+1, gocui.ColorMagenta, gocui.ColorDefault)
				}
				services, tasks := areas["services"], areas["tasks"]
				apiColor, taskColor := gocui.ColorGreen, gocui.ColorGreen
				apiBackground, taskBackground := gocui.ColorDefault, gocui.ColorDefault
				if frame.focus == servicesPane && !frame.worker {
					apiColor, apiBackground = gocui.ColorWhite+8, gocui.ColorBlue
				}
				if frame.focus == tasksPane {
					taskColor, taskBackground = gocui.ColorWhite+8, gocui.ColorBlue
				}
				checkCell(services.x0+1+len("  api  "), services.y0+1, apiColor, apiBackground)
				checkCell(tasks.x0+1+len("  tests  "), tasks.y0+1, taskColor, taskBackground)
				for _, p := range []pane{servicesPane, tasksPane} {
					r := areas[paneNames[p]]
					if frame.focus == p {
						y := r.y0 + 1
						if p == servicesPane && frame.worker {
							y++
						}
						checkCell(r.x0+1, y, gocui.ColorWhite+8, gocui.ColorBlue)
						// Highlight extends beyond the label to the full row width.
						checkCell(r.x1-1, y, gocui.ColorWhite+8, gocui.ColorBlue)
					} else {
						checkCell(r.x0+1, r.y0+1, gocui.ColorDefault, gocui.ColorDefault)
					}
				}
			}
		}
		if step == len(frames) {
			return gocui.ErrQuit
		}
		frame := frames[step]
		d.help = frame.help
		d.setFocus(frame.focus)
		if frame.focus == servicesPane {
			d.selected[servicesPane] = "api"
			if frame.worker {
				d.selected[servicesPane] = "worker"
			}
		}
		if frame.focus == detailPane {
			d.loaded = true
			d.buffer.reset(false)
			d.buffer.append([]byte("\x1b[35mapplication output\x1b[0m"), time.Time{})
		}
		if err := d.layout(g); err != nil {
			return err
		}
		step++
		g.UpdateAsync(func(*gocui.Gui) error { return nil })
		return nil
	})
	g.UpdateAsync(func(*gocui.Gui) error { return nil })
	if err := g.MainLoop(); err != gocui.ErrQuit {
		t.Fatal(err)
	}
	if step != len(frames) {
		t.Fatal("frames not rendered", step)
	}
}

func TestSelectionHighlightFollowsScrollingAndClearsWhenUnfocused(t *testing.T) {
	g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: wideWidth, Height: wideHeight, OutputMode: gocui.OutputTrue, SupportOverlaps: true})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	d := testDashboard(t, &fakeClient{})
	for i := range 20 {
		def := model.Definition{Alias: fmt.Sprintf("service-%02d", i), Kind: model.Service}
		d.state.Commands = append(d.state.Commands, model.CommandState{Definition: &def, Run: model.Run{Definition: def, Lifecycle: model.NotStarted}})
	}
	d.setFocus(servicesPane)
	d.selected[servicesPane] = "service-19"
	if err := d.layout(g); err != nil {
		t.Fatal(err)
	}
	v, _ := g.View("services")
	_, cursorY := v.Cursor()
	_, height := v.Size()
	if !v.Highlight || cursorY != height-1 || !strings.Contains(v.Buffer(), "> service-19") {
		t.Fatal("selected row did not track the visible scroll position", v.Buffer(), cursorY)
	}
	d.setFocus(detailPane)
	if err := d.layout(g); err != nil {
		t.Fatal(err)
	}
	if v.Highlight || !strings.Contains(v.Buffer(), "> service-19") {
		t.Fatal("unfocused owner retained active highlight or lost its selection")
	}
}

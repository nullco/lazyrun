package gui

import (
	"fmt"
	"strings"
	"time"

	"github.com/jesseduffield/gocui"
	"github.com/nullco/lazyrun/internal/model"
)

const (
	MinWidth  = 70
	MinHeight = 18
)

type rectangle struct{ x0, y0, x1, y1 int }

func geometry(width, height int) map[string]rectangle {
	if width < MinWidth || height < MinHeight {
		return nil
	}
	left := max(24, min(42, width/3))
	bottom := height - 4 // one notification row and two footer rows
	projectEnd := 5
	servicesEnd := projectEnd + (bottom-projectEnd)/2
	return map[string]rectangle{
		"project":      {0, 0, left, projectEnd},
		"services":     {0, projectEnd, left, servicesEnd},
		"tasks":        {0, servicesEnd, left, bottom},
		"detail":       {left, 0, width - 1, bottom},
		"notification": {-1, bottom, width, height - 2},
		"footer":       {-1, height - 3, width, height},
	}
}

func view(g *gocui.Gui, name, title string, r rectangle, frame bool) (*gocui.View, error) {
	v, err := g.SetView(name, r.x0, r.y0, r.x1, r.y1, 0)
	// SetView returns a wrapped ErrUnknownView with a valid newly created view.
	if v == nil {
		return nil, err
	}
	v.Frame = frame
	v.Title = title
	v.Wrap = false
	v.Autoscroll = false
	v.Clear()
	_ = v.SetOrigin(0, 0)
	return v, nil
}
func putLines(v *gocui.View, lines []logLine, top, horizontal int) {
	width, height := v.Size()
	for i := top; i < len(lines) && i < top+height; i++ {
		if i > top {
			fmt.Fprint(v, "\n")
		}
		fmt.Fprint(v, crop(lines[i].text, lines[i].prefix, horizontal, width))
	}
}
func textLines(text string) []logLine {
	parts := strings.Split(text, "\n")
	lines := make([]logLine, len(parts))
	for i, part := range parts {
		lines[i].text = part
	}
	return lines
}
func (d *dashboard) layout(g *gocui.Gui) error {
	width, height := g.Size()
	areas := geometry(width, height)
	wasSmall := d.small
	d.small = areas == nil
	if wasSmall != d.small {
		d.syncLogView()
	}
	if d.small {
		for _, name := range []string{"project", "services", "tasks", "detail", "notification", "footer", "help"} {
			_ = g.DeleteView(name)
		}
		v, err := view(g, "minimum", "", rectangle{-1, -1, max(1, width), max(1, height)}, false)
		if err != nil {
			return err
		}
		putLines(v, textLines(fmt.Sprintf("Terminal too small: need %dx%d (now %dx%d).\nResize to use the dashboard.\nq / Ctrl-C quits; commands keep running.", MinWidth, MinHeight, width, height)), 0, 0)
		_, err = g.SetCurrentView("minimum")
		return err
	}
	_ = g.DeleteView("minimum")
	connection := "connected"
	if !d.connected {
		connection = "DISCONNECTED: " + d.connectionError
	}
	v, err := view(g, "project", "1 Project", areas["project"], true)
	if err != nil {
		return err
	}
	putLines(v, textLines(singleLine(d.state.Project.Name)+"\n"+singleLine(d.state.Project.Root)+"\n"+connection), 0, 0)
	for _, p := range []pane{servicesPane, tasksPane} {
		name := paneNames[p]
		title := fmt.Sprintf("%d %s", p+1, strings.ToUpper(name[:1])+name[1:])
		v, err := view(g, name, title, areas[name], true)
		if err != nil {
			return err
		}
		items := d.items(p)
		if len(items) == 0 {
			putLines(v, textLines("(no configured "+name+")"), 0, 0)
			continue
		}
		lines := make([]logLine, len(items))
		selected := 0
		for i, item := range items {
			prefix := "  "
			if item.Run.Definition.Alias == d.selected[p] {
				selected = i
				prefix = "> "
			}
			lines[i].text = prefix + itemLabel(item)
			if !d.connected {
				lines[i].text += " [last known]"
			}
		}
		_, h := v.Size()
		top := max(0, selected-h+1)
		putLines(v, lines, top, 0)
	}
	item, ok := d.current()
	title := "Project Details"
	if ok {
		tabs := "[Logs] | Details"
		if d.tab == 1 {
			tabs = "Logs | [Details]"
		}
		title = singleLine(item.Run.Definition.Alias) + ": " + tabs + " - " + outcome(item.Run)
		if d.tab == 0 {
			if d.follow {
				title += " - following"
			} else {
				title += " - PAUSED (G follows)"
			}
		}
	}
	v, err = view(g, "detail", title, areas["detail"], true)
	if err != nil {
		return err
	}
	if d.owner == projectPane {
		d.showDetails(v, projectDetails(d.state, connection, d.opts.Version))
	} else if !ok {
		putLines(v, textLines("No command selected. Add commands to lazyrun.yml and reopen."), 0, 0)
	} else if d.tab == 1 {
		text := commandDetails(item)
		if !d.connected {
			text = "DISCONNECTED: cached state; ownership/outcome not known.\n\n" + text
		}
		if item.Run.ID != "" && item.Run.Shell != d.state.Project.Shell {
			text += "\nCurrent shell (next run): " + singleLine(d.state.Project.Shell)
		}
		d.showDetails(v, text)
	} else {
		d.showLogs(v, item)
	}
	v, err = view(g, "notification", "", areas["notification"], false)
	if err != nil {
		return err
	}
	text := ""
	if time.Now().Before(d.noticeUntil) {
		text = d.notice
	} else if !d.connected {
		text = connection
	} else if d.logError != "" {
		text = "Log warning: " + d.logError
	}
	putLines(v, textLines(text), 0, 0)
	v, err = view(g, "footer", "", areas["footer"], false)
	if err != nil {
		return err
	}
	actions := "Select a service/task for actions"
	if ok {
		start, restart := "start", "restart"
		if displayKind(item) == model.Task {
			start, restart = "run", "rerun"
		}
		actions = "S " + start + " | s stop (SIGTERM only) | r " + restart
		if item.Removed {
			actions = "s stop (removed from config)"
		}
	}
	putLines(v, textLines("1 Project | 2 Services | 3 Tasks | Tab focus | ? help | q quit\n"+actions+" | Enter details | [ ] tabs | G follow"), 0, 0)
	if d.help {
		v, err := view(g, "help", "Help - j/k scroll; Esc / ? closes", rectangle{2, 1, width - 3, height - 2}, true)
		if err != nil {
			return err
		}
		lines := textLines(helpText)
		_, h := v.Size()
		d.helpTop = min(d.helpTop, max(0, len(lines)-h))
		putLines(v, lines, d.helpTop, 0)
		_, _ = g.SetViewOnTop("help")
		_, err = g.SetCurrentView("help")
		return err
	}
	_ = g.DeleteView("help")
	_, err = g.SetCurrentView(paneNames[d.focus])
	return err
}
func (d *dashboard) showDetails(v *gocui.View, text string) {
	if d.notice != "" {
		text += "\n\nLast notification: " + d.notice
	}
	lines := textLines(plain(text)) // all metadata fields, including historical run IDs
	_, height := v.Size()
	d.detailTop = min(d.detailTop, max(0, len(lines)-height))
	putLines(v, lines, d.detailTop, d.horizontal)
}
func (d *dashboard) showLogs(v *gocui.View, item model.CommandState) {
	if item.Run.ID == "" {
		putLines(v, textLines("Not started. Press S to start/run this command."), 0, 0)
		return
	}
	if d.unavailable {
		putLines(v, textLines("Logs unavailable for this run.\n"+d.logError), 0, 0)
		return
	}
	if !d.loaded {
		putLines(v, textLines("Loading latest-run logs...\n"+d.logError), 0, 0)
		return
	}
	lines := d.buffer.lines
	if len(lines) == 0 {
		putLines(v, textLines("(no displayable output)"), 0, 0)
		return
	}
	_, height := v.Size()
	if d.buffer.evicted {
		fmt.Fprint(v, crop("[older output discarded from dashboard buffer]", "", 0, v.InnerWidth()), "\n")
		height--
	}
	bottom := max(0, len(lines)-height)
	if d.follow {
		d.top = bottom
	} else {
		d.top = min(d.top, bottom)
	}
	// putLines uses view height; slice also accounts for the eviction banner.
	end := min(len(lines), d.top+height)
	putLines(v, lines[d.top:end], 0, d.horizontal)
}

const helpText = `Navigation
1 / 2 / 3         Project / Services / Tasks
Tab / Shift-Tab   Next / previous pane
j / k, Up / Down  Select command; scroll focused detail pane
Enter / Esc       Focus details / return to owning pane
[ / ]             Switch Logs / Details
PgUp / PgDn       Scroll by ten lines; Left / Right scroll sideways

Selected command only (never project-wide)
S                 Start service / run task
s                 Graceful stop: SIGTERM only, no force-kill
r                 Restart service / rerun task (stop before start)

Logs follow by default; scrolling pauses. G resumes following.
Config changes affect the next run; reopen to synchronize.
? / Esc           Toggle / dismiss this help
q / Ctrl-C        Quit dashboard ONLY; commands continue`

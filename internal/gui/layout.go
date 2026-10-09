package gui

import (
	"fmt"
	"strings"

	"github.com/jesseduffield/gocui"
	"github.com/mattn/go-runewidth"
	"github.com/nullco/lazyrun/internal/model"
)

const (
	MinWidth  = 40
	MinHeight = 10
	// Keep enough room for a useful list and log preview before splitting.
	wideWidth  = 100
	wideHeight = 24
)

type rectangle struct{ x0, y0, x1, y1 int }

func geometry(width, height int) map[string]rectangle {
	if width < wideWidth || height < wideHeight {
		return nil
	}
	left := max(24, min(42, width/3))
	bottom := height - 2 // panes end directly above the single footer/input row
	// Framed coordinates include their borders. Adjacent border cells keep
	// each pane distinct without an extra blank row or column.
	const separation = 1
	projectEnd := 3 // two content rows: name, connection
	servicesStart := projectEnd + separation
	servicesEnd := servicesStart + (bottom-servicesStart-separation)/2
	return map[string]rectangle{
		"project":  {0, 0, left, projectEnd},
		"services": {0, servicesStart, left, servicesEnd},
		"tasks":    {0, servicesEnd + separation, left, bottom},
		"detail":   {left + separation, 0, width - 1, bottom},
		"footer":   {-1, bottom, width, height},
	}
}

// A collapsed pane is a borderless, one-row header, using the same view name
// and input bindings as its expanded pane. Rectangles include virtual borders.
type paneLayout struct {
	areas     map[string]rectangle
	collapsed [4]bool
}

func responsiveGeometry(width, height int, focus, owner pane) *paneLayout {
	if width < MinWidth || height < MinHeight {
		return nil
	}
	if areas := geometry(width, height); areas != nil {
		return &paneLayout{areas: areas}
	}
	layout := &paneLayout{areas: map[string]rectangle{
		"footer": {-1, height - 2, width, height},
	}}
	if width < wideWidth {
		// Keep output visible beneath the list accordion. Give it most of the
		// height, and collapse all lists when output itself takes focus.
		listHeight := 3
		if focus != detailPane {
			listHeight = min(height-5, max(6, (height-1)/3))
		}
		layout.stack(0, width-1, listHeight, []pane{projectPane, servicesPane, tasksPane}, focus)
		layout.areas["detail"] = rectangle{0, listHeight, width - 1, height - 2}
	} else {
		left := max(24, min(42, width/3))
		active := focus
		if active == detailPane {
			active = owner
		}
		layout.stack(0, left, height-1, []pane{projectPane, servicesPane, tasksPane}, active)
		layout.areas["detail"] = rectangle{left + 1, 0, width - 1, height - 2}
	}
	return layout
}

func (l *paneLayout) stack(left, right, height int, panes []pane, active pane) {
	y := 0
	for _, p := range panes {
		if p == active {
			rows := height - len(panes) + 1
			l.areas[paneNames[p]] = rectangle{left, y, right, y + rows - 1}
			y += rows
		} else {
			l.collapsed[p] = true
			l.areas[paneNames[p]] = rectangle{left - 1, y - 1, right + 1, y + 1}
			y++
		}
	}
}

func paneView(g *gocui.Gui, name, title string, r rectangle, collapsed bool) (*gocui.View, error) {
	v, err := view(g, name, title, r, !collapsed)
	if err == nil && collapsed {
		v.Title = ""
		width, _ := v.Size()
		label := crop("─ "+singleLine(title)+" ", "", 0, width)
		fmt.Fprint(v, label, strings.Repeat("─", max(0, width-runewidth.StringWidth(label))))
	}
	return v, err
}

func view(g *gocui.Gui, name, title string, r rectangle, frame bool) (*gocui.View, error) {
	v, err := g.SetView(name, r.x0, r.y0, r.x1, r.y1, 0)
	// SetView returns a wrapped ErrUnknownView with a valid newly created view.
	if v == nil {
		return nil, err
	}
	v.Frame = frame
	if frame {
		v.FrameRunes = []rune{'─', '│', '╭', '╮', '╰', '╯'}
	}
	v.Title = title
	v.Wrap = false
	v.Autoscroll = false
	v.Highlight = false
	v.FgColor, v.BgColor = gocui.ColorDefault, gocui.ColorDefault
	v.SelFgColor, v.SelBgColor = gocui.ColorWhite|gocui.AttrBold, gocui.ColorBlue
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
	configureTheme(g)
	width, height := g.Size()
	plan := responsiveGeometry(width, height, d.focus, d.owner)
	wasSmall := d.small
	d.small = plan == nil
	if wasSmall != d.small {
		d.syncLogView()
	}
	if d.small {
		g.Cursor = false
		for _, name := range []string{"project", "services", "tasks", "detail", "footer", "help", "search"} {
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
	areas := plan.areas
	d.collapsed = plan.collapsed
	connection := "connected"
	if !d.connected {
		connection = "DISCONNECTED: " + d.connectionError
	}
	projectTitle := "1 Project"
	if !d.connected {
		projectTitle += " - DISCONNECTED"
	}
	v, err := paneView(g, "project", projectTitle, areas["project"], d.collapsed[projectPane])
	if err != nil {
		return err
	}
	connectionStyle := styleGreen
	if !d.connected {
		connectionStyle = styleRed
	}
	if !d.collapsed[projectPane] {
		putLines(v, textLines(coloredLabel(singleLine(d.state.Project.Name), "\x1b[1;36m")+"\n"+coloredLabel(connection, connectionStyle)), 0, 0)
	}
	for _, p := range []pane{servicesPane, tasksPane} {
		name := paneNames[p]
		title := fmt.Sprintf("%d %s", p+1, strings.ToUpper(name[:1])+name[1:])
		v, err := paneView(g, name, title, areas[name], d.collapsed[p])
		if err != nil {
			return err
		}
		clear(d.visibleAliases[p])
		d.visibleAliases[p] = d.visibleAliases[p][:0]
		if d.collapsed[p] {
			continue
		}
		items := d.items(p)
		if len(items) == 0 {
			v.FgColor = gocui.ColorWhite | gocui.AttrDim
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
			lines[i].text = prefix + coloredItemLabel(item)
			if !d.connected {
				lines[i].text = coloredLabel(prefix+itemLabel(item)+" [last known]", styleMuted)
			}
		}
		_, h := v.Size()
		top := max(0, selected-h+1)
		putLines(v, lines, top, 0)
		for i := top; i < len(items) && i < top+h; i++ {
			d.visibleAliases[p] = append(d.visibleAliases[p], items[i].Run.Definition.Alias)
		}
		if d.focus == p && !d.help {
			v.Highlight = true
			if err := v.SetCursor(0, selected-top); err != nil {
				return err
			}
			// SetHighlight overrides the status color on the selected cells for
			// white-on-blue contrast; Highlight also fills the rest of the row.
			if err := v.SetHighlight(selected-top, true); err != nil {
				return err
			}
		}
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
			if d.history {
				title += " - history"
			}
			if d.streamFirst > 0 {
				title += " - beginning not retained"
			}
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
	v, err = view(g, "footer", "", areas["footer"], false)
	if err != nil {
		return err
	}
	v.FgColor = footerColor
	if !d.searchEditing {
		putLines(v, textLines(d.footerHints(width)), 0, 0)
	}
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
	if d.searchEditing {
		// Shared footer presentation; the editor still belongs to the pane that
		// opened it. Other pane filters can reuse this surface later.
		v, err := view(g, "search", "", areas["footer"], false)
		if err != nil {
			return err
		}
		v.Editable = true
		v.Editor = searchEditor{d: d, g: g}
		const label = "Filter: "
		innerWidth, _ := v.Size()
		available := innerWidth - len(label)
		draftWidth := runewidth.StringWidth(d.searchDraft)
		offset := max(0, draftWidth-available+1)
		fmt.Fprint(v, label, crop(d.searchDraft, "", offset, available))
		_ = v.SetCursor(len(label)+min(available-1, draftWidth-offset), 0)
		_, _ = g.SetViewOnTop("search")
		g.Cursor = true
		_, err = g.SetCurrentView("search")
		return err
	}
	_ = g.DeleteView("search")
	g.Cursor = false
	_, err = g.SetCurrentView(paneNames[d.focus])
	return err
}
func (d *dashboard) showDetails(v *gocui.View, text string) {
	if d.notice != "" {
		text += "\n\nLast message: " + d.notice
	}
	if d.owner != projectPane && d.logError != "" {
		text += "\n\nLog warning: " + d.logError
	}
	lines := textLines(plain(text)) // all metadata fields, including historical run IDs
	_, height := v.Size()
	d.detailTop = min(d.detailTop, max(0, len(lines)-height))
	putLines(v, lines, d.detailTop, d.horizontal)
}
func (d *dashboard) showLogs(v *gocui.View, item model.CommandState) {
	width, height := v.Size()
	d.resizeLogs(width)
	d.logHeight = height
	if item.Run.ID == "" {
		v.FgColor = gocui.ColorWhite | gocui.AttrDim
		putLines(v, textLines("Not started. Press S to start/run this command."), 0, 0)
		return
	}
	if d.unavailable {
		v.FgColor = gocui.ColorYellow + 8
		putLines(v, textLines("Logs unavailable for this run.\n"+d.logError), 0, 0)
		return
	}
	if !d.loaded {
		v.FgColor = gocui.ColorWhite | gocui.AttrDim
		putLines(v, textLines("Loading latest-run logs...\n"+d.logError), 0, 0)
		return
	}
	lines := d.buffer.lines
	if len(lines) == 0 {
		putLines(v, textLines("(no displayable output)"), 0, 0)
		return
	}
	if d.buffer.evicted {
		fmt.Fprint(v, crop(coloredLabel("[older output discarded from dashboard buffer]", styleYellow), "", 0, v.InnerWidth()), "\n")
		height--
	}
	d.logHeight = max(1, height)
	for i, line := range d.visibleLogs() {
		if i > 0 {
			fmt.Fprint(v, "\n")
		}
		fmt.Fprint(v, line.text)
	}
}

const helpText = `Navigation
1 / 2 / 3         Project / Services / Tasks
Tab / Shift-Tab   Next / previous pane
j / k, Up / Down  Select command; scroll focused detail pane
Enter / Esc       Focus details / return to owning pane
[ / ]             Switch Logs / Details
PgUp / PgDn       Scroll by ten rows; Logs wrap to pane width
Left / Right      Scroll Details sideways
Left click        Expand header; focus pane/select command (no lifecycle action)
Mouse wheel       Navigate hovered list; scroll Logs / Details / Help
Home / G          Earliest retained output / return to live follow
/ (focused Logs)  Literal, case-sensitive search of all retained run output
n / N (Logs only) Next / previous match; Esc clears/cancels log search

Selected command only (never project-wide)
S                 Start service / run task
s                 Graceful stop: SIGTERM only, no force-kill
r                 Restart service / rerun task (stop before start)

Logs follow by default; scrolling pauses. G resumes following.
Config changes affect the next run; reopen to synchronize.
? / Esc           Toggle / dismiss this help
q / Ctrl-C        Quit dashboard ONLY; commands continue

Small screens: lists collapse; narrow layouts keep Logs/Details at the bottom.`

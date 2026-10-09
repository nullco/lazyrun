package gui

import (
	"fmt"
	"strings"

	"github.com/jesseduffield/gocui"
	"github.com/mattn/go-runewidth"
	"github.com/nullco/lazyrun/internal/model"
)

type helpEntry struct{ key, description string }
type helpRow struct {
	logLine
	binding int // -1 for section headings and spacing
}

// Keybindings are reference-only: selecting a row never executes its action.
func (d *dashboard) helpEntries() []helpEntry {
	entries := []helpEntry{{description: "Local"}}
	add := func(key, description string) { entries = append(entries, helpEntry{key, description}) }
	switch {
	case d.focus == projectPane:
		add("<enter>", "View project details")
	case d.focus == servicesPane || d.focus == tasksPane:
		add("↑/↓, j/k", "Select command")
		add("<enter>", "Focus Logs/Details")
	default:
		scroll := "Scroll details"
		if d.owner != projectPane && d.tab == 0 {
			scroll = "Scroll logs"
		}
		add("↑/↓, j/k", scroll)
		add("<pgup>/<pgdn>", "Scroll ten rows")
		if d.owner != projectPane && d.tab == 0 {
			if item, ok := d.current(); ok && item.Run.ID != "" {
				add("<home>", "Earliest retained output")
				add("G", "Follow live output")
				add("/", "Search retained output")
				add("n / N", "Next/previous match")
			}
		} else {
			add("← / →", "Scroll sideways")
		}
		back := "Return to owning pane"
		if d.owner != projectPane && d.tab == 0 && d.searchQuery != "" {
			back = "Clear search or return"
		}
		add("<esc>", back)
	}
	if item, ok := d.current(); ok && d.connected {
		start, restart := "Start service", "Restart service"
		if displayKind(item) == model.Task {
			start, restart = "Run task", "Rerun task"
		}
		if !item.Removed {
			add("S", start)
		}
		add("s", "Stop group (SIGTERM only)")
		if !item.Removed {
			add("r", restart)
		}
	}
	entries = append(entries, helpEntry{}, helpEntry{description: "Global"})
	add("1 / 2 / 3", "Project / Services / Tasks")
	add("<tab>/<s-tab>", "Next/previous pane")
	if d.owner != projectPane {
		add("[ / ]", "Switch Logs/Details")
	}
	add("Click", "Focus pane or select command")
	add("Wheel", "Navigate or scroll hovered pane")
	add("? / <esc>", "Close keybindings")
	add("q / <ctrl-c>", "Quit (commands continue)")
	return entries
}

func helpColumns(entries []helpEntry) (int, int) {
	keyWidth, descriptionWidth := 0, 0
	for _, entry := range entries {
		keyWidth = max(keyWidth, runewidth.StringWidth(entry.key))
		descriptionWidth = max(descriptionWidth, runewidth.StringWidth(entry.description))
	}
	return keyWidth, descriptionWidth
}

func helpRows(entries []helpEntry, width int) ([]helpRow, int) {
	keyWidth, _ := helpColumns(entries)
	// Two cells of padding on either side, and one between the columns.
	descriptionWidth := max(1, width-keyWidth-5)
	var rows []helpRow
	binding := 0
	for _, entry := range entries {
		if entry.key == "" {
			text := ""
			if entry.description != "" {
				label := "--- " + entry.description + " ---"
				text = strings.Repeat(" ", max(0, (width-runewidth.StringWidth(label))/2)) + coloredLabel(label, "\x1b[1;32m")
			}
			rows = append(rows, helpRow{logLine{text: text}, -1})
			continue
		}
		for i, description := range wrapHelpDescription(entry.description, descriptionWidth) {
			key := strings.Repeat(" ", keyWidth+3)
			if i == 0 {
				key = strings.Repeat(" ", keyWidth-runewidth.StringWidth(entry.key)+2) + coloredLabel(entry.key, styleCyan) + " "
			}
			text := key + description
			text += strings.Repeat(" ", max(0, width-runewidth.StringWidth(plain(text))))
			rows = append(rows, helpRow{logLine{text: text}, binding})
		}
		binding++
	}
	return rows, binding
}

func wrapHelpDescription(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		candidate := word
		if line != "" {
			candidate = line + " " + word
		}
		if runewidth.StringWidth(candidate) > width && line != "" {
			lines = append(lines, line)
			line = word
		} else {
			line = candidate
		}
	}
	return append(lines, line)
}

func helpGeometry(width, height int, entries []helpEntry) (rectangle, []helpRow, int) {
	keyWidth, descriptionWidth := helpColumns(entries)
	popupWidth := min(width-4, max(50, min(80, keyWidth+descriptionWidth+7)))
	rows, count := helpRows(entries, popupWidth-2)
	popupHeight := min(height-3, len(rows)+2)
	x, y := (width-popupWidth)/2, (height-1-popupHeight)/2
	return rectangle{x, y, x + popupWidth - 1, y + popupHeight - 1}, rows, count
}

func (d *dashboard) moveHelp(delta int) {
	if !d.help || d.small {
		return
	}
	count := 0
	for _, entry := range d.helpEntries() {
		if entry.key != "" {
			count++
		}
	}
	d.helpSelection = max(0, min(count-1, d.helpSelection+delta))
}

func (d *dashboard) toggleHelp() {
	d.help = !d.help
	if d.help {
		d.helpSelection, d.helpTop = 0, 0
	}
}

func (d *dashboard) showHelp(g *gocui.Gui, width, height int) error {
	r, rows, count := helpGeometry(width, height, d.helpEntries())
	v, err := view(g, "help", "Keybindings", r, true)
	if err != nil {
		return err
	}
	_, pageHeight := v.Size()
	d.helpSelection = max(0, min(count-1, d.helpSelection))
	first, last := 0, 0
	for i, row := range rows {
		if row.binding == d.helpSelection {
			last = i
			if i == 0 || rows[i-1].binding != row.binding {
				first = i
			}
		}
	}
	d.helpTop = max(0, min(d.helpTop, len(rows)-pageHeight))
	if first < d.helpTop {
		d.helpTop = first
	} else if last >= d.helpTop+pageHeight {
		d.helpTop = last - pageHeight + 1
	}
	clear(d.helpRows)
	d.helpRows = d.helpRows[:0]
	for i := d.helpTop; i < len(rows) && i < d.helpTop+pageHeight; i++ {
		if i > d.helpTop {
			fmt.Fprint(v, "\n")
		}
		fmt.Fprint(v, crop(rows[i].text, "", 0, v.InnerWidth()))
		d.helpRows = append(d.helpRows, rows[i].binding)
		if rows[i].binding == d.helpSelection {
			if err := v.SetHighlight(i-d.helpTop, true); err != nil {
				return err
			}
		}
	}
	v.Highlight = true
	v.Subtitle = fmt.Sprintf("%d of %d", d.helpSelection+1, count)
	if err := v.SetCursor(0, max(0, first-d.helpTop)); err != nil {
		return err
	}
	g.Cursor = false
	_, _ = g.SetViewOnTop("help")
	_, err = g.SetCurrentView("help")
	return err
}

func (d *dashboard) helpClick(row int) {
	if d.help && !d.small && row >= 0 && row < len(d.helpRows) && d.helpRows[row] >= 0 {
		d.helpSelection = d.helpRows[row]
	}
}

package gui

import (
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/nullco/lazyrun/internal/model"
)

// Keep primary actions and help/quit visible at the minimum width. Append
// secondary hints only when the entire hint fits, never a clipped key/action.
func (d *dashboard) footerHints(width int) string {
	var primary, secondary []string
	switch {
	case d.help:
		primary = []string{"↑ ↓: scroll", "Esc/?: close"}
		secondary = []string{"PgUp/PgDn: scroll"}
	case d.logPaneFocused():
		primary = []string{"PgUp/PgDn: scroll", "/: search", "G: follow"}
		if d.searchQuery != "" {
			primary = append(primary, "n/N: jump")
		}
		secondary = []string{"Home: earliest", "↑ ↓: scroll", "[ ]: tabs", "Esc: back", "Tab: focus"}
	case d.focus == detailPane && d.owner != projectPane && d.tab == 0:
		if item, ok := d.current(); ok && d.connected && !item.Removed {
			start := "start"
			if displayKind(item) == model.Task {
				start = "run"
			}
			primary = []string{"S: " + start}
		}
		secondary = []string{"[ ]: tabs", "Esc: back", "Tab: focus"}
	case d.focus == detailPane:
		primary = []string{"↑ ↓: scroll", "← →: pan"}
		secondary = []string{"Esc: back", "[ ]: tabs", "Tab: focus"}
		if d.owner == projectPane {
			secondary = []string{"Esc: back", "Tab: focus"}
		}
	case d.focus == servicesPane || d.focus == tasksPane:
		primary = []string{"↑ ↓: navigate"}
		if item, ok := d.current(); ok && d.connected {
			if item.Removed {
				primary = append(primary, "s: stop")
			} else {
				start, restart := "start", "restart"
				if displayKind(item) == model.Task {
					start, restart = "run", "rerun"
				}
				primary = append(primary, "S: "+start, "s: stop", "r: "+restart)
			}
		}
		secondary = []string{"Enter: details", "Tab: focus", "1/2/3: panes"}
	default:
		primary = []string{"Tab: focus", "1/2/3: panes"}
		secondary = []string{"Enter: details", "Shift-Tab: back"}
	}
	primary = append(primary, "q: quit")
	if !d.help {
		primary = append(primary, "?: help")
	}
	text := strings.Join(primary, ", ")
	for _, hint := range secondary {
		candidate := text + ", " + hint
		if runewidth.StringWidth(candidate) > width {
			break
		}
		text = candidate
	}
	return text
}

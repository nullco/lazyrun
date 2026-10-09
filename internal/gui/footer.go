package gui

import (
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/nullco/lazyrun/internal/model"
)

// Reserve help/quit at every supported width. Prefer the primary actions that
// fit, then append complete secondary hints, never a clipped key/action.
func (d *dashboard) footerHints(width int) string {
	var primary, secondary []string
	switch {
	case d.help:
		primary = []string{"↑ ↓: navigate", "Esc/?: close"}
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
	if width < wideWidth {
		switch {
		case d.logPaneFocused():
			primary = []string{"/: search"}
			if d.searchQuery != "" {
				primary = append(primary, "n/N: jump")
			}
			primary = append(primary, "G: follow", "PgUp/PgDn: scroll")
		case (d.focus == servicesPane || d.focus == tasksPane) && len(primary) > 1:
			secondary = append([]string{primary[0]}, secondary...)
			primary = primary[1:]
		}
	}
	required := []string{"q: quit"}
	if !d.help {
		required = append(required, "?: help")
	}
	var shown []string
	for _, hint := range primary {
		candidate := append(append(append([]string{}, shown...), hint), required...)
		if runewidth.StringWidth(strings.Join(candidate, ", ")) <= width {
			shown = append(shown, hint)
		}
	}
	text := strings.Join(append(shown, required...), ", ")
	for _, hint := range secondary {
		candidate := text + ", " + hint
		if runewidth.StringWidth(candidate) > width {
			break
		}
		text = candidate
	}
	return text
}

package gui

import (
	"github.com/jesseduffield/gocui"
	"github.com/nullco/lazyrun/internal/model"
)

const (
	styleReset  = "\x1b[0m"
	styleMuted  = "\x1b[2m"
	styleGreen  = "\x1b[32m"
	styleYellow = "\x1b[33m"
	styleRed    = "\x1b[31m"
	styleCyan   = "\x1b[36m"
)

var footerColor = gocui.NewRGBColor(128, 170, 255)

func configureTheme(g *gocui.Gui) {
	// SelFrameColor is ignored unless GUI-level highlighting is enabled.
	g.Highlight = true
	g.FrameColor = gocui.ColorWhite
	g.SelFrameColor = (gocui.ColorGreen + 8) | gocui.AttrBold
	g.SelFgColor = g.SelFrameColor
	g.SelBgColor = gocui.ColorDefault
}

// Only use this on UI-owned labels, never raw application log output. Metadata
// is stripped of controls before adding our fixed, validated SGR sequences.
func coloredLabel(text, style string) string {
	return style + plain(text) + styleReset
}

func runStyle(run model.Run) string {
	if run.Error != "" || run.MetadataError != "" || run.LogError != "" || run.Lifecycle == model.Unknown {
		return styleRed
	}
	switch run.Lifecycle {
	case model.Running:
		return styleGreen
	case model.Starting, model.Stopping:
		return styleYellow
	case model.Exited:
		if run.Outcome == nil {
			return styleMuted
		}
		if run.Outcome.Kind == model.LaunchFailed {
			return styleRed
		}
		if run.StopRequested {
			return styleYellow
		}
		if run.Outcome.Kind != model.Success {
			return styleRed
		}
		if run.Definition.Kind == model.Task {
			return styleGreen
		}
		return styleCyan // naturally exited service, not a running/healthy one
	default:
		return styleMuted
	}
}

func coloredItemLabel(item model.CommandState) string {
	return itemLabelWithStatus(item, coloredLabel(outcome(item.Run), runStyle(item.Run)))
}

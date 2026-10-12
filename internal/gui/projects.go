package gui

import (
	"context"
	"path/filepath"

	"github.com/jesseduffield/gocui"
	"github.com/nullco/lazyrun/internal/model"
)

type projectLister interface {
	Projects(context.Context) ([]model.ProjectSummary, error)
}

type stateScope struct {
	client     Client
	generation uint64
}

func (d *dashboard) selectProject(id string) {
	if id == d.state.Project.ID || d.opts.ProjectClient == nil {
		return
	}
	if d.busy || d.reloading {
		d.notify("A request is pending; wait before switching projects")
		return
	}
	for _, p := range d.projects {
		if p.ID != id {
			continue
		}
		d.client = d.opts.ProjectClient(id)
		d.stateGeneration++
		d.cancelWindow()
		d.resetNavigation()
		d.state = model.State{Project: model.Project{ID: p.ID, Name: p.Name, Root: p.Root,
			ConfigPath: filepath.Join(p.Root, "lazyrun.yml"), Logs: model.DefaultLogSettings()}}
		d.loadingProject = true
		d.connectionError = ""
		d.selected = [4]string{}
		d.visibleAliases = [4][]string{}
		d.logAlias, d.logRun = "", ""
		d.buffer.reset(false)
		d.loaded = false
		d.cursor = 0
		d.logError = ""
		d.unavailable = false
		d.follow = true
		d.top, d.horizontal, d.detailTop = 0, 0, 0
		d.tab = 0
		d.notify("Loading " + p.Name)
		select {
		case <-d.stateScopes:
		default:
		}
		d.stateScopes <- stateScope{client: d.client, generation: d.stateGeneration}
		return
	}
}

func (d *dashboard) projectListFocused() bool {
	return d.focus == projectPane && len(d.projects) > 1
}

func (d *dashboard) renderProjects(v *gocui.View) error {
	clear(d.visibleAliases[projectPane])
	d.visibleAliases[projectPane] = d.visibleAliases[projectPane][:0]
	if d.collapsed[projectPane] {
		return nil
	}
	// Outside the project list, keep only the selected project's name.
	if !d.projectListFocused() {
		putLines(v, textLines(coloredLabel(singleLine(d.state.Project.Name), "\x1b[1;36m")), 0, 0)
		d.visibleAliases[projectPane] = append(d.visibleAliases[projectPane], d.state.Project.ID)
		return nil
	}
	lines := make([]logLine, len(d.projects))
	selected := 0
	for i, p := range d.projects {
		prefix := "  "
		if p.ID == d.state.Project.ID {
			selected = i
		}
		lines[i].text = prefix + singleLine(p.Name)
	}
	_, h := v.Size()
	top := max(0, selected-h+1)
	putLines(v, lines, top, 0)
	for i := top; i < len(d.projects) && i < top+h; i++ {
		d.visibleAliases[projectPane] = append(d.visibleAliases[projectPane], d.projects[i].ID)
	}
	if d.focus == projectPane && !d.help {
		v.Highlight = true
		if err := v.SetCursor(0, selected-top); err != nil {
			return err
		}
		return v.SetHighlight(selected-top, true)
	}
	return nil
}

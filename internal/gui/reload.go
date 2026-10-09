package gui

import (
	"context"
	"errors"
	"time"

	"github.com/nullco/lazyrun/internal/config"
)

// Reload uses the original project path and existing synchronization protocol.
// It never reconnects, launches a supervisor, or requests a lifecycle action.
func (d *dashboard) reloadConfig() {
	if d.help || d.small || d.searchEditing {
		return
	}
	if !d.connected {
		d.notify("Disconnected: reload disabled; reopen the dashboard if the supervisor was lost")
		return
	}
	if d.reloading || d.busy {
		d.notify("A request is pending; no config reload was queued")
		return
	}
	path, projectID := d.state.Project.ConfigPath, d.state.Project.ID
	d.reloading = true
	d.notify("Config reload pending")
	d.spawn(func() {
		ctx, cancel := context.WithTimeout(d.ctx, 10*time.Second)
		defer cancel()
		project, data, err := config.LoadFileBytes(path)
		if err == nil && project.ID != projectID {
			err = errors.New("configuration project identity changed")
		}
		if err == nil {
			err = d.client.Sync(ctx, data)
		}
		d.send(d.ctx, event{reload: true, err: err})
	})
}

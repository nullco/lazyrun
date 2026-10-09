package gui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nullco/lazyrun/internal/config"
	"github.com/nullco/lazyrun/internal/model"
)

func reloadDashboard(t *testing.T, c *fakeClient) (*dashboard, string) {
	t.Helper()
	d := testDashboard(t, c)
	root := t.TempDir()
	path := filepath.Join(root, config.Filename)
	d.state.Project.Root, d.state.Project.ConfigPath = root, path
	d.state.Project.ID = model.ProjectID(root)
	if err := os.WriteFile(path, []byte("version: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return d, path
}

func TestReloadConfigIsGlobalAndKeepsSelection(t *testing.T) {
	for _, focus := range []pane{projectPane, servicesPane, tasksPane, detailPane} {
		c := &fakeClient{}
		d, path := reloadDashboard(t, c)
		d.setFocus(focus)
		selected, owner := d.selected, d.owner
		data := "version: 1\ntasks: {new: {command: echo new}}\n"
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		d.reloadConfig()
		eventually(t, func() bool { d.drain(); return !d.reloading })
		c.mu.Lock()
		if string(c.synced) != data || len(c.calls) != 1 || c.calls[0] != "sync " {
			t.Fatal("reload did not synchronize exact source", c.calls, string(c.synced))
		}
		c.mu.Unlock()
		if d.selected != selected || d.focus != focus || d.owner != owner || !strings.Contains(d.notice, "Config reloaded") {
			t.Fatal("reload changed navigation or lost feedback", d.notice)
		}
	}
}

func TestReloadConfigFailureLeavesStateIntact(t *testing.T) {
	for _, failure := range []string{"invalid", "missing", "identity", "transport"} {
		t.Run(failure, func(t *testing.T) {
			c := &fakeClient{}
			d, path := reloadDashboard(t, c)
			switch failure {
			case "invalid":
				if err := os.WriteFile(path, []byte("version: ["), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "identity":
				d.state.Project.ID = "different"
			case "transport":
				c.syncErr = errors.New("supervisor unavailable")
			}
			id, command := d.state.Project.ID, d.state.Commands[0].Run.Definition.Command
			d.reloadConfig()
			eventually(t, func() bool { d.drain(); return !d.reloading })
			if !strings.Contains(d.notice, "Config reload failed:") || d.state.Project.ID != id || d.state.Commands[0].Run.Definition.Command != command {
				t.Fatal("reload failure changed state or lost feedback", d.notice)
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			if failure != "transport" && len(c.calls) != 0 {
				t.Fatal("unvalidated config sent to supervisor", c.calls)
			}
		})
	}
}

func TestReloadConfigGuardsAndBoundedRequests(t *testing.T) {
	for _, guard := range []string{"help", "small", "search", "disconnected", "lifecycle"} {
		t.Run(guard, func(t *testing.T) {
			c := &fakeClient{}
			d, _ := reloadDashboard(t, c)
			d.help, d.small, d.searchEditing = guard == "help", guard == "small", guard == "search"
			d.connected, d.busy = guard != "disconnected", guard == "lifecycle"
			d.reloadConfig()
			if d.reloading {
				t.Fatal("guard allowed reload", guard)
			}
		})
	}
	c := &fakeClient{blocked: make(chan struct{})}
	d, _ := reloadDashboard(t, c)
	d.reloadConfig()
	d.reloadConfig()
	d.setFocus(servicesPane)
	d.action("start")
	eventually(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.calls) == 1 })
	if d.busy || !d.reloading {
		t.Fatal("reload allowed a concurrent lifecycle request")
	}
	close(c.blocked)
	eventually(t, func() bool { d.drain(); return !d.reloading })
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) != 1 {
		t.Fatal("duplicate reload or lifecycle request queued", c.calls)
	}
}

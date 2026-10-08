//go:build linux

package runtime

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"lazyrun/internal/model"
)

func validateProject(p model.Project) error {
	if p.ID == "" || !filepath.IsAbs(p.Shell) || strings.ContainsRune(p.Shell, 0) {
		return errors.New("project ID and absolute shell executable are required")
	}
	if p.Logs.MaxBytes <= 0 {
		return errors.New("output limit must be positive")
	}
	seen := make(map[string]bool)
	for _, d := range p.Definitions() {
		if strings.TrimSpace(d.Alias) == "" || strings.IndexFunc(d.Alias, unicode.IsControl) >= 0 || strings.TrimSpace(d.Command) == "" || strings.ContainsRune(d.Command, 0) || !filepath.IsAbs(d.Cwd) || strings.ContainsRune(d.Cwd, 0) || (d.Kind != model.Service && d.Kind != model.Task) {
			return fmt.Errorf("invalid definition for alias %q", d.Alias)
		}
		if seen[d.Alias] {
			return fmt.Errorf("duplicate alias %q", d.Alias)
		}
		seen[d.Alias] = true
		if _, err := environment(nil, d.Env); err != nil {
			return fmt.Errorf("alias %s: %w", d.Alias, err)
		}
	}
	return nil
}

func (m *Manager) Sync(project model.Project) error {
	if err := validateProject(project); err != nil {
		return err
	}
	if project.ID != m.projectID {
		return errors.New("cannot synchronize a different project")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Lock every slot in a stable order: synchronization is atomic with respect
	// to lifecycle requests and list state. Monitors never take the manager lock.
	aliases := make([]string, 0, len(m.slots))
	for alias := range m.slots {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		m.slots[alias].mu.Lock()
	}
	defer func() {
		for _, alias := range aliases {
			m.slots[alias].mu.Unlock()
		}
	}()
	for _, s := range m.slots {
		s.configured = false
	}
	limit := project.Logs.MaxBytes
	if limit > MaxMemoryOutput {
		limit = MaxMemoryOutput
	}
	for _, d := range project.Definitions() {
		s, ok := m.slots[d.Alias]
		if !ok {
			s = &slot{run: model.Run{ProjectID: m.projectID, Definition: d.Clone(), Shell: project.Shell, Lifecycle: model.NotStarted}}
			m.slots[d.Alias] = s
		}
		s.definition, s.configured, s.shell, s.outputLimit = d, true, project.Shell, int(limit)
		if s.run.ID == "" {
			s.run.Definition, s.run.Shell = d.Clone(), project.Shell
		}
	}
	m.project = project.Clone()
	return nil
}

func (m *Manager) State() model.State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state := model.State{Project: m.project.Clone(), Commands: []model.CommandState{}}
	seen := make(map[string]bool)
	appendSlot := func(alias string, removed bool) {
		s := m.slots[alias]
		s.mu.Lock()
		defer s.mu.Unlock()
		if removed && !s.run.Lifecycle.Active() {
			return
		}
		item := model.CommandState{Run: s.run.Clone(), Removed: removed}
		if s.configured {
			d := s.definition.Clone()
			item.Definition = &d
		}
		state.Commands = append(state.Commands, item)
	}
	for _, d := range m.project.Definitions() {
		seen[d.Alias] = true
		appendSlot(d.Alias, false)
	}
	var removed []string
	for alias := range m.slots {
		if !seen[alias] {
			removed = append(removed, alias)
		}
	}
	sort.Strings(removed)
	for _, alias := range removed {
		appendSlot(alias, true)
	}
	return state
}

// Restore is startup-only. Recorded identities never become owned exec.Cmds.
// Unknown potentially surviving runs block both signaling and replacement.
func (m *Manager) Restore(runs []model.Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, saved := range runs {
		if saved.ProjectID != m.projectID || saved.ID == "" {
			return errors.New("invalid restored run")
		}
		r, err := ReconcileRecorded(saved)
		if err != nil {
			return err
		}
		s, ok := m.slots[r.Definition.Alias]
		if !ok {
			s = &slot{}
			m.slots[r.Definition.Alias] = s
		}
		s.run = r.Clone()
		s.cmd = nil
		s.output = &memoryOutput{limit: MaxMemoryOutput}
		s.outputUnavailable = true
		s.done = make(chan struct{})
		// There is no future collector/reaper for a historical run, even if unknown.
		close(s.done)
		if err := m.persistLocked(s); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) persistLocked(s *slot) error {
	if m.opts.Persist == nil {
		return nil
	}
	s.run.MetadataError = ""
	err := m.opts.Persist(s.run.Clone())
	if err != nil {
		s.run.MetadataError = "metadata write failed: " + err.Error()
	} else {
		s.run.MetadataError = ""
	}
	return err
}

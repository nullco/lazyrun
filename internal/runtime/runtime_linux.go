//go:build linux

// Package runtime is the supervisor-owned headless process-group engine.
// Dashboard contexts never own command lifetimes. The supervisor supplies
// durable metadata hooks; recorded processes never become owned exec.Cmds.
package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"lazyrun/internal/model"
)

var (
	ErrAlreadyRunning = errors.New("already running")
	ErrNoActiveRun    = errors.New("no active run")
	ErrRestartPending = errors.New("restart already pending")
	ErrRestartBlocked = errors.New("restart blocked: old process group is still alive or unverified; replacement canceled")
	ErrRunChanged     = errors.New("run ID no longer matches latest run")
	ErrUnknownAlias   = errors.New("unknown alias")
	ErrRemoved        = errors.New("alias removed from configuration; only stop is available")
	ErrUnmanaged      = errors.New("run may be unmanaged after supervisor loss; manual cleanup required; no signaling or replacement allowed")
)

const MaxMemoryOutput = 2 * 1024 * 1024

type Options struct {
	PollInterval   time.Duration
	RestartTimeout time.Duration
	DrainTimeout   time.Duration
	Persist        func(model.Run) error // Called before launch intent and on state transitions.
}

type Manager struct {
	projectID string
	bootID    string
	opts      Options
	mu        sync.RWMutex
	project   model.Project
	slots     map[string]*slot
}

type slot struct {
	mu                sync.Mutex
	definition        model.Definition
	configured        bool
	shell             string
	outputLimit       int
	run               model.Run
	cmd               *exec.Cmd
	output            *memoryOutput
	outputUnavailable bool
	done              chan struct{}
	pending           bool
}

// New installs Linux child-subreaping, a process-wide setting required for
// conservative group completion. Use this engine only in its owning supervisor
// process; other code in that process must not reap its children independently.
func New(project model.Project, opts Options) (*Manager, error) {
	if err := validateProject(project); err != nil {
		return nil, err
	}
	if opts.PollInterval < 0 || opts.RestartTimeout < 0 || opts.DrainTimeout < 0 {
		return nil, errors.New("runtime timeouts must not be negative")
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = 20 * time.Millisecond
	}
	if opts.RestartTimeout == 0 {
		opts.RestartTimeout = 3 * time.Second
	}
	if opts.DrainTimeout == 0 {
		opts.DrainTimeout = time.Second
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, fmt.Errorf("read Linux boot identity: %w", err)
	}
	m := &Manager{projectID: project.ID, bootID: strings.TrimSpace(string(boot)), opts: opts, slots: make(map[string]*slot)}
	if err := m.Sync(project); err != nil {
		return nil, err
	}
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return nil, fmt.Errorf("enable Linux child-subreaping: %w", err)
	}
	return m, nil
}

func (m *Manager) lookup(alias string) (*slot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.slots[alias]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownAlias, alias)
	}
	return s, nil
}

func (m *Manager) Snapshot(alias string) (model.Run, error) {
	s, err := m.lookup(alias)
	if err != nil {
		return model.Run{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run.Clone(), nil
}

// Start uses only the requesting client's environment snapshot, then applies
// configured overrides. It never implicitly uses the manager's own os.Environ.
func (m *Manager) Start(alias string, env []string) (model.Run, error) {
	s, err := m.lookup(alias)
	if err != nil {
		return model.Run{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending {
		return s.run.Clone(), ErrRestartPending
	}
	return m.startLocked(s, env)
}

func (m *Manager) startLocked(s *slot, env []string) (model.Run, error) {
	if s.run.Lifecycle.Active() {
		if s.cmd == nil {
			return s.run.Clone(), ErrUnmanaged
		}
		return s.run.Clone(), ErrAlreadyRunning
	}
	if !s.configured {
		return s.run.Clone(), ErrRemoved
	}
	environment, err := environment(env, s.definition.Env)
	if err != nil {
		return s.run.Clone(), err
	}
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return s.run.Clone(), fmt.Errorf("create run ID: %w", err)
	}
	s.run = model.Run{ID: hex.EncodeToString(bytes[:]), ProjectID: m.projectID, Definition: s.definition.Clone(), Shell: s.shell, Lifecycle: model.Starting, StartedAt: time.Now(), Identity: model.ProcessIdentity{BootID: m.bootID}}
	s.output = &memoryOutput{limit: s.outputLimit}
	s.outputUnavailable = false
	s.cmd = nil
	s.done = make(chan struct{})
	launchFailure := func(err error) (model.Run, error) {
		now := time.Now()
		s.run.Lifecycle = model.Exited
		s.run.EndedAt = &now
		s.run.Outcome = &model.Outcome{Kind: model.LaunchFailed, Error: err.Error()}
		m.persistLocked(s)
		close(s.done)
		return s.run.Clone(), err
	}
	// Durable intent precedes exec: a crash in the launch/identity gap must
	// leave a conservative blocker rather than permit an invisible duplicate.
	if err := m.persistLocked(s); err != nil {
		return launchFailure(fmt.Errorf("persist launch intent: %w", err))
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return launchFailure(fmt.Errorf("create output pipe: %w", err))
	}
	cmd := exec.Command(s.shell, "-c", s.definition.Command)
	cmd.Dir, cmd.Env = s.definition.Cwd, environment
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A nil stdin is /dev/null. Use explicit files rather than exec's copying
	// goroutines so Wait cannot hang on pipe FDs held by surviving descendants.
	cmd.Stdout, cmd.Stderr = writer, writer
	if err := cmd.Start(); err != nil {
		reader.Close()
		writer.Close()
		return launchFailure(fmt.Errorf("launch %s: %w", s.definition.Alias, err))
	}
	writer.Close()
	cmd.Env = nil // The child has its snapshot; don't retain inherited secrets.
	s.cmd = cmd
	id, err := identity(cmd.Process.Pid, m.bootID)
	if err != nil {
		// Do not signal or duplicate an unverified launch. Retain ownership for
		// diagnosis; under the supported /proc baseline even instant exits are
		// readable because we have not reaped the shell.
		s.run.Identity = model.ProcessIdentity{PID: cmd.Process.Pid, PGID: cmd.Process.Pid, BootID: m.bootID}
		s.run.Lifecycle, s.run.Error = model.Unknown, err.Error()
	} else {
		s.run.Identity, s.run.Lifecycle = id, model.Running
	}
	captureDone := make(chan error, 1)
	output := s.output
	go func() {
		_, err := io.CopyBuffer(output, reader, make([]byte, 32*1024))
		reader.Close()
		captureDone <- err
	}()
	go m.monitor(s, reader, captureDone)
	persistErr := m.persistLocked(s)
	if err == nil {
		err = persistErr
	}
	return s.run.Clone(), err
}

func (m *Manager) Stop(alias string) (model.Run, error) {
	s, err := m.lookup(alias)
	if err != nil {
		return model.Run{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err = m.stopLocked(s)
	return s.run.Clone(), err
}

func (m *Manager) stopLocked(s *slot) error {
	if !s.run.Lifecycle.Active() {
		return ErrNoActiveRun
	}
	if s.cmd == nil {
		return ErrUnmanaged
	}
	if err := terminateGroup(s.run.Identity); err != nil {
		s.run.Lifecycle, s.run.Error = model.Unknown, err.Error()
		m.persistLocked(s)
		return err
	}
	s.run.StopRequested, s.run.Lifecycle = true, model.Stopping
	return m.persistLocked(s)
}

// Restart reserves one replacement per alias. A bounded timeout cancels it;
// nothing starts later if the old group eventually exits. Dashboard contexts do
// not own this operation and cannot cancel a replacement already accepted here.
func (m *Manager) Restart(alias string, env []string) (model.Run, error) {
	s, err := m.lookup(alias)
	if err != nil {
		return model.Run{}, err
	}
	// Copy/validate the request before a possibly lengthy graceful stop.
	env = append([]string(nil), env...)
	s.mu.Lock()
	if _, err := environment(env, s.definition.Env); err != nil {
		s.mu.Unlock()
		return model.Run{}, err
	}
	if !s.configured {
		r := s.run.Clone()
		s.mu.Unlock()
		return r, ErrRemoved
	}
	if s.run.Lifecycle.Active() && s.cmd == nil {
		r := s.run.Clone()
		s.mu.Unlock()
		return r, ErrUnmanaged
	}
	if s.pending {
		r := s.run.Clone()
		s.mu.Unlock()
		return r, ErrRestartPending
	}
	if !s.run.Lifecycle.Active() {
		r, err := m.startLocked(s, env)
		s.mu.Unlock()
		return r, err
	}
	if err := m.stopLocked(s); err != nil {
		r := s.run.Clone()
		s.mu.Unlock()
		return r, err
	}
	s.pending = true
	done := s.done
	s.mu.Unlock()
	deadline := time.Now().Add(m.opts.RestartTimeout)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-done:
		s.mu.Lock()
		defer s.mu.Unlock()
		s.pending = false
		// If completion and timeout become ready together, select is random.
		// Never accept a replacement after the bounded wait has expired.
		if !time.Now().Before(deadline) {
			return s.run.Clone(), ErrRestartBlocked
		}
		return m.startLocked(s, env)
	case <-timer.C:
		s.mu.Lock()
		defer s.mu.Unlock()
		s.pending = false
		return s.run.Clone(), ErrRestartBlocked
	}
}

// Wait cancels only the caller's wait, not capture or the managed process.
func (m *Manager) Wait(ctx context.Context, alias, runID string) (model.Run, error) {
	s, err := m.lookup(alias)
	if err != nil {
		return model.Run{}, err
	}
	s.mu.Lock()
	if s.run.ID != runID || runID == "" {
		s.mu.Unlock()
		return model.Run{}, ErrRunChanged
	}
	done := s.done
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return model.Run{}, ctx.Err()
	case <-done:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.run.ID != runID {
			return model.Run{}, ErrRunChanged
		}
		return s.run.Clone(), nil
	}
}

func (m *Manager) Output(alias, runID string) ([]byte, bool, error) {
	s, err := m.lookup(alias)
	if err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if runID == "" || s.run.ID != runID {
		return nil, false, ErrRunChanged
	}
	data, truncated := s.output.snapshot()
	return data, truncated, nil
}

func (m *Manager) monitor(s *slot, reader *os.File, captureDone <-chan error) {
	ticker := time.NewTicker(m.opts.PollInterval)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		alive, err := groupAlive(s.run.Identity)
		if err != nil {
			if s.run.Lifecycle != model.Unknown || s.run.Error != err.Error() {
				s.run.Lifecycle, s.run.Error = model.Unknown, err.Error()
				m.persistLocked(s)
			}
			s.mu.Unlock()
			continue // Fail closed: no reaping/replacement/signaling unverified PIDs.
		}
		if alive {
			if s.run.Lifecycle == model.Unknown {
				s.run.Lifecycle, s.run.Error = model.Running, ""
				if s.run.StopRequested {
					s.run.Lifecycle = model.Stopping
				}
				m.persistLocked(s)
			}
			s.mu.Unlock()
			continue
		}
		// No executing members remain. Keep the slot locked while reaping and
		// finishing capture so Stop can never signal a now-reusable PGID.
		if err := reapDescendants(s.run.Identity); err != nil {
			if s.run.Lifecycle != model.Unknown || s.run.Error != err.Error() {
				s.run.Lifecycle, s.run.Error = model.Unknown, err.Error()
				m.persistLocked(s)
			}
			s.mu.Unlock()
			continue
		}
		waitErr := s.cmd.Wait()
		outcome := outcome(s.cmd.ProcessState, waitErr)
		drainTimer := time.NewTimer(m.opts.DrainTimeout)
		select {
		case err := <-captureDone:
			if err != nil {
				s.run.Error = "output capture failed: " + err.Error()
			}
		case <-drainTimer.C:
			reader.Close()
			<-captureDone
			s.run.Error = "output capture incomplete: a process outside the managed group kept the pipe open"
		}
		drainTimer.Stop()
		now := time.Now()
		s.run.Lifecycle, s.run.Outcome, s.run.EndedAt = model.Exited, &outcome, &now
		m.persistLocked(s)
		close(s.done)
		s.mu.Unlock()
		return
	}
}

func outcome(state *os.ProcessState, err error) model.Outcome {
	if state == nil {
		return model.Outcome{Error: fmt.Sprintf("unable to collect shell outcome: %v", err)}
	}
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok {
		return model.Outcome{Error: "unsupported wait status"}
	}
	if status.Signaled() {
		return model.Outcome{Kind: model.Signaled, Signal: int(status.Signal())}
	}
	code := status.ExitStatus()
	kind := model.Success
	if code != 0 {
		kind = model.NonzeroExit
	}
	return model.Outcome{Kind: kind, ExitCode: &code}
}

func environment(snapshot []string, overrides map[string]string) ([]string, error) {
	values := make(map[string]string, len(snapshot)+len(overrides))
	for _, item := range snapshot {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" || strings.ContainsRune(item, 0) {
			return nil, errors.New("invalid environment snapshot entry")
		}
		values[key] = value
	}
	for key, value := range overrides {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("invalid environment override name/value for %q", key)
		}
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result, nil
}

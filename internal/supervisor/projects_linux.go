//go:build linux

package supervisor

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/nullco/lazyrun/internal/config"
	"github.com/nullco/lazyrun/internal/logstore"
	"github.com/nullco/lazyrun/internal/model"
	"github.com/nullco/lazyrun/internal/runtime"
	"github.com/nullco/lazyrun/internal/transport"
	"github.com/sirupsen/logrus"
)

const MaxProjects = 256

// Registration lasts only for this server's lifetime. Run records remain durable.
type projectServer struct {
	mu       sync.RWMutex
	projects map[string]*projectSession
	hello    transport.Hello
	logger   *logrus.Logger
}

type projectSession struct {
	backend
	paths *Paths
	owner *os.File
}

func (s *projectServer) close() {
	for _, p := range s.projects {
		p.owner.Close()
		p.paths.Close()
	}
}

func (s *projectServer) register(id, root string) error {
	p, err := config.Load(root)
	if err != nil {
		return err
	}
	if p.Root != root || p.ID != id {
		return errors.New("project root or identity mismatch")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if session := s.projects[id]; session != nil {
		return session.manager.Sync(p)
	}
	if len(s.projects) >= MaxProjects {
		return errors.New("supervisor project limit reached")
	}
	paths, err := OpenPaths(id)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			paths.Close()
		}
	}()
	owner, err := lock(paths.State, "state.lock")
	if err != nil {
		return fmt.Errorf("project state is already owned or unsafe; keep XDG locations stable: %w", err)
	}
	defer func() {
		if !success {
			owner.Close()
		}
	}()
	storage := &store{dir: paths.State, projectID: id}
	saved, err := storage.Load()
	if err != nil {
		return err
	}
	manager, err := runtime.New(p, runtime.Options{LogStore: logstore.New(paths.State), Persist: func(r model.Run) error {
		err := storage.Save(r)
		if err != nil {
			s.logger.WithError(err).Error("run metadata could not be retained")
		}
		return err
	}})
	if err != nil {
		return err
	}
	if err := manager.Restore(saved); err != nil {
		return err
	}
	s.projects[id] = &projectSession{backend: backend{manager: manager, root: root}, paths: paths, owner: owner}
	success = true
	return nil
}

func (s *projectServer) handle(req transport.Request) transport.Response {
	if req.ID == "" {
		return invalid(req, errors.New("request ID is required"))
	}
	switch req.Operation {
	case transport.Handshake:
		var payload transport.HandshakePayload
		if err := transport.Decode(req.Payload, &payload); err != nil {
			return invalid(req, err)
		}
		if payload.ProjectID != req.ProjectID {
			return invalid(req, errors.New("project identity mismatch"))
		}
		hello := s.hello
		hello.ProjectID = req.ProjectID
		return transport.Reply(req, hello, nil)
	case transport.RegisterProject:
		var payload transport.RegisterPayload
		if err := transport.Decode(req.Payload, &payload); err != nil {
			return invalid(req, err)
		}
		if err := s.register(req.ProjectID, payload.Root); err != nil {
			return invalid(req, err)
		}
		return transport.Reply(req, struct{}{}, nil)
	case transport.ListProjects:
		var payload struct{}
		if err := transport.Decode(req.Payload, &payload); err != nil {
			return invalid(req, err)
		}
		s.mu.RLock()
		projects := make([]model.ProjectSummary, 0, len(s.projects))
		for _, session := range s.projects {
			projects = append(projects, session.manager.ProjectSummary())
		}
		s.mu.RUnlock()
		sort.Slice(projects, func(i, j int) bool {
			if projects[i].Name == projects[j].Name {
				return projects[i].Root < projects[j].Root
			}
			return projects[i].Name < projects[j].Name
		})
		return transport.Reply(req, projects, nil)
	default:
		s.mu.RLock()
		session := s.projects[req.ProjectID]
		s.mu.RUnlock()
		if session == nil {
			return invalid(req, errors.New("project is not registered; open lazyrun in its directory"))
		}
		return session.handle(req)
	}
}

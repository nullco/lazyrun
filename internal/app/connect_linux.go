//go:build linux

package app

import (
	"context"
	"errors"

	"github.com/nullco/lazyrun/internal/config"
	"github.com/nullco/lazyrun/internal/supervisor"
)

// ConnectDashboard uses the current project if present, otherwise the live
// supervisor's project list. Invalid local configurations still fail closed.
func ConnectDashboard(ctx context.Context, dir string, opts supervisor.LaunchOptions) (*supervisor.Connection, error) {
	if _, err := config.Discover(dir); err != nil {
		if errors.Is(err, config.ErrNotFound) {
			return supervisor.ConnectExisting(ctx)
		}
		return nil, err
	}
	return Connect(ctx, dir, opts)
}

// Connect validates before launching/synchronizing a supervisor. Opening a
// connection never starts a configured command. Each call refreshes config.
func Connect(ctx context.Context, dir string, opts supervisor.LaunchOptions) (*supervisor.Connection, error) {
	p, data, err := config.LoadBytes(dir)
	if err != nil {
		return nil, err
	}
	connection, err := supervisor.Ensure(ctx, p, opts)
	if err != nil {
		return nil, err
	}
	if err := connection.Client.Sync(ctx, data); err != nil {
		connection.Close()
		return nil, err
	}
	return connection, nil
}

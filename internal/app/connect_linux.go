//go:build linux

package app

import (
	"context"

	"github.com/nullco/lazyrun/internal/config"
	"github.com/nullco/lazyrun/internal/supervisor"
)

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

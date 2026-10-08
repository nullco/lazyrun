// lazyrun's dashboard is still in progress. The headless CLI exercises the same
// detached supervisor/client path that will back the TUI.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/integrii/flaggy"
	"lazyrun/internal/app"
	"lazyrun/internal/model"
	"lazyrun/internal/supervisor"
)

var version = "dev"

func main() {
	if len(os.Args) == 3 && os.Args[1] == supervisor.InternalMode {
		signal.Ignore(syscall.SIGHUP)
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
		defer cancel()
		if err := supervisor.RunInternal(ctx, os.Args[2], version); err != nil {
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	parser := flaggy.NewParser("lazyrun")
	parser.Description = "A Linux-first project command dashboard (headless client available; TUI in progress)."
	parser.Version = version
	parser.ShowCompletion = false
	var check, state bool
	var start, stop, restart, logs, runID string
	var after uint64
	parser.Bool(&check, "", "check", "Validate configuration only; do not launch the supervisor")
	parser.Bool(&state, "", "state", "Connect/synchronize and print project state as JSON (default until TUI)")
	parser.String(&start, "", "start", "Start a configured alias; commands survive client exit")
	parser.String(&stop, "", "stop", "Gracefully stop an alias's owned process group")
	parser.String(&restart, "", "restart", "Gracefully restart/rerun an alias using this environment")
	parser.String(&logs, "", "logs", "Read an alias's bounded output as JSON (data is base64)")
	parser.String(&runID, "", "run-id", "Expected run ID for --logs; defaults to latest run")
	parser.UInt64(&after, "", "after", "Log byte cursor for --logs")
	if err := parser.ParseArgs(os.Args[1:]); err != nil {
		return err
	}
	actions := 0
	for _, active := range []bool{check, state, start != "", stop != "", restart != "", logs != ""} {
		if active {
			actions++
		}
	}
	if actions > 1 {
		return fmt.Errorf("choose only one of --check, --state, --start, --stop, --restart, --logs")
	}
	if logs == "" && (runID != "" || after != 0) {
		return fmt.Errorf("--run-id and --after require --logs")
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	if check {
		return app.Check(dir, os.Stdout)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	connection, err := app.Connect(ctx, dir, supervisor.LaunchOptions{})
	if err != nil {
		return err
	}
	defer connection.Close()
	var result any
	switch {
	case start != "":
		result, err = connection.Client.Start(ctx, start, os.Environ())
	case stop != "":
		result, err = connection.Client.Stop(ctx, stop)
	case restart != "":
		result, err = connection.Client.Restart(ctx, restart, os.Environ())
	case logs != "":
		if runID == "" {
			var current model.State
			current, err = connection.Client.State(ctx)
			if err != nil {
				return err
			}
			for _, item := range current.Commands {
				if item.Run.Definition.Alias == logs {
					runID = item.Run.ID
					break
				}
			}
		}
		result, err = connection.Client.Logs(ctx, logs, runID, after, 0)
	default:
		result, err = connection.Client.State(ctx)
	}
	// Failed mutation responses can still contain a run (e.g. metadata write
	// failure after launch). Make that identity visible; never retry implicitly.
	if result != nil {
		if encodeErr := json.NewEncoder(os.Stdout).Encode(result); encodeErr != nil {
			return encodeErr
		}
	}
	return err
}

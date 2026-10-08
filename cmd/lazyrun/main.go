// lazyrun's dashboard is still in progress. The headless CLI exercises the same
// detached supervisor/client path that will back the TUI.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
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
	var afterValues []string
	var tailValues []int
	parser.Bool(&check, "", "check", "Validate configuration only; do not launch the supervisor")
	parser.Bool(&state, "", "state", "Connect/synchronize and print project state as JSON (default until TUI)")
	parser.String(&start, "", "start", "Start a configured alias; commands survive client exit")
	parser.String(&stop, "", "stop", "Gracefully stop an alias's owned process group")
	parser.String(&restart, "", "restart", "Gracefully restart/rerun an alias using this environment")
	parser.String(&logs, "", "logs", "Read an alias's bounded output as JSON (data is base64)")
	parser.String(&runID, "", "run-id", "Expected run ID for --logs; defaults to latest run")
	parser.StringSlice(&afterValues, "", "after", "Log byte cursor for --logs (otherwise read the recent tail)")
	parser.IntSlice(&tailValues, "", "tail", "Initial log lines; 0 means byte-bounded tail (defaults to logs.tail)")
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
	if len(afterValues) > 1 || len(tailValues) > 1 {
		return fmt.Errorf("--after and --tail can only be supplied once")
	}
	afterProvided, tailProvided := len(afterValues) == 1, len(tailValues) == 1
	if logs == "" && (runID != "" || afterProvided || tailProvided) {
		return fmt.Errorf("--run-id, --after and --tail require --logs")
	}
	tail := -1
	if tailProvided {
		tail = tailValues[0]
	}
	if (tailProvided && tail < 0) || (tailProvided && afterProvided) {
		return fmt.Errorf("--tail must be nonnegative and cannot be combined with --after")
	}
	var after uint64
	if afterProvided {
		var err error
		after, err = strconv.ParseUint(afterValues[0], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid --after cursor: %w", err)
		}
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
		var current model.State
		current, err = connection.Client.State(ctx)
		if err != nil {
			return err
		}
		if runID == "" {
			for _, item := range current.Commands {
				if item.Run.Definition.Alias == logs {
					runID = item.Run.ID
					break
				}
			}
		}
		if afterProvided {
			result, err = connection.Client.Logs(ctx, logs, runID, after, 0)
		} else {
			if tail == -1 {
				tail = current.Project.Logs.Tail
			}
			result, err = connection.Client.TailLogs(ctx, logs, runID, tail, 0)
		}
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

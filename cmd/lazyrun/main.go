// lazyrun opens a client-only dashboard; explicit flags provide headless actions.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/template"

	"github.com/integrii/flaggy"
	"github.com/nullco/lazyrun/internal/app"
	"github.com/nullco/lazyrun/internal/gui"
	"github.com/nullco/lazyrun/internal/model"
	"github.com/nullco/lazyrun/internal/supervisor"
	"github.com/nullco/lazyrun/internal/textutil"
	"golang.org/x/term"
)

func main() {
	version := binaryVersion()
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
		fmt.Fprintln(os.Stderr, textutil.EscapeControls(err.Error()))
		os.Exit(1)
	}
}

func run() error {
	version := binaryVersion()
	parser := flaggy.NewParser("lazyrun")
	parser.Description = "A Linux-first project command dashboard with detached supervision."
	parser.Version = version
	parser.ShowCompletion = false
	// flaggy can render unknown arguments and exit before returning an error.
	// Its help/parse-error path needs the same terminal boundary as our errors.
	help, err := template.New("help").Funcs(template.FuncMap{"escapeControls": textutil.EscapeControls}).Parse(`{{range .Lines}}{{escapeControls .}}{{"\n"}}{{end}}`)
	if err != nil {
		return err
	}
	parser.HelpTemplate = help
	var check, state bool
	var startValues, stopValues, restartValues, logsValues, runIDValues []string
	var afterValues []string
	var tailValues []int
	parser.Bool(&check, "", "check", "Validate configuration only; do not launch the supervisor")
	parser.Bool(&state, "", "state", "Connect/synchronize and print project state as JSON")
	parser.StringSlice(&startValues, "", "start", "Start a configured alias; commands survive client exit")
	parser.StringSlice(&stopValues, "", "stop", "Gracefully stop an alias's owned process group")
	parser.StringSlice(&restartValues, "", "restart", "Gracefully restart/rerun an alias using this environment")
	parser.StringSlice(&logsValues, "", "logs", "Read an alias's bounded output as JSON (data is base64)")
	parser.StringSlice(&runIDValues, "", "run-id", "Expected run ID for --logs; defaults to latest run")
	parser.StringSlice(&afterValues, "", "after", "Log byte cursor for --logs (otherwise read the recent tail)")
	parser.IntSlice(&tailValues, "", "tail", "Initial log lines; 0 means byte-bounded tail (defaults to logs.tail)")
	if err := parser.ParseArgs(os.Args[1:]); err != nil {
		return err
	}
	var values [5]string
	for i, flag := range []struct {
		name   string
		values []string
	}{
		{"start", startValues}, {"stop", stopValues}, {"restart", restartValues}, {"logs", logsValues}, {"run-id", runIDValues},
	} {
		if len(flag.values) > 1 {
			return fmt.Errorf("--%s can only be supplied once", flag.name)
		}
		if len(flag.values) == 1 {
			if strings.TrimSpace(flag.values[0]) == "" {
				return fmt.Errorf("--%s requires a nonempty value", flag.name)
			}
			values[i] = flag.values[0]
		}
	}
	start, stop, restart, logs, runID := values[0], values[1], values[2], values[3], values[4]
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
	if actions == 0 && (!term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd()))) {
		return fmt.Errorf("dashboard requires a terminal; use --state for JSON or --check to validate configuration")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	connection, err := app.Connect(ctx, dir, supervisor.LaunchOptions{})
	if err != nil {
		return err
	}
	defer connection.Close()
	if actions == 0 {
		return gui.Run(ctx, connection.Client, gui.Options{Environment: os.Environ(), Version: version})
	}
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

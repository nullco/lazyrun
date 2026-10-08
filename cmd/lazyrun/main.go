// lazyrun currently provides configuration validation. The detached supervisor
// and dashboard are intentionally not wired to the runtime proof of concept.
package main

import (
	"fmt"
	"os"

	"github.com/integrii/flaggy"

	"lazyrun/internal/app"
)

var version = "dev"

func main() {
	parser := flaggy.NewParser("lazyrun")
	parser.Description = "A Linux-first project command dashboard (implementation in progress)."
	parser.Version = version
	parser.ShowCompletion = false
	var check bool
	parser.Bool(&check, "", "check", "Validate and summarize the discovered lazyrun.yml; execute nothing")
	if err := parser.ParseArgs(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	dir, err := os.Getwd()
	if err == nil {
		err = app.Check(dir, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !check {
		fmt.Fprintln(os.Stderr, "Dashboard/supervisor not implemented yet. Use --check to validate configuration. Nothing was started.")
		os.Exit(1)
	}
}

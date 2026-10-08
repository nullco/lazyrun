// Package app coordinates discovery and application startup, without owning
// managed process lifetimes.
package app

import (
	"fmt"
	"io"

	"lazyrun/internal/config"
)

func Check(dir string, out io.Writer) error {
	p, err := config.Load(dir)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Project: %s\nRoot: %s\nID: %s\nServices: %d\nTasks: %d\n", p.Name, p.Root, p.ID, len(p.Services), len(p.Tasks))
	if err != nil {
		return err
	}
	for _, d := range p.Definitions() {
		if _, err := fmt.Fprintf(out, "  %s %s: not started\n", d.Kind, d.Alias); err != nil {
			return err
		}
	}
	return nil
}

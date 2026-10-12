package gui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nullco/lazyrun/internal/model"
)

type restartContextClient struct {
	*fakeClient
	deadlines chan time.Time
}

func (c *restartContextClient) Restart(ctx context.Context, alias string, env []string) (model.Run, error) {
	deadline, _ := ctx.Deadline()
	c.deadlines <- deadline
	return c.fakeClient.Restart(ctx, alias, env)
}

func TestRestartDoesNotImposeFixedDashboardDeadline(t *testing.T) {
	for _, parentDeadline := range []time.Time{{}, time.Now().Add(2 * time.Minute)} {
		ctx := context.Background()
		if !parentDeadline.IsZero() {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, parentDeadline)
			t.Cleanup(cancel)
		}
		c := &restartContextClient{fakeClient: &fakeClient{}, deadlines: make(chan time.Time, 1)}
		d := newDashboard(ctx, c, fixtureState(), Options{})
		t.Cleanup(func() { d.cancel(); d.workers.Wait() })
		d.setFocus(servicesPane)
		d.action("restart")
		d.workers.Wait()
		if got := <-c.deadlines; !got.Equal(parentDeadline) {
			t.Fatal("dashboard shortened restart's configurable wait", got, parentDeadline)
		}
		d.drain()
		if d.busy || len(c.calls) != 1 || c.calls[0] != "restart api" {
			t.Fatal("restart did not finish normally", d.notice, c.calls)
		}
	}
}

func TestCommandDetailsShowCurrentRestartTimeout(t *testing.T) {
	item := fixtureState().Commands[0]
	item.Run.Definition.RestartTimeout = 5 * time.Second
	item.Definition.RestartTimeout = 60 * time.Second
	if text := commandDetails(item); !strings.Contains(text, "Restart timeout: 1m0s\n") || strings.Contains(text, "Restart timeout: 5s") {
		t.Fatal("details displayed the latest run's obsolete timeout", text)
	}
}

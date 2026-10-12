package gui

import (
	"reflect"
	"time"

	"github.com/jesseduffield/gocui"
	"github.com/nullco/lazyrun/internal/model"
)

const updateInterval = 100 * time.Millisecond

// Keep one outstanding gocui callback. A wakeup is only a hint to drain the
// bounded mailbox, so many worker results can share one frame. There is no
// redraw timer while idle; the interval only limits frames during activity.
func (d *dashboard) updateLoop(update func(func(*gocui.Gui) error)) {
	defer func() {
		if d.ctx.Err() != nil {
			update(func(*gocui.Gui) error { return gocui.ErrQuit })
		}
	}()
	var nextUpdate time.Time
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.wake:
		}
		// A callback may already have drained results that supplied another hint.
		if len(d.events) == 0 {
			continue
		}
		if delay := time.Until(nextUpdate); delay > 0 && !wait(d.ctx, delay) {
			return
		}
		if d.ctx.Err() != nil {
			return
		}
		ack := make(chan struct{})
		update(func(*gocui.Gui) error {
			defer close(ack)
			d.drain()
			return nil
		})
		select {
		case <-d.ctx.Done():
			return
		case <-ack:
		}
		nextUpdate = time.Now().Add(updateInterval)
	}
}

func sameError(a, b error) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Error() == b.Error()
}

// Never suppress output or changes to retention, cursors, availability, or
// warnings. Only identical empty responses can be ignored after the first read.
func logReadChanged(previous, read model.LogRead) bool {
	if len(read.Data) > 0 || len(read.Records) > 0 {
		return true
	}
	previous.Data, read.Data = nil, nil
	previous.Records, read.Records = nil, nil
	return !reflect.DeepEqual(previous, read)
}

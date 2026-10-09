package gui

import "github.com/jesseduffield/gocui"

func (d *dashboard) mouseBindings(g *gocui.Gui) error {
	g.Mouse = true
	for i, name := range paneNames {
		p := pane(i)
		if err := g.SetViewClickBinding(&gocui.ViewMouseBinding{ViewName: name, Key: gocui.MouseLeft, Handler: func(point gocui.ViewMouseBindingOpts) error {
			d.mouseClick(g, p, point.X, point.Y)
			return nil
		}}); err != nil {
			return err
		}
		for _, wheel := range []struct {
			key   gocui.Key
			delta int
		}{{gocui.MouseWheelUp, -3}, {gocui.MouseWheelDown, 3}} {
			if err := g.SetViewClickBinding(&gocui.ViewMouseBinding{ViewName: name, Key: wheel.key, Handler: func(gocui.ViewMouseBindingOpts) error {
				d.mouseWheel(p, wheel.delta)
				return nil
			}}); err != nil {
				return err
			}
		}
	}
	for _, wheel := range []struct {
		key   gocui.Key
		delta int
	}{{gocui.MouseWheelUp, -3}, {gocui.MouseWheelDown, 3}} {
		if err := g.SetViewClickBinding(&gocui.ViewMouseBinding{ViewName: "help", Key: wheel.key, Handler: func(gocui.ViewMouseBindingOpts) error {
			if d.help && !d.small {
				d.helpTop = max(0, d.helpTop+wheel.delta)
			}
			return nil
		}}); err != nil {
			return err
		}
	}
	return nil
}

func (d *dashboard) mouseClick(g *gocui.Gui, p pane, x, y int) {
	if d.small || d.help {
		return
	}
	v, err := g.View(paneNames[p])
	if err != nil {
		return
	}
	width, height := v.Size()
	// Border and empty-space clicks focus the pane but never select a nearby
	// row. Use the last rendered alias so a state update cannot shift the hit.
	if (p == servicesPane || p == tasksPane) && x >= 0 && x < width && y >= 0 && y < height && y < len(d.visibleAliases[p]) {
		alias := d.visibleAliases[p][y]
		for _, item := range d.items(p) {
			if item.Run.Definition.Alias == alias {
				d.selected[p] = alias
				break
			}
		}
	}
	d.setFocus(p)
}

func (d *dashboard) mouseWheel(p pane, delta int) {
	if d.small || d.help {
		return
	}
	d.setFocus(p)
	d.move(delta)
}

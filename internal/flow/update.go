// SPDX-License-Identifier: MIT

package flow

import (
	"context"
	"errors"

	"github.com/borovaka/defy-installer/internal/release"
	"github.com/borovaka/defy-installer/internal/updater"
)

// update updates a keyboard running ZMK, keeping its settings, as
// planUpdate decides: the halves first, when they need it, then the
// Neuron, when it needs it. opening starts its first line, which goes on
// with what the Neuron runs.
func (r *run) update(ctx context.Context, f found, opening string) error {
	rep, err := r.read(ctx, f.serial, false)
	if err != nil {
		return err
	}
	v := rep.version
	r.ui.begin("%s %s", opening, runsZMK(v))
	if r.opts.Command == "install" {
		r.ui.say("ZMK is already installed, so this updates it and keeps your Bluetooth pairings.")
	}
	if v == nil {
		r.ui.say("Its firmware is too old to say what it runs, so the whole keyboard is written.")
	}
	c, err := r.target(ctx, v)
	if err != nil {
		return err
	}
	p := planUpdate(rep, c)
	if p.wake && !p.neuron {
		// Only the halves can tell whether anything needs writing.
		if p, err = r.wake(ctx, f.serial, c, p); err != nil {
			return err
		}
	}
	if p.upToDate() {
		r.ui.add(" It is up to date.")
		r.ui.end()
		if r.opts.Yes {
			// --yes answers questions, but does not ask for a write that
			// nothing needs.
			return r.done(c, false)
		}
		again, err := r.ui.ask(ctx, "Write it again anyway?")
		if err != nil {
			return err
		}
		if !again {
			return r.done(c, false)
		}
		p = p.again()
		if err := r.download(ctx, c, "Downloading it from GitHub..."); err != nil {
			return err
		}
	}
	if c.build == nil {
		if err := r.download(ctx, c, releaseLine(v, c.ch, c.rel, false)); err != nil {
			return err
		}
	}
	if p.neuron {
		if err := keepsSettings(c.build); err != nil {
			return err
		}
	}
	// With the Neuron to write, the halves are woken only now, after the
	// download and keepsSettings.
	if p.wake {
		if p, err = r.wake(ctx, f.serial, c, p); err != nil {
			return err
		}
	}
	return r.write(ctx, f.serial, c, p)
}

// keepsSettings refuses a build whose Neuron image reaches into the ZMK
// settings, which an update keeps.
func keepsSettings(b *release.Build) error {
	if _, err := b.Neuron.EraseEnd(true); err != nil {
		return guided(err, "This build's Neuron image reaches into the ZMK settings, so an update cannot keep them.\n"+
			"Write it with defy-installer advanced neuron write, without --keep-settings, which clears them.")
	}
	return nil
}

// write says what p writes of the build c, which is downloaded, and writes
// it to the keyboard whose Neuron runs ZMK with USB serial number serial:
// the halves first, through their cables, when p needs them, then the
// Neuron, when p needs it.
func (r *run) write(ctx context.Context, serial string, c *candidate, p plan) error {
	b := c.build
	for _, line := range p.lines() {
		r.ui.say("%s", line)
	}
	if !p.needsCables() {
		if p.neuron {
			if _, err := r.updateNeuron(ctx, serial, b, "", len(neuronBar)); err != nil {
				return err
			}
		}
		return r.done(c, false, "Done.")
	}

	steps := 2
	if p.unpaired {
		steps = 3
	}
	if err := r.cableStep(ctx, steps); err != nil {
		return err
	}
	r.step(2, steps, "writing, about 2 minutes. Do not unplug anything.")
	wrote, didReset, err := r.writeHalves(ctx, serial, b, p.force)
	if err != nil {
		return err
	}
	if !wrote {
		r.ui.say("  Both halves already run this build's images.")
	}
	back := true
	if p.neuron {
		if back, err = r.updateNeuron(ctx, serial, b, "  ", len(longestBar)); err != nil {
			return err
		}
	}
	r.phase = phaseWritten
	if wrote && back {
		if err := r.confirmHalves(ctx, serial, c); err != nil {
			return err
		}
	}
	if !didReset {
		if !back {
			// updateNeuron said to plug the Neuron in again.
			return r.done(c, true, "Once ZMK starts, keep typing with the cables in, or unplug them and switch the "+
				"halves on.")
		}
		return r.done(c, true, "Done. Keep typing with the cables in, or unplug them and switch the halves on.")
	}
	r.step(3, 3, "pairing")
	if back {
		if err := r.wireless(ctx, serial); err != nil {
			return err
		}
	} else {
		// Until ZMK starts, nothing can say when the halves connect.
		r.sayWireless()
	}
	return r.done(c, true, "Done. Your keyboard runs the new firmware.",
		"The halves are paired with the Neuron again; your computers keep their pairings.",
		"Keep typing with the cables in, or unplug them and switch the halves on.")
}

// wake asks for a key press on each half that p knows nothing of, then
// waits for their reports.
func (r *run) wake(ctx context.Context, serial string, c *candidate, p plan) (plan, error) {
	r.ui.say("%s", wakeLine(p.halves))
	return r.awaitHalves(ctx, serial, c, p)
}

// awaitHalves waits up to halvesWait for the reports of the halves that p
// knows nothing of, and returns p with what they reported.
func (r *run) awaitHalves(ctx context.Context, serial string, c *candidate, p plan) (plan, error) {
	states := p.halves
	err := r.useUpdater(ctx, serial, func(client *updater.Client) error {
		var err error
		states, err = r.waitForReports(ctx, client, c, states)
		return err
	})
	return p.woken(states), err
}

// updateNeuron writes the build's Neuron image, keeping the settings, then
// clears the keymap changes ZMK Studio saved. It reports whether ZMK came
// back.
func (r *run) updateNeuron(ctx context.Context, serial string, b *release.Build, indent string, width int) (bool, error) {
	r.phase = phaseNeuron
	bar := r.ui.bar(indent, neuronBar, width)
	n := Neuron{Serial: serial, Timeout: DefaultNeuronTimeout}
	var back bool
	err := whenBothPorts(ctx, func() error {
		var err error
		_, back, err = writeNeuron(ctx, r.detail, n, b.Neuron, true, nil, bar.update)
		return err
	})
	if err != nil {
		return false, err
	}
	r.phase = phaseWritten
	if !back {
		r.ui.say("%sThe Neuron is written, but ZMK did not start within %s. Unplug the Neuron and plug it back in.",
			indent, inWords(n.Timeout))
		r.ui.say("%sChanges made in ZMK Studio may still be in place; Studio's \"Restore Stock Settings\" clears them.", indent)
		return false, nil
	}
	return true, r.clearStudio(ctx, serial, indent)
}

// clearStudio clears the keymap changes ZMK Studio saved, once a new
// Neuron image runs. While Studio is unlocked it asks for Studio to be
// closed or locked, and tries again on Enter. When the changes cannot be
// cleared, it says how to clear them in Studio, and fails only when ctx
// ends.
func (r *run) clearStudio(ctx context.Context, serial, indent string) error {
	for {
		err := r.useUpdater(ctx, serial, func(c *updater.Client) error { return c.ClearKeymapChanges(ctx) })
		switch {
		case err == nil:
			r.detail.say("Cleared the keymap changes ZMK Studio saved.")
			return nil
		case ctx.Err() != nil:
			return err
		case !errors.Is(err, updater.ErrStudioUnlocked):
			r.detail.say("%v", err)
			r.ui.say("%sThe changes made in ZMK Studio could not be cleared. Studio's \"Restore Stock Settings\" "+
				"clears them.", indent)
			return nil
		}
		r.ui.say("%sZMK Studio is unlocked, so its changes cannot be cleared. Close or lock Studio, then press "+
			"Enter to try again.", indent)
		if err := r.ui.enter(ctx); err != nil {
			return err
		}
	}
}

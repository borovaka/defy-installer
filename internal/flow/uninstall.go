// SPDX-License-Identifier: MIT

package flow

import (
	"context"

	"github.com/borovaka/defy-installer/internal/neuron"
	"github.com/borovaka/defy-installer/internal/release"
)

// Uninstall puts Dygma's 2.3.0 firmware back on the whole keyboard, as
// advanced stock does, after a question: the Neuron's firmware and S140,
// then the keyscanner on both halves through Dygma's firmware. Dygma's
// files come from Dygma's GitHub, or from the folder opts.Firmware names.
// A Neuron that already runs Dygma's firmware goes straight to the halves.
func Uninstall(ctx context.Context, env Env, opts Options) error {
	r := newRun(ctx, env, opts)
	err := r.uninstall(ctx)
	r.ui.end()
	return r.guide(err)
}

func (r *run) uninstall(ctx context.Context) error {
	f, err := r.detect(ctx)
	if err != nil {
		return err
	}
	const whole = "This puts Dygma's 2.3.0 firmware back on the whole keyboard."
	switch f.mode {
	case neuron.NotNeuron:
		return noDefy()
	case neuron.ZMK:
		rep, err := r.read(ctx, f.serial, false)
		if err != nil {
			return err
		}
		name := "ZMK"
		if rep.version != nil {
			name = zmkName(rep.version.Module)
		}
		r.ui.say("Found your Defy. It runs %s.", name)
		r.ui.say(whole)
		r.ui.say("ZMK's Bluetooth pairings and any changes made in ZMK Studio will be lost.")
	case neuron.Stock:
		r.ui.say("Found your Defy. Its Neuron already runs Dygma's firmware.")
		r.ui.say("This puts Dygma's 2.3.0 firmware back on the halves too.")
	case neuron.Bootloader, neuron.Loader:
		r.ui.say("Found your Defy. %s", waiting(f.mode))
		r.ui.say(whole)
		r.ui.say("ZMK's Bluetooth pairings and any changes made in ZMK Studio will be lost.")
	}
	ok, err := r.ui.ask(ctx, "Continue?")
	if err != nil {
		return err
	}
	if !ok {
		r.ui.say("Nothing was written.")
		return nil
	}
	stock, err := r.stockFiles(ctx)
	if err != nil {
		return err
	}

	if err := r.cableStep(ctx, 3); err != nil {
		return err
	}

	r.step(2, 3, "writing, about 2 minutes. Do not unplug anything.")
	n := Neuron{Timeout: DefaultNeuronTimeout}
	port, running, err := runningStock(ctx, r.detail, n)
	if err != nil {
		return err
	}
	if !running {
		r.phase = phaseNeuron
		bar := r.ui.bar("  ", neuronBar, len(longestBar))
		err := whenBothPorts(ctx, func() error {
			var err error
			port, err = restoreStock(ctx, r.detail, n, stock.Neuron, bar.update)
			return err
		})
		if err != nil {
			return err
		}
		if port.Path == "" {
			r.phase = phaseWritten
			return guided(errNotReturned, "Dygma's firmware is written to the Neuron, but it did not start within %s. "+
				"%s, then run\n  %s\nagain: it goes on with the halves.", inWords(n.Timeout), replugNoButton,
				r.rerun("uninstall"))
		}
	}
	r.phase = phaseHalves
	if err := restoreHalves(ctx, r.detail, port, n.Timeout, stock.Keyscanner, r.escWait(), r.halfBars()); err != nil {
		return err
	}
	r.phase = phaseWritten

	r.step(3, 3, "finish")
	r.ui.say("  Unplug the Neuron from this computer and plug it back in.")
	r.ui.say("  Then, for each half: unplug its cable, wait 5 seconds, and switch it on.")
	r.ui.blank()
	r.ui.say("Done. Your keyboard runs Dygma's firmware again.")
	r.ui.say("Open Bazecor and restore your layout from your backup.")
	r.ui.say("Pair your computers over Bluetooth again.")
	return nil
}

// stockFiles reads Dygma's files from the folder --firmware names, or
// downloads them from Dygma's GitHub. Either way each must match its
// pinned SHA-256.
func (r *run) stockFiles(ctx context.Context) (*release.Stock, error) {
	if dir := r.opts.Firmware; dir != "" {
		s, err := r.opts.Releases.LoadStock(dir)
		if err != nil {
			return nil, err
		}
		r.ui.say("Read Dygma's firmware from %s. Checked.", dir)
		return s, nil
	}
	r.ui.begin("Downloading Dygma's firmware from github.com/Dygmalab...")
	s, err := r.opts.Releases.FetchStock(ctx)
	if err != nil {
		return nil, err
	}
	r.ui.add(" checked.")
	r.ui.end()
	return s, nil
}

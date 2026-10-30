// SPDX-License-Identifier: MIT

package flow

import (
	"context"
	"time"

	"github.com/borovaka/defy-installer/internal/neuron"
)

// driveStartWait is how long a start from the UF2 loader's drive may take
// to bring back ZMK or the drive. The loader shows its drive again at once
// when ZMK is incomplete, but only after three starts when ZMK does not
// confirm that it runs.
const driveStartWait = 30 * time.Second

// fromDrive updates a keyboard whose Neuron shows its UF2 loader's drive.
// The loader never writes ZMK's settings, so the update keeps them. It
// enters the bootloader from the loader's port and starts the loader,
// which starts ZMK when ZMK is complete. A ZMK that comes back is updated
// as one found running. When the drive comes back instead, writeFromDrive
// writes the Neuron again.
func (r *run) fromDrive(ctx context.Context, f found) error {
	r.ui.say("Found your Defy. %s", waiting(f.mode))
	r.ui.say("Starting ZMK on the Neuron...")
	r.phase = phaseStart
	p, back, err := startFromDrive(ctx, r.detail, Neuron{Serial: f.serial, Timeout: driveStartWait})
	switch {
	case err != nil:
		return err
	case !back:
		return guided(errNotReturned, "Neither ZMK nor the DEFY drive came back within %s, so nothing was written. %s",
			inWords(driveStartWait), r.replugFirst())
	}
	r.phase = phaseNone
	if neuron.ModeOf(p) == neuron.Loader {
		return r.writeFromDrive(ctx, f.serial)
	}
	if f, err = r.detect(ctx); err != nil {
		return err
	}
	if f.mode != neuron.ZMK {
		return guided(nil, "ZMK started on the Neuron, then stopped, so nothing was written. Run\n  %s\nagain.",
			r.rerun(r.opts.Command))
	}
	return r.update(ctx, f, "ZMK started.")
}

// writeFromDrive writes the Neuron with USB serial number serial, whose
// loader showed its drive again after a start, keeping its settings, then
// writes the halves as update would once they have reported. With no
// VERSION to go on, the build is chosen as for a first install.
func (r *run) writeFromDrive(ctx context.Context, serial string) error {
	r.ui.say("The DEFY drive came back, so ZMK on the Neuron is incomplete or does not start.")
	c, err := r.target(ctx, nil)
	if err != nil {
		return err
	}
	if c.build == nil {
		if err := r.download(ctx, c, releaseLine(nil, c.ch, c.rel, true)); err != nil {
			return err
		}
	}
	if err := keepsSettings(c.build); err != nil {
		return err
	}
	r.ui.say("Changes made in ZMK Studio will be lost. Your Bluetooth pairings are kept.")
	back, err := r.updateNeuron(ctx, serial, c.build, "", len(neuronBar))
	if err != nil {
		return err
	}
	if !back {
		// updateNeuron said to plug the Neuron in again.
		r.ui.say("Once ZMK starts, run\n  %s\nagain to check the halves. If the DEFY drive comes back instead, this "+
			"build does not start on the Neuron. Install another with --from or --firmware.", r.rerun(r.opts.Command))
		return nil
	}
	rep, err := r.read(ctx, serial, false)
	if err != nil {
		return err
	}
	p := planUpdate(rep, c).afterNeuron()
	if p.wake {
		// The Neuron has just restarted, so the halves are connecting again.
		r.ui.say("If a half is asleep, press Shift on it to wake it.")
		if p, err = r.awaitHalves(ctx, serial, c, p); err != nil {
			return err
		}
	}
	return r.write(ctx, serial, c, p)
}

// SPDX-License-Identifier: MIT

package flow

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/borovaka/defy-installer/internal/firmware"
	"github.com/borovaka/defy-installer/internal/neuron"
	"github.com/borovaka/defy-installer/internal/release"
	"github.com/borovaka/defy-installer/internal/updater"
)

// sides are the halves in the order the installer names them.
var sides = [...]firmware.Side{firmware.Left, firmware.Right}

// The labels of the write bars; a step's bars are padded to the longest.
const (
	neuronBar  = "Writing the Neuron"
	longestBar = "Writing the right half"
)

// Install installs ZMK or updates it, whichever the keyboard needs: a first
// install from Dygma's firmware, a Neuron left in its bootloader written
// again with its settings cleared, or an update of a keyboard running ZMK,
// or showing its UF2 loader's drive, which keeps its settings. Both install
// and update run it; when opts.Command is not what the keyboard needs, it
// says what it does instead.
func Install(ctx context.Context, env Env, opts Options) error {
	r := newRun(ctx, env, opts)
	err := r.install(ctx)
	r.ui.end()
	return r.guide(err)
}

func (r *run) install(ctx context.Context) error {
	f, err := r.detect(ctx)
	switch {
	case err != nil:
		return err
	case f.mode == neuron.NotNeuron:
		return noDefy()
	case f.mode == neuron.ZMK:
		return r.update(ctx, f, "Found your Defy.")
	case f.mode == neuron.Loader:
		return r.fromDrive(ctx, f)
	}
	return r.firstInstall(ctx, f)
}

// candidate is the build an install or update writes: a release, found by
// its manifest and downloaded only when it is needed, or a build read from
// this computer.
type candidate struct {
	ch    choice
	rel   *release.Release
	build *release.Build
}

func (c *candidate) manifest() *release.Manifest {
	if c.build != nil {
		return c.build.Manifest
	}
	return c.rel.Manifest
}

// halfCRC is the program CRC-32 of the build's package for side: from the
// package once it is read, and from the manifest before.
func (c *candidate) halfCRC(side firmware.Side) uint32 {
	switch {
	case c.build != nil && side == firmware.Right:
		return c.build.Right.ProgramCRC
	case c.build != nil:
		return c.build.Left.ProgramCRC
	case side == firmware.Right:
		return c.rel.Manifest.Right.ProgramCRC32
	}
	return c.rel.Manifest.Left.ProgramCRC32
}

// locate finds the build ch names, and says nothing: a build on this
// computer is read at once, and of a release only the manifest.
func (r *run) locate(ctx context.Context, ch choice) (*candidate, error) {
	c := &candidate{ch: ch}
	var err error
	if ch.src.Path != "" {
		c.build, err = r.opts.Releases.ReadLocal(ch.src.Path)
	} else {
		c.rel, err = r.opts.Releases.Resolve(ctx, ch.src)
	}
	return c, err
}

// target picks the build to install, says what decided it when that is a
// saved setting, and finds it. v is what the Neuron reports, or nil.
func (r *run) target(ctx context.Context, v *updater.Version) (*candidate, error) {
	ch, err := r.choose(v)
	if err != nil {
		return nil, err
	}
	r.notices(ctx, ch, v)
	return r.find(ctx, ch, v)
}

// find is locate for install and update: it says what a build on this
// computer holds, and words a path that is not there. v is what the Neuron
// reports, if it runs ZMK.
func (r *run) find(ctx context.Context, ch choice, v *updater.Version) (*candidate, error) {
	c, err := r.locate(ctx, ch)
	if ch.src.Path == "" {
		return c, err
	}
	switch {
	case errors.Is(err, fs.ErrNotExist) && ch.from == bySettingsFirmware:
		return nil, guided(err, "%s, saved in %s, is not there any more. Give the firmware's new place with\n"+
			"  defy-installer %s --firmware <zip or folder>\n%s", ch.src.Path, r.settingsName(), r.opts.Command,
			r.backToGitHub(v))
	case errors.Is(err, fs.ErrNotExist):
		return nil, guided(err, "%s is not there. Check the path, then try again.", ch.src.Path)
	case err != nil:
		return nil, err
	}
	r.ui.say("%s", localLine(ch.src.Path, c.build))
	return c, nil
}

// download downloads a release's images, after line, which says what they
// are. A build already read, from this computer or by an earlier
// download, is not read again.
func (r *run) download(ctx context.Context, c *candidate, line string) error {
	if c.build != nil {
		return nil
	}
	r.ui.begin("%s", line)
	b, err := r.opts.Releases.Download(ctx, c.rel)
	if err != nil {
		return err
	}
	r.ui.add(" checked.")
	r.ui.end()
	c.build = b
	return nil
}

// firstInstall installs ZMK on a keyboard that runs Dygma's firmware, or
// writes again a Neuron left in its bootloader, clearing its settings: the
// Neuron, then both halves with a split pairing reset, then the halves'
// first connection over Bluetooth.
func (r *run) firstInstall(ctx context.Context, f found) error {
	fromStock := f.mode == neuron.Stock
	if fromStock {
		r.ui.say("Found your Defy. It runs Dygma's firmware.")
		if r.opts.Command != "install" {
			r.ui.say("ZMK is not installed yet, so this installs it.")
		}
	} else {
		r.ui.say("Found your Defy. %s", waiting(f.mode))
	}
	c, err := r.target(ctx, nil)
	if err != nil {
		return err
	}
	if c.build == nil {
		if err := r.download(ctx, c, releaseLine(nil, c.ch, c.rel, true)); err != nil {
			return err
		}
	}
	b := c.build
	var ask askFunc
	if fromStock {
		r.ui.blank()
		r.ui.say("Installing ZMK erases the layout stored on the keyboard.")
		r.ui.say("Back it up in Bazecor first, then quit Bazecor.")
		ok, err := r.ui.ask(ctx, "Have you backed up your layout?")
		if err != nil {
			return err
		}
		if !ok {
			r.ui.say("Nothing was written. Back up your layout in Bazecor, then run\n  %s\nagain.",
				r.rerun(r.opts.Command))
			return nil
		}
		ask = r.detail.askStock(DefaultNeuronTimeout, r.escWait())
	} else {
		r.ui.say("This writes the Neuron again, then both halves. Writing the Neuron clears its Bluetooth pairings " +
			"and any changes made in ZMK Studio.")
	}

	if err := r.cableStep(ctx, 3, "  Keep the Neuron plugged into this computer."); err != nil {
		return err
	}

	r.step(2, 3, "writing, about 2 minutes. Do not unplug anything.")
	r.phase = phaseNeuron
	bar := r.ui.bar("  ", neuronBar, len(longestBar))
	n := Neuron{Timeout: DefaultNeuronTimeout}
	studio, back, err := writeNeuron(ctx, r.detail, n, b.Neuron, false, ask, bar.update)
	if err != nil {
		return err
	}
	if !back {
		r.phase = phaseWritten
		return guided(errNotReturned, "The Neuron is written, but ZMK did not start within %s. %s, then run\n  %s\n"+
			"again to write the halves.", inWords(n.Timeout), replugNoButton, r.rerun(r.opts.Command))
	}
	// ZMK runs on the Neuron again, so a run that stops from here on has
	// only the halves left to write.
	r.phase = phaseHalves
	if _, _, err := r.writeHalves(ctx, studio.Serial, b, true); err != nil {
		return err
	}
	r.phase = phaseWritten

	r.step(3, 3, "pairing")
	if err := r.wireless(ctx, studio.Serial); err != nil {
		return err
	}
	return r.done(c, true, "Done. Your keyboard runs ZMK.",
		"Keep typing with the cables in, or unplug them and switch the halves on.",
		"Pair your computers over Bluetooth again: ZMK keeps its own pairings.",
		"To try layout changes, open https://zmk.studio with the Neuron plugged in by USB.",
		"Studio changes last until the next update. For a lasting layout, make your own keymap repository.")
}

// cableStep is step 1 of steps, getting ready: it asks for both halves on
// their cables and switched off, says each line of extra, then waits for
// Enter.
func (r *run) cableStep(ctx context.Context, steps int, extra ...string) error {
	r.step(1, steps, "get ready")
	r.ui.say("  Plug both halves into the Neuron with their cables, and switch both halves off.")
	for _, line := range extra {
		r.ui.say("%s", line)
	}
	r.ui.say("  Press Enter when ready.")
	return r.ui.enter(ctx)
}

// halfBars returns a progress func that draws a bar for each half as its
// write starts, lined up with the Neuron's.
func (r *run) halfBars() firmware.HalfProgress {
	bars := map[firmware.Side]*bar{}
	return func(side firmware.Side, done, total int) {
		if bars[side] == nil {
			bars[side] = r.ui.bar("  ", fmt.Sprintf("Writing the %v half", side), len(longestBar))
		}
		bars[side].update(done, total)
	}
}

// writeHalves writes both halves through the half updater of the Neuron
// running ZMK with USB serial number serial, with a pairing reset as
// halvesSession decides. Unless force is set, a session whose PROBEs find
// the build's images on both halves closes without writing. When a half
// does not answer, it names the half and tries again on Enter. It reports
// whether it wrote the halves, and whether it reset the pairing.
func (r *run) writeHalves(ctx context.Context, serial string, b *release.Build, force bool) (wrote, reset bool, err error) {
	for {
		wrote, reset, err = halvesSession(ctx, r.detail, Halves{Serial: serial, Timeout: DefaultHalvesTimeout},
			b.Left, b.Right, halvesWrite{
				unlessCurrent: !force,
				opening:       func() { r.phase = phaseHalves },
				progress:      r.halfBars(),
			})
		if err == nil && !wrote {
			r.detail.say("Both halves hold this build's images, so the session closed without writing.")
		}
		if err == nil || ctx.Err() != nil {
			return wrote, reset, err
		}
		probe, isProbe := errors.AsType[*updater.ProbeError](err)
		switch {
		case isProbe:
			r.ui.say("  The %v half does not answer. Check that its cable is in the Neuron and the half is switched off.",
				probe.Side)
		case errors.Is(err, updater.ErrHalvesBusy):
			r.ui.say("  The Neuron is still busy with the halves. Wait a few seconds.")
		case errors.Is(err, updater.ErrHalvesSilent):
			r.ui.say("  The halves do not answer. Check that both cables are in the Neuron and both halves are " +
				"switched off.")
		default:
			return wrote, reset, err
		}
		r.detail.say("%v", err)
		r.ui.say("  Press Enter to try again.")
		if err := r.ui.enter(ctx); err != nil {
			return false, false, err
		}
	}
}

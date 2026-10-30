// SPDX-License-Identifier: MIT

package flow

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/borovaka/defy-installer/internal/firmware"
	"github.com/borovaka/defy-installer/internal/neuron"
	"github.com/borovaka/defy-installer/internal/updater"
)

// Status says what the keyboard runs, how the halves are connected and how
// their batteries stand, the latest build of the source an update would
// install, and the settings file in use. It writes nothing. Without the
// network it leaves out the latest build.
func Status(ctx context.Context, env Env, opts Options) error {
	r := newRun(ctx, env, opts)
	err := r.status(ctx)
	r.ui.end()
	return r.guide(err)
}

// statusLine prints one of status's lines: a label, then what it says.
func (r *run) statusLine(label, format string, args ...any) {
	r.ui.say("%-13s%s", label, fmt.Sprintf(format, args...))
}

func (r *run) status(ctx context.Context) error {
	f, err := r.detect(ctx)
	if err != nil {
		return err
	}
	var rep report
	switch f.mode {
	case neuron.NotNeuron:
		r.statusLine("Neuron", "not found. Plug it into this computer with its USB cable.")
	case neuron.Stock:
		r.statusLine("Neuron", "Dygma's firmware, connected by USB")
	case neuron.Bootloader:
		r.statusLine("Neuron", "waiting in its bootloader, after a write that did not finish. Keep it plugged in, and "+
			"run defy-installer install to finish, or defy-installer uninstall to put Dygma's firmware back. Either "+
			"writes the whole Neuron again, which clears its Bluetooth pairings and any changes made in ZMK Studio")
	case neuron.Loader:
		r.statusLine("Neuron", "waiting for a UF2 file on its DEFY drive. Unplug it and plug it back in to start ZMK. "+
			"If the drive comes back, copy the UF2 file again, or run defy-installer update")
		const unknown = "not known: the Neuron does not report the halves while it shows its DEFY drive"
		r.statusLine("Left half", unknown)
		r.statusLine("Right half", unknown)
	case neuron.ZMK:
		if rep, err = r.read(ctx, f.serial, true); err != nil {
			return err
		}
		r.statusLine("Neuron", "%s, connected by USB", describeNeuron(rep.version))
		r.statusLine("Left half", "%s", describeHalf(rep, firmware.Left))
		r.statusLine("Right half", "%s", describeHalf(rep, firmware.Right))
	}
	r.latest(ctx, f, rep)
	if s := r.opts.Settings; s.Path != "" {
		if s.Exists {
			r.ui.say("Settings: %s", s.Path)
		} else {
			r.ui.say("Settings: %s, not created yet", s.Path)
		}
	}
	return nil
}

// describeNeuron says what a Neuron running ZMK runs, as VERSION reads it.
func describeNeuron(v *updater.Version) string {
	if v == nil {
		return "ZMK, too old to say which version"
	}
	at := ""
	if v.Commit != "" {
		at = " at " + shortCommit(v.Commit)
	}
	switch {
	case isDefaultSource(v.Source):
		return fmt.Sprintf("%s, default layout%s", zmkName(v.Module), at)
	case v.Source == updater.LocalSource:
		return fmt.Sprintf("%s, a local build%s", zmkName(v.Module), at)
	}
	return fmt.Sprintf("%s, keymap %s%s", zmkName(v.Module), v.Source, at)
}

// describeHalf says what a half runs, how it is connected, and how its
// battery stands.
func describeHalf(rep report, side firmware.Side) string {
	if rep.halves == nil && rep.battery == nil {
		return "not known: this Neuron firmware does not report the halves"
	}
	var rec updater.HalfRecord
	if rep.halves != nil {
		rec = rep.halves.Side(side)
	}
	var bat updater.BatteryRecord
	if rep.battery != nil {
		bat = rep.battery.Side(side)
	}
	// The battery sample says which links are up. A cabled half can be on
	// both, and then it types over its cable. The version record says only
	// which link the last report came by, which alternates on a cabled
	// half, so it is the fallback.
	link := rec.Link
	if bat.Connected {
		switch {
		case bat.Cable:
			link = updater.Cable
		case bat.Bluetooth:
			link = updater.Bluetooth
		}
	}
	var parts []string
	switch {
	case rec.Known():
		parts = append(parts, zmkName(rec.Report.Module))
	case link != updater.NoLink:
		parts = append(parts, "ZMK, version not reported")
	}
	switch link {
	case updater.Cable:
		parts = append(parts, "connected by cable")
	case updater.Bluetooth:
		parts = append(parts, "connected wirelessly")
	default:
		parts = append(parts, "not connected: asleep or switched off")
	}
	if bat.Connected {
		switch {
		case bat.Charge == updater.Charging:
			parts = append(parts, "charging")
		case bat.Valid:
			parts = append(parts, fmt.Sprintf("battery %d%%", bat.Level))
		case bat.Charge == updater.Charged:
			parts = append(parts, "charged")
		}
	}
	return strings.Join(parts, ", ")
}

// latest says what the latest build of the source an update would install
// is, and whether the keyboard runs it, as update would decide. Without
// the network it says nothing.
func (r *run) latest(ctx context.Context, f found, rep report) {
	ch, err := r.choose(rep.version)
	if err != nil {
		// choose fails only on a source the Neuron reports that it cannot
		// follow.
		if rep.version.Source == updater.LocalSource {
			r.ui.say("Updates: the keyboard runs a local build, so give defy-installer update --from or --firmware.")
		} else {
			r.ui.say("Updates: the Neuron reports that it was built from %q, which the installer cannot follow, so "+
				"give defy-installer update --from or --firmware.", rep.version.Source)
		}
		return
	}
	c, err := r.locate(ctx, ch)
	local := ch.src.Path != ""
	switch {
	case err != nil && ch.from == bySettingsFirmware:
		r.ui.say("Updates install from %s, saved in %s, which cannot be read: %v", ch.src.Path, r.settingsName(), err)
		r.ui.say("%s", r.backToGitHub(rep.version))
		return
	case err != nil && local:
		r.ui.say("%s cannot be read: %v", ch.src.Path, err)
		return
	case err != nil:
		r.detail.say("The latest build could not be checked: %v", err)
		return
	}
	var subject, what string
	if local {
		subject = "The build in " + filepath.Base(ch.src.Path)
		if ch.from == bySettingsFirmware {
			subject += ", saved in " + r.settingsName() + ","
		}
		what = "without a manifest"
		if m := c.build.Manifest; m != nil {
			what = zmkName(m.Module)
			if !isDefaultSource(m.Source) && m.Commit != "" {
				what = shortCommit(m.Commit)
			}
		}
	} else {
		name := ch.src.Repo
		if ch.src.IsDefault() {
			name = "the default layout"
		}
		switch {
		case ch.src.Tag != "":
			subject = fmt.Sprintf("Release %s of %s", c.rel.Tag, name)
		case ch.src.IsDefault():
			subject = "The latest release"
		default:
			subject = "The latest build of " + name
		}
		what = shortCommit(c.rel.Manifest.Commit)
		if ch.src.IsDefault() {
			what = zmkName(c.rel.Manifest.Module)
		}
	}
	r.ui.say("%s is %s: %s.", subject, what, verdict(f, rep, c))
}

// verdict says whether the keyboard runs the build c, as update would
// decide, and if not, what to do.
func verdict(f found, rep report, c *candidate) string {
	switch {
	case f.mode == neuron.Loader:
		// update starts ZMK from the drive, and keeps its settings.
		return "defy-installer update installs it"
	case f.mode != neuron.ZMK:
		return "defy-installer install installs it"
	}
	p := planUpdate(rep, c)
	switch {
	case p.upToDate():
		return "up to date"
	case p.neuron:
		return "defy-installer update installs it"
	case p.unpaired:
		return "the Neuron runs it, but is not paired with both halves. defy-installer update writes the halves " +
			"and pairs them again"
	case slices.Contains(p.halves[:], halfDiffers):
		return "defy-installer update installs it on the halves"
	case p.wake:
		name, both := unknownHalves(p.halves)
		if both {
			return "the Neuron runs it, and the halves did not say what they run. Press Shift on each, then run " +
				"defy-installer status again"
		}
		return fmt.Sprintf("the Neuron runs it, and %s did not say what it runs. Press Shift on it, then run "+
			"defy-installer status again", name)
	}
	return "the Neuron runs it, and its firmware cannot say what the halves run. defy-installer update checks " +
		"them through their cables"
}

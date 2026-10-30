// SPDX-License-Identifier: MIT

package flow

import (
	"cmp"
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/borovaka/defy-installer/internal/neuron"
	"github.com/borovaka/defy-installer/internal/release"
	"github.com/borovaka/defy-installer/internal/settings"
	"github.com/borovaka/defy-installer/internal/updater"
	"github.com/borovaka/defy-installer/internal/usbserial"
)

// Releases finds the builds that install and update write, and Dygma's
// files for uninstall; *release.Client is one.
type Releases interface {
	Resolve(ctx context.Context, src release.Source) (*release.Release, error)
	Download(ctx context.Context, r *release.Release) (*release.Build, error)
	ReadLocal(path string) (*release.Build, error)
	LatestTag(ctx context.Context, repo string) (string, error)
	FetchStock(ctx context.Context) (*release.Stock, error)
	LoadStock(dir string) (*release.Stock, error)
}

// Options are what the user's commands, install, update, uninstall and
// status, take besides Env.
type Options struct {
	// Command is the command run, as the messages name it.
	Command string
	// From is --from, a release on GitHub; the zero Source when it is not
	// given.
	From release.Source
	// Firmware is --firmware, an absolute path: a zip or a folder of a
	// build, or for uninstall a folder of Dygma's files. At most one of
	// From and Firmware is set.
	Firmware string
	// Settings is the settings file. Install and update save From or
	// Firmware in it once the keyboard runs that build. Nil is an empty
	// File without a Path, which nothing is saved in.
	Settings *settings.File
	// Releases finds builds and Dygma's files.
	Releases Releases
	// In gives the answers to questions, and the Enter presses.
	In io.Reader
	// Echo prints each answer read, for input that is not a terminal.
	Echo bool
	// Discard, if set, discards the input waiting in In before each
	// question and each wait for Enter, such as keys pressed on the Defy,
	// which reach this terminal through the Neuron. It is set when In is a
	// terminal.
	Discard func()
	// Yes answers yes to every question. A keyboard that is up to date is
	// not written again, and steps that wait for Enter still wait.
	Yes bool
	// Verbose prints the detail of each step: addresses, CRCs and ports.
	Verbose bool
	// Terminal is set when Env.Out is a terminal, where progress bars are
	// drawn again in place.
	Terminal bool
}

// phase is how far a run has got, which decides what an error says to do
// next.
type phase int

const (
	phaseNone phase = iota
	// phaseStart: the Neuron may be in its bootloader, entered from the UF2
	// loader's drive, with nothing written.
	phaseStart
	// phaseNeuron: the Neuron may be in its bootloader, partly written.
	phaseNeuron
	// phaseHalves: the halves may be partly written.
	phaseHalves
	// phaseWritten: the writes have finished.
	phaseWritten
)

// run is one run of a user's command.
type run struct {
	// detail is the Env the run reaches the keyboard through. Its Out
	// takes the advanced flows' detail lines, shown with --verbose and
	// dropped otherwise; its Open waits for Enter while another program
	// holds a port.
	detail Env
	ui     *ui
	opts   Options
	phase  phase
	// chosen is the build the run installs, once choose has picked it.
	chosen *choice
}

func newRun(ctx context.Context, env Env, opts Options) *run {
	in := opts.In
	if in == nil {
		in = strings.NewReader("")
	}
	u := newUI(env.Out, in, opts.Discard)
	u.yes, u.echo = opts.Yes, opts.Echo
	// Detail lines would break a bar drawn in place.
	u.terminal = opts.Terminal && !opts.Verbose
	opts.Settings = cmp.Or(opts.Settings, &settings.File{})
	r := &run{detail: env, ui: u, opts: opts}
	r.detail.Out = u
	if !opts.Verbose {
		r.detail.Out = io.Discard
	}
	open := env.Open
	r.detail.Open = func(path string, timeout time.Duration) (Port, error) {
		for {
			p, err := open(path, timeout)
			if !errors.Is(err, usbserial.ErrBusy) {
				return p, err
			}
			u.say("Another program, usually Bazecor or ZMK Studio in a browser, is using the keyboard. Quit it, then " +
				"press Enter to try again.")
			if err := u.enter(ctx); err != nil {
				return nil, err
			}
		}
	}
	return r
}

// found is the Neuron connected, as its USB ports show it.
type found struct {
	// mode is neuron.NotNeuron when none is connected.
	mode neuron.Mode
	// serial is the USB serial number of a Neuron running ZMK, or showing
	// its UF2 loader's drive, which has ZMK's.
	serial string
}

// detect finds the one Neuron connected.
func (r *run) detect(ctx context.Context) (found, error) {
	ports, err := r.detail.List(ctx)
	if err != nil {
		return found{}, err
	}
	switch devices := neurons(ports); len(devices) {
	case 0:
		return found{}, nil
	case 1:
		f := found{mode: devices[0].mode}
		if f.mode == neuron.ZMK || f.mode == neuron.Loader {
			f.serial = devices[0].ports[0].Serial
		}
		return f, nil
	}
	return found{}, guided(nil, "More than one Defy is connected. Unplug all but one, then try again.")
}

// noDefy is the error when no Neuron is connected.
func noDefy() error {
	return guided(nil, "No Defy found. Plug the Neuron into this computer with its USB cable.\nIf Bazecor is open, quit it.")
}

// report is what a Neuron running ZMK says about the keyboard. A field is
// nil when the Neuron's firmware does not answer its request.
type report struct {
	version *updater.Version
	halves  *updater.HalfVersions
	pairing *updater.Pairing
	battery *updater.Battery
}

// read asks the Neuron running ZMK with USB serial number serial what it
// and the halves run, and with battery set, how the halves' batteries
// stand.
func (r *run) read(ctx context.Context, serial string, battery bool) (report, error) {
	var rep report
	err := r.useUpdater(ctx, serial, func(c *updater.Client) error { return rep.read(ctx, c, battery) })
	if err == nil {
		r.sayReport(rep)
	}
	return rep, err
}

func (rep *report) read(ctx context.Context, c *updater.Client, battery bool) (err error) {
	if rep.version, err = optional(c.Version(ctx)); err != nil {
		return err
	}
	if rep.halves, err = optional(c.HalfVersions(ctx)); err != nil {
		return err
	}
	if rep.pairing, err = optional(c.Pairing(ctx)); err != nil {
		return err
	}
	if battery {
		rep.battery, err = optional(c.Battery(ctx))
	}
	return err
}

// optional returns a request's reply v, or nil when the Neuron's firmware
// does not answer the request.
func optional[T any](v T, err error) (*T, error) {
	switch {
	case err == nil:
		return &v, nil
	case errors.Is(err, updater.ErrUnsupported), errors.Is(err, updater.ErrNoPairing):
		return nil, nil
	}
	return nil, err
}

// sayReport gives the detail of what the Neuron reported.
func (r *run) sayReport(rep report) {
	if v := rep.version; v != nil {
		r.detail.say("VERSION: module %s, source %s, commit %s.", v.Module, v.Source, cmp.Or(v.Commit, "none"))
	} else {
		r.detail.say("VERSION: this Neuron firmware does not answer it.")
	}
	if rep.halves == nil {
		r.detail.say("HALF VERSIONS: this Neuron firmware does not answer it.")
	} else {
		for _, side := range sides {
			h := rep.halves.Side(side)
			if !h.Known() {
				r.detail.say("HALF VERSIONS: no record from the %v half.", side)
				continue
			}
			r.detail.say("HALF VERSIONS: the %v half runs module %s, program CRC 0x%08x, seal version %d; "+
				"reported by %v %v ago.", side, h.Report.Module, h.Report.ProgramCRC, h.Report.SealVersion, h.Link,
				h.Age.Round(time.Millisecond))
		}
	}
	if rep.pairing != nil {
		r.detail.say("PAIRING: %s.", describePairing(*rep.pairing))
	}
}

// step starts a numbered step, such as "Step 1 of 3: get ready", after a
// blank line.
func (r *run) step(n, of int, what string) {
	r.ui.blank()
	r.ui.say("Step %d of %d: %s", n, of, what)
}

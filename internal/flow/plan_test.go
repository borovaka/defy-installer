// SPDX-License-Identifier: MIT

package flow

import (
	"slices"
	"testing"
	"testing/synctest"

	"github.com/borovaka/defy-installer/internal/neuron"
	"github.com/borovaka/defy-installer/internal/updater"
	ut "github.com/borovaka/defy-installer/internal/updater/updatertest"
)

// reported is what HALF VERSIONS reads from a Neuron with the halves
// given; a half without a link has not reported.
func reported(t *testing.T, left, right ut.Half) *updater.HalfVersions {
	t.Helper()
	d := newDefy(t, "zmk")
	d.updater.Version = ut.VersionPayload("v0.2.0", "alice/my-defy", commitB)
	d.updater.Halves = [2]ut.Half{left, right}
	var h updater.HalfVersions
	err := withUpdater(t.Context(), d.env(), Halves{Serial: testSerial, Timeout: DefaultHalvesTimeout},
		func(c *updater.Client) error {
			var err error
			h, err = c.HalfVersions(t.Context())
			return err
		})
	if err != nil {
		t.Fatal(err)
	}
	return &h
}

// The plan's lines that update runs say most often.
const (
	studioLost = "Changes made in ZMK Studio will be lost. Your Bluetooth pairings are kept."
	neuronOnly = "Both halves already run this build's images, so only the Neuron needs writing."
)

func TestPlanUpdate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := testBuild(t, "alice/my-defy", commitB, 0xa5)
		c := &candidate{build: b}
		runs := &updater.Version{Module: "v0.2.0", Source: "alice/my-defy", Commit: commitB}
		other := &updater.Version{Module: "v0.2.0", Source: "alice/my-defy", Commit: commitA}
		otherModule := &updater.Version{Module: "v0.1.0", Source: "alice/my-defy", Commit: commitB}
		current := ut.Half{CRC: b.Left.ProgramCRC, Link: ut.Bluetooth}
		currentRight := ut.Half{CRC: b.Right.ProgramCRC, Link: ut.Cable}
		differs := ut.Half{CRC: 1, Link: ut.Bluetooth}
		asleep := ut.Half{}
		paired := &updater.Pairing{Slots: [2]updater.Slot{3, 3}}
		unpaired := &updater.Pairing{Slots: [2]updater.Slot{3, 1}}
		const (
			reset       = "The Neuron is not paired with both halves, so both halves are written and paired with it again, which clears the halves' Bluetooth settings."
			halvesToo   = "The halves need new firmware too."
			halvesOnly  = "The Neuron already runs this build, so only the halves need new firmware."
			checkCables = "The halves did not say what they run, so the installer checks them through their cables."
			checkOnly   = "The Neuron already runs this build. The halves did not say what they run, so the installer checks them through their cables."
			halvesRun   = "Both halves already run this build's images."
		)
		for _, tt := range []struct {
			name string
			rep  report
			want plan
			// upToDate and the lines said before writing.
			upToDate bool
			lines    []string
		}{
			{"up to date", report{runs, reported(t, current, currentRight), paired, nil},
				plan{halves: bothCurrent}, true, []string{neuronOnly}},
			{"a keymap change", report{other, reported(t, current, currentRight), paired, nil},
				plan{neuron: true, halves: bothCurrent}, false, []string{neuronOnly, studioLost}},
			// The same keymap commit, built with another module.
			{"a module change", report{otherModule, reported(t, differs, currentRight), paired, nil},
				plan{neuron: true, halves: [2]halfState{halfDiffers, halfCurrent}}, false, []string{halvesToo, studioLost}},
			{"a new release", report{other, reported(t, differs, currentRight), paired, nil},
				plan{neuron: true, halves: [2]halfState{halfDiffers, halfCurrent}}, false, []string{halvesToo, studioLost}},
			{"halves alone", report{runs, reported(t, current, differs), paired, nil},
				plan{halves: [2]halfState{halfCurrent, halfDiffers}}, false, []string{halvesOnly}},
			{"an asleep half", report{runs, reported(t, current, asleep), paired, nil},
				plan{halves: [2]halfState{halfCurrent, halfUnknown}, wake: true}, false, []string{checkOnly}},
			{"an asleep half and a new Neuron image", report{other, reported(t, asleep, asleep), paired, nil},
				plan{neuron: true, wake: true}, false, []string{checkCables, studioLost}},
			{"firmware without HALF VERSIONS", report{runs, nil, paired, nil},
				plan{}, false, []string{checkOnly}},
			{"firmware without VERSION", report{nil, nil, nil, nil},
				plan{neuron: true, force: true}, false, []string{studioLost}},
			{"firmware without VERSION, unpaired", report{nil, nil, unpaired, nil},
				plan{neuron: true, force: true, unpaired: true}, false, []string{reset, studioLost}},
			// An unpaired Neuron gets the cable step and a pairing reset
			// even with current halves, and asks no half to wake.
			{"unpaired, halves current", report{runs, reported(t, current, currentRight), unpaired, nil},
				plan{halves: bothCurrent, unpaired: true}, false, []string{reset}},
			{"unpaired, a half asleep", report{other, reported(t, asleep, currentRight), unpaired, nil},
				plan{neuron: true, halves: [2]halfState{halfUnknown, halfCurrent}, unpaired: true}, false,
				[]string{reset, studioLost}},
		} {
			p := planUpdate(tt.rep, c)
			if p != tt.want || p.upToDate() != tt.upToDate || !slices.Equal(p.lines(), tt.lines) {
				t.Errorf("%s: got %+v, up to date %v, lines %q\nwant %+v, %v, %q", tt.name, p, p.upToDate(), p.lines(),
					tt.want, tt.upToDate, tt.lines)
			}
		}

		// Woken halves are known; a keyboard written again gets everything.
		p := planUpdate(report{runs, reported(t, current, asleep), paired, nil}, c)
		if got := p.woken(bothCurrent); !got.upToDate() || got.wake {
			t.Errorf("woken: %+v", got)
		}
		if got := p.woken(bothCurrent).again(); got.upToDate() || !got.needsCables() || !slices.Equal(got.lines(),
			[]string{studioLost}) {
			t.Errorf("again: %+v, %q", got, got.lines())
		}

		// Once the Neuron is written from its drive, the halves alone are
		// left, even when the Neuron cannot say it runs the build.
		for _, tt := range []struct {
			rep   report
			lines []string
		}{
			{report{runs, reported(t, current, currentRight), paired, nil}, []string{halvesRun}},
			{report{other, reported(t, current, currentRight), paired, nil}, []string{halvesRun}},
			{report{runs, reported(t, differs, currentRight), paired, nil}, []string{halvesToo}},
			{report{runs, reported(t, asleep, currentRight), paired, nil}, []string{checkCables}},
			{report{runs, reported(t, current, currentRight), unpaired, nil}, []string{reset}},
		} {
			got := planUpdate(tt.rep, c).afterNeuron()
			if got.neuron || !slices.Equal(got.lines(), tt.lines) {
				t.Errorf("after the Neuron: %+v, %q, want %q", got, got.lines(), tt.lines)
			}
		}
	})
}

// TestStatusAgreesWithUpdate gives the verdict of status for each kind of
// plan: up to date exactly when update writes nothing.
func TestStatusAgreesWithUpdate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := testBuild(t, "alice/my-defy", commitB, 0xa5)
		c := &candidate{build: b}
		zmk := found{mode: neuron.ZMK, serial: testSerial}
		runs := &updater.Version{Module: "v0.2.0", Source: "alice/my-defy", Commit: commitB}
		other := &updater.Version{Module: "v0.2.0", Source: "alice/my-defy", Commit: commitA}
		current, currentRight := ut.Half{CRC: b.Left.ProgramCRC, Link: ut.Bluetooth}, ut.Half{CRC: b.Right.ProgramCRC, Link: ut.Bluetooth}
		paired := &updater.Pairing{Slots: [2]updater.Slot{3, 3}}
		unpaired := &updater.Pairing{Slots: [2]updater.Slot{3, 0}}
		for _, tt := range []struct {
			f    found
			rep  report
			want string
		}{
			{found{mode: neuron.Stock}, report{}, "defy-installer install installs it"},
			{found{mode: neuron.Bootloader}, report{}, "defy-installer install installs it"},
			// update starts ZMK from the drive, and keeps the settings.
			{found{neuron.Loader, testSerial}, report{}, "defy-installer update installs it"},
			{zmk, report{runs, reported(t, current, currentRight), paired, nil}, "up to date"},
			{zmk, report{other, reported(t, current, currentRight), paired, nil}, "defy-installer update installs it"},
			{zmk, report{runs, reported(t, current, currentRight), unpaired, nil}, "the Neuron runs it, but is not " +
				"paired with both halves. defy-installer update writes the halves and pairs them again"},
			{zmk, report{runs, reported(t, ut.Half{CRC: 1, Link: ut.Bluetooth}, currentRight), paired, nil},
				"defy-installer update installs it on the halves"},
			{zmk, report{runs, reported(t, current, ut.Half{}), paired, nil}, "the Neuron runs it, and the right half " +
				"did not say what it runs. Press Shift on it, then run defy-installer status again"},
			{zmk, report{runs, reported(t, ut.Half{}, ut.Half{}), paired, nil}, "the Neuron runs it, and the halves " +
				"did not say what they run. Press Shift on each, then run defy-installer status again"},
			{zmk, report{runs, nil, paired, nil}, "the Neuron runs it, and its firmware cannot say what the halves " +
				"run. defy-installer update checks them through their cables"},
		} {
			if got := verdict(tt.f, tt.rep, c); got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
			if p := planUpdate(tt.rep, c); tt.f.mode == neuron.ZMK && p.upToDate() != (tt.want == "up to date") {
				t.Errorf("%q, but the plan's up to date is %v", tt.want, p.upToDate())
			}
		}
	})
}
